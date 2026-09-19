package service

// 发布流水线与 OSC 的接缝。
//
// 单独一个文件,因为 pipeline.go 已经很大,而这里的每一段都只为一件事服务:
// 让执行阶段能把一条语句交给一个要跑几小时的外部任务,然后在它结束时接着往下走。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/internal/oscroute"
)

// AttachOSC 把在线变更接进来。不接的话自动路由整个不存在,执行阶段照旧直发。
//
// enabled 是**和控制台那道闸同一个闭包** —— 急停开关(ADR 0011)要挡住的是"发起",
// 而流水线是发起的另一条路。各写一份判断的话,两条路迟早会分叉,而分叉的那一刻
// 没有人会发现:界面上写着"已挡住",任务却还在一个个地起来。
func (s *Services) AttachOSC(r *osc.Runner, connect osc.ConnectFunc, enabled func() bool) {
	s.osc = r
	s.oscEnabled = enabled
	s.tableRowsFn = func(conn *model.Connection, schema, table string) int64 {
		return gatherRows(connect, conn.ID, schema, table)
	}
}

// oscPolicy 读平台对自动路由的立场。
//
// 默认开着是安全的:OSC 的总开关默认关着,所以在没打开 OSC 的部署上它只会走到
// "直发并说明"那条路 —— 不改变任何现有行为,只多一行日志。
func (s *Services) oscPolicy() oscroute.Policy {
	return oscroute.Policy{
		AutoRoute: s.Repo.SettingBool("osc.autoRoute.enabled", true),
		MinRows:   int64(s.Repo.SettingInt("osc.autoRoute.minRows", 2_000_000)),
	}
}

// tableRows 是这张表的**估算**行数。
//
// 走 information_schema(osc.Gather 读的就是它),不做 COUNT(*):八百万行上要跑
// 几十秒,而它换来的精度在这里没有价值 —— 边界上误判的后果是"走了/没走 OSC",
// 两边都不危险。
//
// 拿不到就返回 0,也就是"当它是小表" —— 与 Preflight 对"拿不到磁盘余量不拦"
// 相反的方向,但同一个立场:未知的时候选那个不会把事情搞砸的答案。这里直发是
// 安全的那一边。
func gatherRows(connect osc.ConnectFunc, connID int64, schema, table string) int64 {
	db, _, err := connect(connID)
	if err != nil {
		return 0
	}
	facts, err := osc.Gather(context.Background(), db, schema, table)
	if err != nil {
		return 0
	}
	return facts.EstimatedRows
}

// routeStatement 决定一条语句的去向,并在决定走 OSC 时把任务发起出来。
//
// 返回的 jobID 为 0 表示"这条语句直发" —— **包括决定走 OSC 但发起失败的情况**。
// 那是决定 2:该走却走不了时直发,并把原因说出来。原生加索引本来就是在线的,
// "没走 OSC"是失去了限流/从库友好/MDL 可重试这三件事,不是干了一件危险的事;
// 让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立。
func (s *Services) routeStatement(rel *model.Release, conn *model.Connection, sql string) (jobID int64, note string) {
	if s.osc == nil {
		// 只有索引 DDL 才值得解释"为什么没走 OSC"。一条 UPDATE 旁边写"本部署没有
		// 接入 OSC"是噪音,而阶段日志有 20000 字的上限 —— 一次跨几小时、分几段
		// 恢复的执行,这点额度要留给真正说明了什么的行。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, "直发:本部署没有接入 OSC"
	}
	if s.oscEnabled == nil || !s.oscEnabled() {
		// 急停开关(配置里的 osc.enabled 前提 + tbl_setting 里的 osc.enabled 急停)
		// 关着,和控制台那道闸走的是同一个闭包 —— 见 AttachOSC。同上,只有索引
		// DDL 才值得解释,普通 DML 不写这一行。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, "直发:在线变更已被关闭(配置或急停开关)"
	}
	// 行数是懒查的:Decide 只在认出这是索引 DDL、且没被覆盖或策略拦下时才回调它。
	d := oscroute.Decide(sql, conn.Engine, rel.Database, s.oscPolicy(), oscroute.Override(rel.OSCMode),
		func(table string) int64 {
			if s.tableRowsFn == nil {
				return 0
			}
			return s.tableRowsFn(conn, rel.Database, table)
		})
	if !d.UseOSC {
		// 同上:只有索引 DDL 才值得解释"为什么没走 OSC"。一条 UPDATE 旁边写
		// "不是索引变更"是噪音——它本来就不该走 OSC,这不是新闻。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, d.Reason
	}
	job, err := s.osc.Start(context.Background(), osc.StartRequest{
		ConnectionID: conn.ID, Schema: rel.Database, Table: d.Table,
		Alter: d.Alter, CreatedBy: rel.Creator,
	})
	if err != nil {
		return 0, fmt.Sprintf("直发:本该走 OSC(%s),但发起失败 —— %v", d.Reason, err)
	}
	return job.ID, fmt.Sprintf("%s · 任务 #%d", d.Reason, job.ID)
}

// OnOSCJobFinished 是挂给 Runner.OnFinish 的那个回调(见 bootstrap/router.go 的接线)。
//
// 它只做一件事:把等着这个任务的那个阶段推下去。反查不到就直接返回 —— 绝大多数
// 迁移是从 OSC 控制台手工发起的,不属于任何发布单,这是正常情况而不是错误。
// jobID <= 0 同样直接返回:0 是 osc_job_id 列的默认值,绝大多数阶段都是 0,
// 按它反查会命中"随便一个没在等待的阶段",而不是"没人在等"。
//
// Runner 在自己的退出路径上**同步**调它(见 osc.Runner.OnFinish 的注释),它的
// defer(把任务从 running 表里摘掉、cancel())要等这里返回才轮到。这里只做
// 几次 map 形式的 Updates 加一次按主键的读取,快;但**推进发布单不能算在
// 这笔账里** —— 见下面 driveRelease 那一行的注释。
func (s *Services) OnOSCJobFinished(j *osc.Job) {
	if j == nil || j.ID <= 0 {
		return
	}
	st, err := s.Repo.StageWaitingOnOSCJob(j.ID)
	if err != nil || st == nil {
		return
	}
	if j.Status != osc.JobDone {
		s.failStageForFinishedOSCJob(st, j)
		return
	}
	// 走 OSC 的那一条到此算执行完毕,游标往前推一格,交回给 driveRelease 重新认领。
	_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
		"osc_job_id":  0,
		"exec_cursor": st.ExecCursor + 1,
		"status":      model.RunPending,
		"log":         st.Log + fmt.Sprintf("· 迁移任务 #%d 完成\n", j.ID),
	})
	s.resumeReleaseAsync(st.ReleaseID)
}

// failStageForFinishedOSCJob 处理迁移失败/被中止的那一条路。
//
// 那条索引根本没加上,不能当作"这一条做完了"往下走 —— 后面的语句可能正依赖它,
// 所以阶段判 failed,日志里点名是哪个任务。
//
// 只把阶段写成 failed 是不够的:driveRelease 重新进入时,对一个**已经**是 failed
// 的阶段只会 continue 过去(它假定这个状态是 onFailure=continue 的产物 —— 见
// driveRelease 同一段注释),不会替我们把"这次失败其实该终止整张单"这件事想清楚。
// 默认的 onFailure=abort 下,这里要像 driveRelease 处理阶段失败时那样直接收尾
// 发布单;只有显式配置成 onFailure=continue 时才把它交回 driveRelease 继续跑
// 后面的阶段。
func (s *Services) failStageForFinishedOSCJob(st *model.ReleaseStage, j *osc.Job) {
	note := fmt.Sprintf("· 迁移任务 #%d %s:%s\n", j.ID, j.Status, j.Err)
	_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
		"status":      model.RunFailed,
		"osc_job_id":  0,
		"log":         st.Log + note,
		"finished_at": time.Now(),
	})
	if st.OnFailure == model.OnFailureContinue {
		s.resumeReleaseAsync(st.ReleaseID)
		return
	}
	// 收尾是终结动作,不会再往下级联到别的阶段,同步做就够了 —— 摘要要和
	// driveRelease 自己遇到阶段失败时给的同等信息量:st.Log 是这个阶段暂停前
	// 已经写下的全部历史,note 是这一次新追加的一行,只传 note 会让人看到一句
	// 没头没尾的失败摘要。
	rel, err := s.Repo.GetRelease(st.ReleaseID)
	if err != nil {
		return
	}
	s.finishRelease(rel, model.RunFailed, fmt.Sprintf("阶段「%s」失败: %s", st.Name, clip(st.Log+note, 300)))
}

// abortOSCOfRelease 叫停这张单挂着的迁移,返回写进终止原因的一句话。
//
// **停不掉不是错误。** Abort 只叫得停本进程手上的任务(ADR 0011:IsRunning 说的是
// "这台网关没在推进它",不是"没有人在推进它")。多副本下另一台副本跑着的那个停不掉,
// 此时发布单照常中止,那个任务留成残局被列出来 —— 谎称已经停掉它比留着它更糟:
// 人会以为事情了结了,而那个迁移还在生产库上拷全表。所以叫停失败时要把这件事
// 写进终止原因,让人知道去哪儿收拾。
//
// **遍历全部挂着任务的阶段,不是找到第一个就 return。** "一张单同时只有一个阶段
// 挂着任务"是个隐含假设(正常路径下 OnOSCJobFinished 推进游标的同一次写里会把
// osc_job_id 清零),但这里没有任何东西强制它,数据库那个索引也不是 unique。假设
// 一旦被打破(比如更早的一个阶段因为某次异常没清零、恰好排在前面),只处理第一个
// 会让真正在跑的那个既没被叫停、也不会在终止原因里被提及 —— 比不处理更糟的是,
// 那次过期的 Abort 若碰巧成功,终止原因还会写"已连带叫停"这种谎话。
//
// 匹配条件只看 OSCJobID != 0 ——"这个阶段挂着任务",不看阶段状态。这不是在
// 判断"哪个阶段还活跃",纯粹是在收集"哪些任务号还挂着没清"这件事本身。
func (s *Services) abortOSCOfRelease(id int64) string {
	if s.osc == nil {
		return ""
	}
	var notes []string
	failed := 0
	for _, st := range s.stagesOf(id) {
		if st.OSCJobID == 0 {
			continue
		}
		if err := s.osc.Abort(context.Background(), st.OSCJobID); err != nil {
			failed++
			// Release.Error 是 varchar(512),真实的 OSC 错误可能很长(同文件
			// failStageForFinishedOSCJob 已经在用同一个 clip)。
			notes = append(notes, fmt.Sprintf("迁移任务 #%d 未能叫停(%s)", st.OSCJobID, clip(err.Error(), 200)))
			continue
		}
		notes = append(notes, fmt.Sprintf("已连带叫停迁移任务 #%d", st.OSCJobID))
	}
	if len(notes) == 0 {
		return ""
	}
	msg := ";" + strings.Join(notes, ";")
	switch {
	case len(notes) > 1:
		// 挂着不止一个任务本身就是不该发生的事,值得单独提示核实 —— 不管
		// 这几个各自叫停成功还是失败。
		msg += ";请到在线变更页确认这几个任务的残留"
	case failed > 0:
		msg += ";请到在线变更页确认它的残留"
	}
	return msg
}

// resumeReleaseAsync 把"接着跑这张发布单"扔到另一条 goroutine 上。
//
// **不能在 OnOSCJobFinished 里同步调 driveRelease**:它会把这张单剩下的全部阶段
// 跑一遍 —— 剩下的语句若没到 OSC 阈值,走 Executor.Run(超时默认 asyncExecTimeout
// 的 5400 秒);若还有一条命中阈值,routeStatement 会再同步做一次连接、Gather、
// Preflight。而这个回调正挂在迁移的退出路径上(osc.Runner.finish 同步调用它,
// run() 的 defer —— 把任务从 running 表里摘掉、cancel() —— 要等它返回才轮到)。
// 同步调用的话,回调阻塞多久,IsRunning(jobID) 和 Abort(jobID) 就对一个已经在库里
// 写成终态的任务说多久的谎:运维那段时间看不出它已经结束,Abort 也叫不停一个
// 早就跑完的东西。这正是 osc.Runner.OnFinish 字段注释警告过的情况,而把它接上的
// 正是这次的 wiring,所以必须在这里断开。
//
// 用 guardRelease 包一层(而不是裸 `go s.driveRelease(...)`):driveRelease 本身
// 没有 panic 恢复,裸起一个 goroutine 的话,里面一次 panic 会直接打倒整个进程。
// dispatchReleaseJob 恢复运行也是同一层包装,这里沿用而不是另起一套。
//
// driveRelease 本来就是可重入的(审批回调、超时巡检都在调它),异步跑它不引入
// 新的竞争;进程中途退出时未跑完的那次会丢,而那与"网关重启后发布单停在中间态"
// 是同一种残局,由残局机制负责被看见。
func (s *Services) resumeReleaseAsync(releaseID int64) {
	go s.guardRelease(releaseID, func() { s.driveRelease(releaseID) })
}
