package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/osc"
)

// 命中阈值的索引变更要改走 OSC,而这件事必须**在阶段日志里说出来** ——
// 一次"本该走 OSC 却没走"看不见的话,这个功能哪天失效了不会有任何迹象。

func TestStageExecute_HandsABigTableIndexChangeToOSC(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000 // 假的行数来源,替掉真的 information_schema 查询
	fx.osc.startID = 17

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunWaiting {
		t.Fatalf("状态 = %s,期望 waiting(在等那个迁移跑完)。日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 0 {
		t.Error("语句被直接下发了 —— 它本该交给 OSC")
	}
	if got := fx.reloadStage(); got.OSCJobID != 17 {
		t.Errorf("阶段挂着的任务是 %d,期望 17", got.OSCJobID)
	}
	if !strings.Contains(out.log, "OSC") {
		t.Errorf("日志里没说这一条走了 OSC:\n%s", out.log)
	}
}

func TestStageExecute_SmallTableGoesStraightThrough(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 1_000

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s,日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 1 {
		t.Errorf("下发了 %d 条,期望 1 条(小表直发)", fx.exec.count())
	}
	// "小表直发"和"路由被整个短路(routeStatement 恒答直发)"在上面两条断言下
	// 长得一模一样 —— 都是状态 success、下发 1 条。这里钉住 rowsOfTable 真的被
	// 读过、Decide 真的按行数判过:日志里要能看到这次判定用的具体行数和阈值。
	if !strings.Contains(out.log, "未超过阈值") {
		t.Errorf("日志里没有行数判定的痕迹 —— 路由是不是被短路了?\n%s", out.log)
	}
}

func TestStageExecute_FallsBackToDirectExecutionWhenOSCIsOff(t *testing.T) {
	// 决定 2:该走却走不了时直发,并把原因说出来。原生加索引本来就是在线的,
	// "没走 OSC"是失去了限流/从库友好/MDL 可重试这三件事,不是干了一件危险的事。
	// 让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startErr = errOSCDisabled // 发起被拒

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s —— OSC 用不了不该让整张单失败。日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 1 {
		t.Error("没有回落到直发")
	}
	if !strings.Contains(out.log, "直发") {
		t.Errorf("日志里没说清这一条为什么没走 OSC:\n%s", out.log)
	}
}

func TestStageExecute_DoesNotAnnotatePlainDML(t *testing.T) {
	// 一条 UPDATE 旁边写"不是索引变更"没有信息量,而阶段日志有 20000 字的上限。
	// 真正要占这点额度的是"走了 OSC"和"本该走却没走"。
	fx := newExecFixture(t, "UPDATE t SET a=1")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if strings.Contains(out.log, "不是一条纯粹的索引变更") {
		t.Errorf("给一条普通 DML 写了判定说明:\n%s", out.log)
	}
	// 执行结果那一行还在。
	if !strings.Contains(out.log, "[1/1]") {
		t.Errorf("执行结果那一行丢了:\n%s", out.log)
	}
}

func TestStageExecute_RespectsTheKillSwitch(t *testing.T) {
	// 急停开关要挡住的是"发起",而流水线是发起的另一条路。只拦住控制台那道门,
	// 等于让扳动开关的人以为自己挡住了,而任务还在一个个地起来。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.oscEnabled = false // 运维刚扳下急停

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s —— 急停不该让发布单失败,它该回落直发。日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 1 {
		t.Error("没有回落到直发")
	}
	if got := fx.reloadStage(); got.OSCJobID != 0 {
		t.Errorf("急停开关关着,却仍然发起了迁移任务 #%d", got.OSCJobID)
	}
	if !strings.Contains(out.log, "关闭") {
		t.Errorf("日志里没说清为什么没走 OSC:\n%s", out.log)
	}
}

func TestStageExecute_StopsAtTheFirstOSCStatementAndLeavesTheRestAlone(t *testing.T) {
	// 逐条串行:命中的那条把阶段挂起,**后面的语句一条都不能先跑** ——
	// 顺序是发起人写下的,乱序执行的后果由数据承担。
	fx := newExecFixture(t, "UPDATE t SET a=1; ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 21

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunWaiting {
		t.Fatalf("状态 = %s,期望 waiting", out.status)
	}
	if n := fx.exec.count(); n != 1 {
		t.Errorf("下发了 %d 条,期望只有第一条 —— 第三条抢在迁移前面跑了", n)
	}
	if got := fx.reloadStage(); got.ExecCursor != 1 {
		t.Errorf("游标是 %d,期望 1(第一条已完成,第二条正在 OSC 里跑)", got.ExecCursor)
	}
}

func TestOnOSCJobFinished_MarksTheOSCStatementDoneSynchronously(t *testing.T) {
	// 这条只钉 OnOSCJobFinished 自己在返回之前同步写下的那部分,不依赖
	// resumeReleaseAsync 那个 goroutine 有没有跑完 —— 后半段("续跑第二条")
	// 是另一条用例的事,见 TestOnOSCJobFinished_ResumesTheReleaseAfterASuccessfulMigration。
	//
	// 拆成两条是因为续跑被挪到了异步的 goroutine 里(见 pipeline_osc.go 的
	// resumeReleaseAsync 注释:同步调用会让 IsRunning/Abort 在回调阻塞期间对一个
	// 已经结束的任务说谎),这条要断言的状态在 OnOSCJobFinished 返回的那一刻就已经
	// 落地,不该被下一条用例里"等 driveRelease 跑完"的轮询拖着一起变得不确定。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 33
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage) // 第一条挂起在任务 33 上

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 33, Status: osc.JobDone})

	got := fx.reloadStage()
	if got.OSCJobID != 0 {
		t.Errorf("任务结束了,阶段还挂着 %d", got.OSCJobID)
	}
	if got.ExecCursor != 1 {
		t.Errorf("游标是 %d,期望 1 —— 走 OSC 的那条要立刻算完成,不用等 driveRelease 续跑", got.ExecCursor)
	}
	if !strings.Contains(got.Log, "33") {
		t.Errorf("日志里没有点名完成的那个任务:\n%s", got.Log)
	}
}

func TestOnOSCJobFinished_ResumesTheReleaseAfterASuccessfulMigration(t *testing.T) {
	// 续跑发生在 resumeReleaseAsync 起的另一条 goroutine 上(同步调 driveRelease
	// 会让这个回调阻塞到剩下的阶段全部跑完 —— 见 pipeline_osc.go 的注释),所以这里
	// 只能等,用轮询直到超时,而不是假设它在某个固定的睡眠之后一定跑完。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 31
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage) // 第一条挂起在任务 31 上

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 31, Status: osc.JobDone})

	// 两条都完成了:走 OSC 的那条由回调算完成,第二条由 driveRelease 异步续跑。
	got := fx.waitForCursor(2, 2*time.Second)
	if got.OSCJobID != 0 {
		t.Errorf("任务结束了,阶段还挂着 %d", got.OSCJobID)
	}
	// **下发的必须是第二条,不是第一条。** 这才是"走 OSC 的那条被算作完成"的证据:
	// 游标没推上去的话,driveRelease 会把那条 ALTER 再执行一遍 —— 而它已经由
	// OSC 做完了,重跑会撞上一个已经存在的索引。
	if fx.exec.count() != 1 {
		t.Fatalf("下发了 %d 条,期望 1 条(只有第二条)", fx.exec.count())
	}
	if last := fx.exec.last(); !strings.Contains(last, "b=2") {
		t.Errorf("下发的是 %q,期望第二条 —— 走 OSC 的那条被重跑了", last)
	}
}

func TestOnOSCJobFinished_FailsTheStageWhenTheMigrationFailed(t *testing.T) {
	// 迁移失败不能当作"这一条做完了"往下走:那条索引根本没加上,而后面的语句
	// 可能正依赖它。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 32
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 32, Status: osc.JobFailed, Err: "拷贝:连接中断"})

	got := fx.reloadStage()
	if got.Status != model.RunFailed {
		t.Errorf("迁移失败了,阶段状态却是 %s", got.Status)
	}
	if !strings.Contains(got.Log, "32") {
		t.Errorf("日志里没有点名那个任务:\n%s", got.Log)
	}
	if fx.exec.count() != 0 {
		t.Error("迁移失败之后,后面的语句仍然被执行了")
	}
}

func TestOnOSCJobFinished_AFailedMigrationDoesNotEndUpAsASuccessfulRelease(t *testing.T) {
	// driveRelease 的循环把已经是 failed 的阶段当成"处理过了"跳过,然后落到
	// finishRelease(success)。所以失败分支不能无条件交回 driveRelease ——
	// 那样迁移失败的发布单最后会显示成功,而那条索引根本没加上。
	//
	// 比"永远等下去"更糟:等着的单子看得见,说成功的单子没人再去看。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 51
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 51, Status: osc.JobFailed, Err: "拷贝:连接中断"})

	if got := fx.reloadRelease(); got.Status != model.RunFailed {
		t.Errorf("迁移失败了,发布单状态却是 %s", got.Status)
	}
}

func TestOnOSCJobFinished_IgnoresAJobNobodyIsWaitingOn(t *testing.T) {
	// 绝大多数任务是从 OSC 控制台手工发起的,不属于任何发布单。反查不到不是错误。
	fx := newExecFixture(t, "UPDATE t SET a=1")

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 999, Status: osc.JobDone}) // 不该 panic

	if fx.exec.count() != 0 {
		t.Error("一个与发布单无关的任务推进了某张单")
	}
}

// errNotRunningHere 模拟"这台网关叫不停它" —— ADR 0011:Abort 只叫得停本进程
// 手上的任务,多副本部署下另一台副本跑着的那个任务,本进程的 Abort 天然够不着。
var errNotRunningHere = errors.New("osc: 这个任务不在本进程手上(模拟)")

func TestAbortRelease_AlsoStopsTheMigrationItIsWaitingOn(t *testing.T) {
	// 否则单子停了、迁移还在拷全表 —— 而人以为自己已经把它按停了。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 41
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	fx.markReleaseWaiting()

	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	if !fx.osc.aborted(41) {
		t.Error("发布单停了,它挂着的迁移任务还在跑")
	}
	if got := fx.reloadRelease(); !strings.Contains(got.Error, "41") {
		t.Errorf("终止原因里没提那个被叫停的任务:%q", got.Error)
	}
}

func TestAbortRelease_StillAbortsWhenTheMigrationCannotBeStopped(t *testing.T) {
	// Abort 只叫得停**本进程**手上的任务(ADR 0011)。多副本下另一台副本跑着的
	// 那个停不掉 —— 此时发布单照常中止,那个任务留成残局被列出来。
	// 谎称已经停掉它,比留着它更糟。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 42
	fx.osc.abortErr = errNotRunningHere
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	fx.markReleaseWaiting()

	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("迁移停不掉不该让终止本身失败: %v", err)
	}
	if got := fx.reloadRelease(); got.Status != model.RunAborted {
		t.Errorf("发布单状态 = %s,期望 aborted", got.Status)
	}
	// 确认确实调用过 Abort —— 而不是整个 OSC 集成根本没被接上(那样发布单
	// 照样能正常中止,断言 Status == aborted 分辨不出这两种情况)。
	if !fx.osc.abortAttempted(42) {
		t.Error("没有尝试叫停那个任务")
	}
	if got := fx.reloadRelease(); !strings.Contains(got.Error, "在线变更页") {
		t.Errorf("叫不停的时候没告诉人去哪儿收拾:%q", got.Error)
	}
}
