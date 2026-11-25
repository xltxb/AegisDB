package service

// 在线加 / 删索引任务的运行器(.scratch/osc-product 工单 02)。
//
// 两种跑法,同一个核心(runOSC):
//   - 终端发起、审批通过后点「执行」:StartOSCJob 在后台跑,跑完回调(回写审批单);
//   - 发布流水线的执行阶段:RunOSCJobSync 就地跑完,阶段一直是运行中,进度经回调写进阶段日志。
//
// 进度每隔 oscPersistEvery 从引擎的 Status 落库,页面读库不读进程内存。暂停 / 继续 / 中止经
// 进程内的 oscRunning 找到正在跑的 Migration;网关重启后进程内的表没了,库里 running 的任务由
// ReconcileStuckOSCJobs 收尾:有检查点的判 interrupted 等人续跑 / 放弃,其余判失败并尽力删掉残留的影子表。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/osc"
)

const oscPersistEvery = 3 * time.Second

// oscRegistry 是进程里正在跑的迁移,按任务 id。
type oscRegistry struct {
	mu   sync.Mutex
	runs map[int64]*osc.Migration
}

var oscRunning = &oscRegistry{runs: map[int64]*osc.Migration{}}

func (r *oscRegistry) put(id int64, m *osc.Migration) {
	r.mu.Lock()
	r.runs[id] = m
	r.mu.Unlock()
}
func (r *oscRegistry) drop(id int64) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}
func (r *oscRegistry) get(id int64) *osc.Migration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}
func (r *oscRegistry) ids() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int64, 0, len(r.runs))
	for id := range r.runs {
		out = append(out, id)
	}
	return out
}

// InterruptRunningOSC 打断本进程里所有在跑的迁移,影子表与检查点都留着、任务仍记为 running ——
// 库端看到的与进程直接退出一样,下次启动由 ReconcileOSCJobs 判为可续跑。等它们都停下再返回
// (至多 wait)。测试用它模拟"网关进程没了";将来做优雅退出也是这一步。
func (s *Services) InterruptRunningOSC(wait time.Duration) {
	ids := oscRunning.ids()
	for _, id := range ids {
		if m := oscRunning.get(id); m != nil {
			m.Interrupt()
		}
	}
	deadline := time.Now().Add(wait)
	for len(oscRunning.ids()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// ErrOSCBusy:那张表上已经有一个迁移在跑。
var ErrOSCBusy = errors.New("这张表上已经有一个在线变更在进行,请等它结束")

// NewOSCJob 记下一次要做的在线变更(状态 awaiting)。d 来自 OSCRoute,warnings 是前置检查的警告。
func (s *Services) NewOSCJob(u *model.User, conn *model.Connection, stmt string, d OSCDecision, warnings []osc.Warning) (*model.OSCJob, error) {
	chg, err := json.Marshal(d.Parsed.Change)
	if err != nil {
		return nil, err
	}
	warn := ""
	if len(warnings) > 0 {
		// 按 dto.OSCItem 存(小写 code / reason,与终端回执同形):直接存 osc.Warning 会是
		// 大写的 Code / Reason,界面读不到字,只剩一个孤零零的 ⚠
		b, _ := json.Marshal(warningItems(warnings))
		warn = string(b)
	}
	j := &model.OSCJob{
		ConnectionID: conn.ID, Instance: conn.Name, Database: d.Schema, Table: d.Table,
		Statement: stmt, ChangeJSON: string(chg), InitiatorID: u.ID, Initiator: u.Name,
		SizeRows: d.Rows, SizeBytes: d.Bytes, Warnings: warn, Status: model.OSCAwaiting,
	}
	return j, s.Repo.CreateOSCJob(j)
}

// OSCPreflight 对目标表跑一遍引擎的前置检查(采集事实 + Preflight)。
func (s *Services) OSCPreflight(ctx context.Context, conn *model.Connection, schema, table string, c osc.Change) ([]osc.Blocker, []osc.Warning, error) {
	db, _, err := gateway.OpenOSCTarget(conn)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := osc.Gather(gctx, db, schema, table)
	if err != nil {
		return nil, nil, apimsg.Errorf("采集表 %s.%s 的信息: %w", schema, table, err)
	}
	b, w := osc.Preflight(f, c)
	// 任意 ALTER:在空的探测表上真跑一遍,按变更后的结构查能不能拷贝 / 重放(工单 08)
	if len(b) == 0 {
		pb, err := osc.ProbeAlter(gctx, db, schema, table, c)
		if err != nil {
			return nil, nil, apimsg.Errorf("在探测表上试执行变更: %w", err)
		}
		b = append(b, pb...)
	}
	return b, append(w, s.oscReplicaWarnings(ctx, conn, db)...), nil
}

// oscReplicaLag 给出这台实例读从库延迟的办法。Aurora MySQL 的副本是存储层复制,在写节点(primary,
// 就是迁移用的那个连接池)上读 replica_host_status;其它实例读连接上配置的从库(没配 configured=false)。
// done 用完调用。
func oscReplicaLag(conn *model.Connection, primary *sql.DB) (read osc.ReplicaLagFunc, done func(), configured bool, err error) {
	if gateway.IsAurora(conn.Engine) {
		return osc.AuroraReplicaLagOf(primary), func() {}, true, nil
	}
	rdb, err := gateway.OpenOSCReplica(conn)
	if err != nil || rdb == nil {
		return nil, func() {}, false, err
	}
	return osc.ReplicaLagOf(rdb), func() { rdb.Close() }, true, nil
}

// defaultOSCMaxReplicaLagSec:从库延迟超过它就暂停拷贝与重放(设置 osc.maxReplicaLagSec,0 = 不看)。
const defaultOSCMaxReplicaLagSec = 10

// oscReplicaWarnings:开始前就让人知道"从库延迟这道保护在不在、现在读不读得到"。
// 都只是警告:没配从库照样能做,读不到的话迁移开始后会停在限流里,由人决定。
func (s *Services) oscReplicaWarnings(ctx context.Context, conn *model.Connection, primary *sql.DB) []osc.Warning {
	maxSec := s.settingInt("osc.maxReplicaLagSec", defaultOSCMaxReplicaLagSec)
	if maxSec <= 0 {
		return nil
	}
	read, done, configured, err := oscReplicaLag(conn, primary)
	if !configured && err == nil {
		return []osc.Warning{{Code: "replica_none", Reason: "实例未配置从库,不按复制延迟限流"}}
	}
	if err == nil {
		defer done()
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var lag time.Duration
		var running bool
		if lag, running, err = read(rctx); err == nil {
			if !running {
				return []osc.Warning{{Code: "replica_not_running", Reason: "从库复制未在运行,迁移开始后会停在限流里,直到复制恢复"}}
			}
			if lag > time.Duration(maxSec)*time.Second {
				return []osc.Warning{{Code: "replica_lagging", Reason: fmt.Sprintf("从库当前延迟 %d 秒,超过阈值 %d 秒,迁移开始后会先停在限流里", int(lag/time.Second), maxSec)}}
			}
			return nil
		}
	}
	return []osc.Warning{{Code: "replica_unreadable", Reason: "读不到从库复制延迟(" + err.Error() + "),迁移开始后会停在限流里;多半是网关账号在从库上缺 REPLICATION CLIENT 权限"}}
}

// StartOSCJob 占住任务并在后台开始迁移;onFinish 在结束(成功 / 失败 / 中止)后调用。
func (s *Services) StartOSCJob(jobID int64, onFinish func(*model.OSCJob, error)) error {
	j, err := s.Repo.GetOSCJob(jobID)
	if err != nil {
		return ErrNotFound
	}
	ok, err := s.Repo.ClaimOSCJobStart(j, time.Now())
	if err != nil {
		return err
	}
	if !ok {
		if j.Status != model.OSCAwaiting {
			return apimsg.Errorf("这个在线变更已经执行过了(%s)", j.Status)
		}
		return ErrOSCBusy
	}
	goSafe("osc.start", func() {
		err := s.runOSC(context.Background(), j, nil, nil)
		if errors.Is(err, osc.ErrInterrupted) {
			return // 被打断等续跑:没有结局可回写
		}
		final, _ := s.Repo.GetOSCJob(j.ID)
		if onFinish != nil && final != nil {
			onFinish(final, err)
		}
	})
	return nil
}

// goSafe 起一个后台 goroutine,panic 时记日志而不是带走整个网关进程(2026-10 安全审计 L-OSC4)。
// runOSC 自己有 recover,但它外面的回调(回写工单、通知)、心跳、启动收尾都在别的 goroutine 里。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background goroutine panicked", "name", name, "panic", r)
			}
		}()
		fn()
	}()
}

// RunOSCJobSync 占住任务并就地跑完(发布流水线用);onProgress 每次落库后调用。
func (s *Services) RunOSCJobSync(ctx context.Context, jobID int64, onProgress func(*model.OSCJob)) error {
	j, err := s.Repo.GetOSCJob(jobID)
	if err != nil {
		return ErrNotFound
	}
	ok, err := s.Repo.ClaimOSCJobStart(j, time.Now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrOSCBusy
	}
	return s.runOSC(ctx, j, onProgress, nil)
}

// runOSC 跑一次迁移(任务已经是 running)。所有结局都落库:done / failed / aborted。
// resume 非 nil 时从检查点续跑(工单 07)。
func (s *Services) runOSC(ctx context.Context, j *model.OSCJob, onProgress func(*model.OSCJob), resume *osc.Checkpoint) (err error) {
	finish := func(status string, m apimsg.M, oldTable string) {
		now := time.Now()
		_ = s.Repo.UpdateOSCJob(j.ID, map[string]any{"status": status, "error": m.S, "error_ref": apimsg.RefJSON(m.Ref), "old_table": oldTable,
			"finished_at": now, "paused": false, "throttled": false})
	}
	defer func() {
		if r := recover(); r != nil {
			err = apimsg.Errorf("在线变更异常退出: %v", r)
			finish(model.OSCFailed, apimsg.FromErr(err), "")
			slog.Error("osc job panicked", "job", j.ID, "panic", r)
		}
	}()

	var change osc.Change
	if err := json.Unmarshal([]byte(j.ChangeJSON), &change); err != nil {
		finish(model.OSCFailed, apimsg.Sprintf("变更内容损坏: %s", err), "")
		return err
	}
	conn, err := s.Repo.GetConnection(j.ConnectionID)
	if err != nil {
		finish(model.OSCFailed, apimsg.T("目标连接已不存在"), "")
		return err
	}
	db, src, err := gateway.OpenOSCTarget(conn)
	if err != nil {
		finish(model.OSCFailed, apimsg.Sprintf("连接目标库: %s", err), "")
		return err
	}
	defer db.Close()
	bl := osc.NewMySQLBinlog(src, j.Database, j.Table)
	defer bl.Close()

	cfg := osc.Config{
		ChunkSize:         s.settingInt("osc.chunkSize", 1000),
		MaxThreadsRunning: s.settingInt("osc.maxThreadsRunning", 50),
		Resume:            resume,
	}
	// 按从库延迟限流:配了从库才有;打不开从库也照样接上 —— 每次探测都报"读不到",
	// 迁移停在限流里等人处理(用户定的口径:延迟未知时不继续往从库灌写入)。
	if lagSec := s.settingInt("osc.maxReplicaLagSec", defaultOSCMaxReplicaLagSec); lagSec > 0 {
		read, done, configured, rerr := oscReplicaLag(conn, db)
		switch {
		case rerr != nil:
			cfg.ReplicaLag = func(context.Context) (time.Duration, bool, error) { return 0, false, rerr }
		case configured:
			defer done()
			cfg.ReplicaLag = read
		}
		cfg.MaxReplicaLag = time.Duration(lagSec) * time.Second
	}
	m := osc.New(db, bl, j.Database, j.Table, change, cfg)
	oscRunning.put(j.ID, m)
	defer oscRunning.drop(j.ID)

	persist := func() {
		st := m.Status()
		fields := map[string]any{
			"phase": string(st.Phase), "rows_estimated": st.RowsEstimated, "rows_copied": st.RowsCopied,
			"chunks": int(st.Chunks), "rows_replayed": st.RowsReplayed, "cut_over_attempts": st.CutOverAttempts,
			"throttled": st.Throttled != "", "throttle_reason": clip(st.Throttled, 250), "throttle_reason_ref": apimsg.RefJSON(st.ThrottledRef), "paused": st.Paused,
		}
		// 检查点与进度一起落:网关重启后从这里续跑(工单 07)
		if ck := m.Checkpoint(); ck != nil {
			if b, err := json.Marshal(ck); err == nil {
				fields["checkpoint"] = string(b)
			}
		}
		_ = s.Repo.UpdateOSCJob(j.ID, fields)
		if onProgress != nil {
			if cur, e := s.Repo.GetOSCJob(j.ID); e == nil {
				onProgress(cur)
			}
		}
	}
	stop := make(chan struct{})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		// persist 里有回调(发布流水线的进度):它 panic 不该带走进程(L-OSC4)。先 recover,
		// close(ticked) 照样执行,等它的 runOSC 不会卡住。
		defer func() {
			if r := recover(); r != nil {
				slog.Error("osc persist goroutine panicked", "job", j.ID, "panic", r)
			}
		}()
		t := time.NewTicker(oscPersistEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				persist()
			}
		}
	}()
	err = m.Run(ctx)
	close(stop)
	<-ticked
	persist()

	switch {
	case errors.Is(err, osc.ErrInterrupted):
		// 被打断(进程要退出):任务留在 running、检查点已随上面的 persist 落库,
		// 由下次启动的 ReconcileOSCJobs 判为可续跑 —— 和进程直接没了是同一个状态
	case err == nil:
		finish(model.OSCDone, apimsg.M{}, "_"+j.Table+"_del")
	case errors.Is(err, osc.ErrAborted) || errors.Is(context.Cause(ctx), osc.ErrAborted):
		finish(model.OSCAborted, apimsg.T("已中止,影子表已清理,原表未动"), "")
	default:
		finish(model.OSCFailed, apimsg.FromErr(err), "")
	}
	return err
}

// PauseOSCJob / ResumeOSCJob / AbortOSCJob 控制一个正在跑的迁移。只对本进程里在跑的有效。
func (s *Services) PauseOSCJob(id int64) error {
	return s.oscControl(id, func(m *osc.Migration) { m.Pause() }, true)
}
func (s *Services) ResumeOSCJob(id int64) error {
	return s.oscControl(id, func(m *osc.Migration) { m.Resume() }, false)
}
func (s *Services) AbortOSCJob(id int64) error {
	return s.oscControl(id, func(m *osc.Migration) { m.Abort() }, false)
}

func (s *Services) oscControl(id int64, f func(*osc.Migration), paused bool) error {
	m := oscRunning.get(id)
	if m == nil {
		return apimsg.Errorf("这个在线变更没有在运行")
	}
	f(m)
	return s.Repo.UpdateOSCJob(id, map[string]any{"paused": paused})
}

// ReconcileOSCJobs 是启动时的收尾(工单 07):
//   - 有检查点、来自终端 / 审批的 → interrupted,影子表保留,等人续跑或放弃。再到库上核对一次:
//     停在 cut-over 里、其实已经换过去了的(原表已是变更后的样子、旧表在、影子表不在)判完成;
//     影子表已经不在了的判失败;
//   - 其余判失败,并在后台尽力删掉残留的影子表。
//
// 判 interrupted 这一步是同步的:紧接着的 FailStuckApprovalExecutions 靠它把可续跑的工单留在"执行中"。
func (s *Services) ReconcileOSCJobs() {
	interrupted, failed, err := s.Repo.ReconcileStuckOSCJobs()
	if err != nil || len(interrupted)+len(failed) == 0 {
		return
	}
	slog.Warn("reconciled interrupted online schema changes on startup", "resumable", len(interrupted), "failed", len(failed))
	goSafe("osc.reconcile", func() {
		for _, j := range failed {
			if m := s.dropOSCGhost(j); m.S != "" {
				msg := apimsg.Sprintf("网关重启,迁移中断,原表未切换;%s", m)
				_ = s.Repo.UpdateOSCJob(j.ID, map[string]any{"error": msg.S, "error_ref": apimsg.RefJSON(msg.Ref)})
			}
		}
		for _, j := range interrupted {
			s.settleInterruptedOSC(j)
		}
	})
}

// settleInterruptedOSC 到库上看一个中断任务的真实状态:已切换完成的判完成,影子表没了的判失败,
// 其余保持 interrupted。连不上库时也保持 interrupted —— 续跑那一步还会再检查。
func (s *Services) settleInterruptedOSC(j model.OSCJob) {
	conn, err := s.Repo.GetConnection(j.ConnectionID)
	if err != nil {
		return
	}
	db, _, err := gateway.OpenOSCTarget(conn)
	if err != nil {
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var change osc.Change
	if json.Unmarshal([]byte(j.ChangeJSON), &change) != nil {
		return
	}
	exists := func(table string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?", j.Database, table).Scan(&n)
		return n > 0, err
	}
	ghost, gerr := exists("_" + j.Table + "_gho")
	old, oerr := exists("_" + j.Table + "_del")
	if gerr != nil || oerr != nil || ghost {
		return // 影子表还在:正常的中断,等人处理
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = ?",
		j.Database, j.Table, change.Index).Scan(&n); err != nil {
		return
	}
	now := time.Now()
	if old && (n > 0) == (change.Action == osc.ActionAddIndex) {
		// RENAME 在上一个进程退出前已经完成:原表已是变更后的样子
		_ = s.Repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCDone, "phase": string(osc.PhaseDone), "error": "",
			"old_table": "_" + j.Table + "_del", "finished_at": now})
	} else {
		_ = s.Repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCFailed, "finished_at": now,
			"error": apimsg.Text("网关重启,迁移中断;影子表已不在,无法续跑,原表未切换")})
	}
	s.finishOSCForApproval(j.ID)
}

// finishOSCForApproval 把任务的结局回写到它的审批单(若有),执行人记为发起人。
func (s *Services) finishOSCForApproval(jobID int64) {
	f, err := s.Repo.GetOSCJob(jobID)
	if err != nil || f.ApprovalID == 0 {
		return
	}
	ap, err := s.Repo.GetApproval(f.ApprovalID)
	if err != nil {
		return
	}
	conn, err := s.Repo.GetConnection(f.ConnectionID)
	if err != nil {
		return
	}
	actor, err := s.Repo.GetUserByID(ap.InitiatorID)
	if err != nil {
		return
	}
	// 续跑到收尾时已经没有原来那次请求了(进程可能重启过):这一行记在它所属的工单名下。
	actor.AuditSource = model.AuditSourceApproval
	s.finishApprovedOSC(actor, ap, conn, f)
}

// ContinueOSCJob 从检查点续跑一个中断的任务(工单 07)。前置检查、影子表结构、binlog 是否还在
// 都由引擎在开跑时再确认;任何一条不满足,任务判失败并清理影子表 —— 与正常执行出错同一个结局。
func (s *Services) ContinueOSCJob(actor *model.User, id int64) error {
	j, err := s.Repo.GetOSCJob(id)
	if err != nil {
		return ErrNotFound
	}
	if j.Status != model.OSCInterrupted {
		return apimsg.Errorf("只有已中断的在线变更可以续跑(当前 %s)", j.Status)
	}
	var ck osc.Checkpoint
	if err := json.Unmarshal([]byte(j.Checkpoint), &ck); err != nil {
		return apimsg.Errorf("检查点损坏,无法续跑: %w", err)
	}
	ok, err := s.Repo.ClaimOSCJobContinue(j)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOSCBusy
	}
	goSafe("osc.continue", func() {
		if err := s.runOSC(context.Background(), j, nil, &ck); errors.Is(err, osc.ErrInterrupted) {
			return
		}
		s.finishOSCForApproval(j.ID)
	})
	return nil
}

// AbandonOSCJob 放弃一个中断的任务:删影子表、判中止、工单记执行失败(与中止同一个结局)。
func (s *Services) AbandonOSCJob(id int64) error {
	j, err := s.Repo.GetOSCJob(id)
	if err != nil {
		return ErrNotFound
	}
	if j.Status != model.OSCInterrupted {
		return ErrOSCNotRunning
	}
	msg := apimsg.T("已放弃,原表未动")
	if m := s.dropOSCGhost(*j); m.S != "" {
		msg = apimsg.Sprintf("已放弃,原表未动;%s", m)
	}
	if err := s.Repo.UpdateOSCJob(id, map[string]any{"status": model.OSCAborted, "error": msg.S, "error_ref": apimsg.RefJSON(msg.Ref), "finished_at": time.Now()}); err != nil {
		return err
	}
	s.finishOSCForApproval(id)
	return nil
}

// dropOSCGhost 删掉一个中断任务留下的影子表,返回要补进任务说明的那句话(删掉了 / 删不掉)。
func (s *Services) dropOSCGhost(j model.OSCJob) apimsg.M {
	conn, err := s.Repo.GetConnection(j.ConnectionID)
	if err != nil {
		return apimsg.T("目标连接已不存在,影子表未清理")
	}
	db, _, err := gateway.OpenOSCTarget(conn)
	if err != nil {
		return apimsg.Sprintf("连不上目标库,影子表 _%s_gho 未清理,请 DBA 处理", j.Table)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ghost := "`" + strings.ReplaceAll(j.Database, "`", "``") + "`.`_" + strings.ReplaceAll(j.Table, "`", "``") + "_gho`"
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+ghost); err != nil {
		return apimsg.Sprintf("影子表 _%s_gho 删除失败(%s),请 DBA 处理", j.Table, err)
	}
	return apimsg.T("残留的影子表已删除")
}
