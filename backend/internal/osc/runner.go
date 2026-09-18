package osc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ConnectFunc 按实例 ID 拿到一个目标库连接和它的 DSN。
//
// DSN 单独要一份,是因为 binlog 订阅走的是复制协议而不是 database/sql,
// 它需要自己拆 host/port/user/pass。
type ConnectFunc func(connectionID int64) (*sql.DB, string, error)

// Runner 把五个阶段串起来,并把每一步写进库。
//
// 它要守的不是「跑得完」,而是**跑不完的时候留下的东西能被收拾**。ADR 0011 担心的
// 那个场景 —— 影子表和一段没追平的 binlog 留在库里,而人以为自己加了个索引 ——
// 正是这一层的职责。
type Runner struct {
	store   *gorm.DB
	connect ConnectFunc

	// ChunkSize 是分块拷贝一次搬多少行。0 = 用 CopyAll 的默认值。
	//
	// 它同时决定**中止的响应有多快**:取消信号在每块之间才被检查,块越大停得越迟。
	// 大表上该调小,而不是图一次少发几条 SQL。
	ChunkSize int

	mu      sync.Mutex
	running map[int64]context.CancelFunc // 正在跑的任务 → 它的取消钩子
}

func NewRunner(store *gorm.DB, connect ConnectFunc) *Runner {
	return &Runner{store: store, connect: connect, running: map[int64]context.CancelFunc{}}
}

// StartRequest 是发起一次迁移要给的东西。
type StartRequest struct {
	ConnectionID int64
	Schema       string
	Table        string
	Alter        string
	CreatedBy    string
}

// Start 建一条任务记录并在后台跑起来。
//
// **前置检查在这里就做,而且是同步做的** —— 被拒绝的变更根本不该留下一条任务记录,
// 更不该建出影子表。让人拿着一个 failed 任务去翻日志才知道"你的表有外键",
// 比当场告诉他多花三个变更窗口。
func (r *Runner) Start(ctx context.Context, req StartRequest) (*Job, error) {
	target, dsn, err := r.connect(req.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("连接目标实例: %w", err)
	}

	facts, err := Gather(ctx, target, req.Schema, req.Table)
	if err != nil {
		return nil, fmt.Errorf("采集实例信息: %w", err)
	}
	if bs, _ := Preflight(facts, ActionAddIndex); len(bs) > 0 {
		return nil, &PreflightError{Blockers: bs}
	}

	job := &Job{
		ConnectionID: req.ConnectionID, Schema: req.Schema, Table: req.Table,
		Alter: req.Alter, Status: JobPending, CreatedBy: req.CreatedBy,
		TotalRows: facts.EstimatedRows,
	}
	if err := r.store.Create(job).Error; err != nil {
		return nil, fmt.Errorf("落库: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.mu.Lock()
	r.running[job.ID] = cancel
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.running, job.ID)
			r.mu.Unlock()
			cancel()
		}()
		r.run(runCtx, job.ID, target, dsn)
	}()
	return job, nil
}

// PreflightError 带着全部阻塞项,好让调用方一次把它们交给人 —— 一条条试出来,
// 在生产上就是一个个变更窗口。
type PreflightError struct{ Blockers []Blocker }

func (e *PreflightError) Error() string {
	return fmt.Sprintf("前置检查不通过:%d 条阻塞项", len(e.Blockers))
}

// Abort 叫停一个正在跑的任务。清理由 run 自己在退出路径上做 —— 它才知道
// 影子表建到哪一步了。
func (r *Runner) Abort(_ context.Context, id int64) error {
	r.mu.Lock()
	cancel, ok := r.running[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务 %d 不在运行中", id)
	}
	cancel()
	return nil
}

// IsRunning 说的是**这个进程**此刻手上有没有这条任务。
//
// 它回答的是状态字段回答不了的问题:库里一条 copying 的记录,可能是正在跑,也可能是
// 上一个进程死在半路留下的 —— 两者的 status 一模一样。能分清的只有进程自己。
//
// 多副本共享一个库时,别的副本正在跑的任务在这里也是 false。所以这个值只能用来说
// 「我没在推进它」,不能用来说「没有人在推进它」—— 前者是事实,后者是猜测,而按猜测
// 去删一张影子表可能删掉另一台正在用的。
func (r *Runner) IsRunning(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[id]
	return ok
}

// Get 读一条任务。
func (r *Runner) Get(ctx context.Context, id int64) (*Job, error) {
	var j Job
	if err := r.store.WithContext(ctx).First(&j, id).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

// Unfinished 列出所有还没走到终点的任务 —— 重启之后靠它把残局挑出来。
func (r *Runner) Unfinished(ctx context.Context) ([]Job, error) {
	var out []Job
	err := r.store.WithContext(ctx).
		Where("status IN ?", []JobStatus{JobPending, JobPreflight, JobCopying, JobReplaying, JobCutOver}).
		Order("id DESC").Find(&out).Error
	return out, err
}

// Recent 按时间倒序列出最近的任务,包括已经结束的 —— 界面要靠它同时看到
// 「正在跑的」和「上次留下的残局」。
func (r *Runner) Recent(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	out := []Job{} // 不是 nil:它会被直接序列化成 JSON,空列表要是 [] 而不是 null
	err := r.store.WithContext(ctx).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// run 是那条链路本身。每一步先写状态再动手 —— 反过来的话,进程在动手与写状态之间
// 挂掉,库里留下的是上一步的状态,而实际已经做了下一步的事。
func (r *Runner) run(ctx context.Context, id int64, target *sql.DB, dsn string) {
	job, err := r.Get(ctx, id)
	if err != nil {
		return
	}

	fail := func(format string, a ...any) {
		r.finish(id, JobFailed, fmt.Sprintf(format, a...))
	}
	// 中止与失败走同一条清理路径:两者留下的残留是一样的。
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		r.cleanupShadow(target, job)
		r.finish(id, JobAborted, "已中止")
		return true
	}

	// ---- 前置检查(Start 已经做过一次,这里只推进状态) ----
	if !r.advance(id, JobPreflight) {
		return
	}
	if stopped() {
		return
	}

	// ---- 建影子表。名字一拿到就写库:失败之后要靠它找到残留 ----
	shadow, err := CreateShadow(ctx, target, job.Schema, job.Table, job.Alter)
	if err != nil {
		fail("建影子表:%v", err)
		return
	}
	r.store.Model(&Job{}).Where("id = ?", id).Update("shadow", shadow)
	job.Shadow = shadow

	// ---- 订阅 binlog,把变更期间的写入重放到影子表 ----
	pos, err := CurrentPosition(ctx, target)
	if err != nil {
		r.cleanupShadow(target, job)
		fail("读 binlog 位点:%v", err)
		return
	}
	replayCtx, stopReplay := context.WithCancel(ctx)
	defer stopReplay()
	go func() {
		_ = Stream(replayCtx, StreamConfig{
			DSN: dsn, ServerID: uint32(5000 + id%1000),
			Schema: job.Schema, Table: job.Table, Position: pos,
		}, func(ev RowEvent) error {
			ev.Table = shadow
			q, args := replayStatement(ev)
			if q == "" {
				return nil
			}
			_, err := target.ExecContext(context.WithoutCancel(replayCtx), q, args...)
			return err
		})
	}()

	// ---- 分块拷贝 ----
	if !r.advance(id, JobCopying) {
		return
	}
	res, err := CopyAll(ctx, target, job.Schema, job.Table, shadow, CopyOptions{
		ChunkSize: r.ChunkSize,
		OnProgress: func(p Progress) {
			r.store.Model(&Job{}).Where("id = ?", id).Update("copied_rows", p.Copied)
		},
	})
	r.store.Model(&Job{}).Where("id = ?", id).Update("copied_rows", res.Copied)
	if err != nil {
		if stopped() {
			return
		}
		r.cleanupShadow(target, job)
		fail("拷贝:%v", err)
		return
	}

	// ---- 等重放追平 ----
	if !r.advance(id, JobReplaying) {
		return
	}
	if err := r.waitCaughtUp(ctx, target, job.Schema, job.Table, shadow); err != nil {
		if stopped() {
			return
		}
		r.cleanupShadow(target, job)
		fail("等待追平:%v", err)
		return
	}

	// ---- 切换 ----
	if !r.advance(id, JobCutOver) {
		return
	}
	if stopped() {
		return
	}
	if err := CutOver(ctx, CutOverConfig{
		DSN: dsn, Schema: job.Schema, Table: job.Table, Shadow: shadow, ReplayCaughtUp: true,
	}); err != nil {
		r.cleanupShadow(target, job)
		fail("切换:%v", err)
		return
	}
	stopReplay()
	r.finish(id, JobDone, "")
}

// waitCaughtUp 等影子表的行数追上原表。
//
// 判据用行数而不是 binlog 位点:位点在还有写入时永远追不上,而这套东西本来就是
// 给「一边写一边迁」准备的。行数相等只是个近似 —— 真正的保证来自 cut-over 那一下
// 的原子改名,它之后不会再有写入落到旧表上。
func (r *Runner) waitCaughtUp(ctx context.Context, db *sql.DB, schema, table, shadow string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var a, b int64
		q := fmt.Sprintf("SELECT (SELECT COUNT(*) FROM %s), (SELECT COUNT(*) FROM %s)",
			quoteName(schema, table), quoteName(schema, shadow))
		if err := db.QueryRowContext(ctx, q).Scan(&a, &b); err != nil {
			return err
		}
		if a == b {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("两分钟内没有追平")
}

// advance 推进状态,并在跃迁非法时拒绝 —— 状态机那几条规则在这里落地。
func (r *Runner) advance(id int64, to JobStatus) bool {
	var j Job
	if err := r.store.First(&j, id).Error; err != nil {
		return false
	}
	if !canTransition(j.Status, to) {
		return false
	}
	return r.store.Model(&Job{}).Where("id = ?", id).
		Updates(map[string]any{"status": to, "updated_at": time.Now()}).Error == nil
}

func (r *Runner) finish(id int64, status JobStatus, errMsg string) {
	now := time.Now()
	r.store.Model(&Job{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "err": errMsg, "updated_at": now, "finished_at": now,
	})
}

// cleanupShadow 收走影子表。
//
// 中止和失败都走这里:两者留下的残留是一样的。不收的话,它占着磁盘,还会挡住下一次
// 迁移 —— 那不叫中止,叫放弃。
func (r *Runner) cleanupShadow(db *sql.DB, job *Job) {
	if job.Shadow == "" {
		return
	}
	// 用不带取消的 ctx:正是因为被取消了才走到这里。
	_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quoteName(job.Schema, job.Shadow))
}
