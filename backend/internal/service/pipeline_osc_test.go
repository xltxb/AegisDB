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
	// C2 修复之后,resumeReleaseAsync 复用 continueRelease,它只认领 waiting 的单。
	// driveRelease 真实场景里在阶段进入 waiting 的同时会把发布单也标成 waiting
	// (见 pipeline.go 的那一行 UpdateRelease),这里手动补上,不然这条用例测的是
	// 一个生产里不会出现的状态组合(阶段 waiting、发布单却还是 running)。
	fx.markReleaseWaiting()

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

// C2:所有"把停着的单子重新驱动起来"的入口都先认领(waiting → running)——
// continueRelease、ContinueManualStage、审批回调、ResumeReleaseApprovals 概莫能外。
// 只有这条路径原来是裸 `go driveRelease`,从不认领:迁移结束后剩下的语句是在
// status=waiting 的发布单上执行的——这个窗口里 AbortRelease 会把它当成"可以安全
// 终止的 waiting"接受掉,而它其实正在执行语句;FailStuckReleases 只对账 running,
// 网关这时候挂掉,这张单永远停在 waiting、没有任何对账逻辑看得见它。
func TestOnOSCJobFinished_ClaimsTheReleaseRunningBeforeResuming(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 61
	// 第二条语句(UPDATE)执行时先卡住,撑开一个观察窗口:continuation 的
	// goroutine 已经真的跑到了 stageExecute,但还没跑完。
	fx.exec.gate = make(chan struct{})
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage) // 第一条挂起在任务 61 上
	fx.markReleaseWaiting()                        // driveRelease 真实会做的事:整单标成 waiting

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 61, Status: osc.JobDone})

	fx.exec.waitUntilBlocked(t, 2*time.Second)
	if got := fx.reloadRelease(); got.Status != model.RunRunning {
		t.Errorf("续跑正卡在第二条语句上,发布单状态却是 %s,期望 running(已被认领)", got.Status)
	}
	close(fx.exec.gate)
	// 放行之后等它真的跑完,再让测试返回 —— 不然 continuation 的 goroutine 会在
	// testsupport 关掉数据库连接之后才收尾,吵着一堆无关的"database is closed"。
	fx.waitForCursor(2, 2*time.Second)
}

// I3:minRows 没有服务端下限。设置接口是自由 key/value、没有校验 —— 下限只活在
// 前端的 clampInt(10000, 1e9)里。API 直调把它写成 0(或任何小得离谱的数)之后,
// 判据 `rows <= p.MinRows` 对 MinRows=0 意味着"每一条落在非空 MySQL 表上的索引
// DDL 都会起一次 OSC"。oscPolicy 必须自己兜住这个下限,不能信任存进来的值。
func TestOscPolicy_FloorsAnAbsurdlyLowMinRowsSetting(t *testing.T) {
	fx := newExecFixture(t, "UPDATE t SET a=1") // 语句内容与这条用例无关
	if err := fx.repo.SetSetting("osc.autoRoute.minRows", "0"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}

	p := fx.svc.oscPolicy()

	if p.MinRows < 10_000 {
		t.Errorf("MinRows = %d,一个 API 直调写进来的 0 未经兜底就被当真了", p.MinRows)
	}
}

func TestOscPolicy_DoesNotFloorASmallButPositiveMinRowsSetting(t *testing.T) {
	// 下限只挡 ≤0,不挡"很小但为正"——那依然是一次说得清楚的明确选择(比如自建
	// MySQL 上多数表都不大),不该被悄悄改写。TestReleaseE2E_TheIndexChangeIsMade
	// ByOSCAndTheReleaseCompletes 就依赖这一点:它把 minRows 设成 100,配一张只有
	// 3000 行的夹具表来触发路由,不必为了测试专门造一张几百万行的大表。
	fx := newExecFixture(t, "UPDATE t SET a=1")
	if err := fx.repo.SetSetting("osc.autoRoute.minRows", "100"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}

	p := fx.svc.oscPolicy()

	if p.MinRows != 100 {
		t.Errorf("MinRows = %d,期望 100 —— 一个很小但合理的配置被下限兜底误伤了", p.MinRows)
	}
}

func TestOscPolicy_KeepsALegitimateMinRowsSetting(t *testing.T) {
	// 下限兜底不能误伤一个合理的、只是比默认值小的配置(比如运维想把阈值调低到
	// 五十万行)——那是明确的意图,不是"看起来没配过"。
	fx := newExecFixture(t, "UPDATE t SET a=1")
	if err := fx.repo.SetSetting("osc.autoRoute.minRows", "500000"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}

	p := fx.svc.oscPolicy()

	if p.MinRows != 500_000 {
		t.Errorf("MinRows = %d,期望 500000(合理配置被下限兜底误伤了)", p.MinRows)
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

// C1:"两种 waiting"这道闸不能只长在前端。挂着迁移任务的执行阶段和"等人点确认
// 执行"共用同一个 status(见 ADR 0011「界面:两种 waiting 长得一模一样」),判据是
// OSCJobID —— 后端如果不查它,一次不需要恶意的序列就能让同一条语句被 OSC 起
// 第二次任务,还会把"走 OSC · 任务 #N"那行日志整个覆盖掉(见 M1/log 覆盖)。
func TestConfirmExecuteStage_RefusesAStageThatIsWaitingOnAMigration(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 17
	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	// stageExecute 只负责算,不负责落库(那是 driveRelease 的活)—— 这里手动做
	// driveRelease 收尾时同样的那一步,把 status/log 落回阶段行,这样
	// ConfirmExecuteStage 从库里读到的才是"挂着任务的 waiting"。
	if err := fx.repo.UpdateReleaseStage(fx.stage.ID, map[string]any{
		"status": out.status, "log": out.log,
	}); err != nil {
		t.Fatalf("写阶段失败: %v", err)
	}

	err := fx.svc.ConfirmExecuteStage(fx.user, fx.rel.ID, fx.stage.ID)

	if err == nil {
		t.Fatal("对一个挂着迁移任务的阶段点了「确认执行」,居然成功了")
	}
	if !strings.Contains(err.Error(), "17") {
		t.Errorf("错误信息 %q 没有点名是哪个任务在挡着", err.Error())
	}
	got := fx.reloadStage()
	if got.Status != model.RunWaiting {
		t.Errorf("阶段状态变成了 %s,期望仍然是 waiting(还在等任务 #17)", got.Status)
	}
	if !strings.Contains(got.Log, "17") {
		t.Errorf("日志被覆盖掉了,不再提任务 #17:\n%s", got.Log)
	}
	if fx.exec.count() != 0 {
		t.Error("语句被第二次发起执行了")
	}
	if len(fx.osc.started) != 1 {
		t.Errorf("OSC 任务被发起了 %d 次,期望只有 1 次(挂起时那一次)", len(fx.osc.started))
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

// C3:AbortRelease 的顺序是 认领成 aborted → abortOSCOfRelease 调 osc.Abort →
// 写 error("已由 X 终止;已连带叫停迁移任务 #N")。而 Abort 就是 cancel(),run()
// 的退出路径会**同步**走到 finish(JobAborted) → OnFinish → OnOSCJobFinished ——
// 这条回调原来对"发布单是不是已经有人处理过了"一无所知:AbortRelease 从不清
// osc_job_id,所以反查照样能查到这个阶段;它把刚被 skipUnrunStages 标成 skipped
// 的阶段改成 failed,再无条件 finishRelease(failed),把一张已经 aborted 的单
// 覆盖成 failed —— 是谁按停的、以及"叫不停请到在线变更页确认残留"那句提示,
// 一起被冲掉。
func TestOnOSCJobFinished_DoesNotUndoAReleaseThatIsAlreadyAborted(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 71
	// 用钩子还原真实的同步回调链:AbortRelease 调 osc.Abort 的那一刻,
	// OnOSCJobFinished 就在同一条调用栈上被打进来 —— 而不是测试摆一个
	// 事后才发生的、和真实时序对不上的顺序。
	fx.osc.onAbortFinish = func(id int64) {
		fx.svc.OnOSCJobFinished(&osc.Job{ID: id, Status: osc.JobAborted})
	}
	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	// 和 driveRelease 一样把 status/log 落回阶段行 —— 不这样做的话,阶段在库里
	// 会一直停在建单时的 running,skipUnrunStages(只认 pending/waiting)不会碰它,
	// 测不出真实场景里"刚被标成 skipped 的阶段被回调改回 failed"这件事。
	if err := fx.repo.UpdateReleaseStage(fx.stage.ID, map[string]any{
		"status": out.status, "log": out.log,
	}); err != nil {
		t.Fatalf("写阶段失败: %v", err)
	}
	fx.markReleaseWaiting()

	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	got := fx.reloadRelease()
	if got.Status != model.RunAborted {
		t.Errorf("发布单状态是 %s,期望仍然是 aborted —— OSC 的收尾回调把它改掉了", got.Status)
	}
	if !strings.Contains(got.Error, "已由") || !strings.Contains(got.Error, "终止") {
		t.Errorf("终止原因被冲掉了:%q", got.Error)
	}
}

// 成功分支同样缺这个检查:一张已经终止的单,若它挂着的迁移这时候恰好正常做完了
// (而不是被 Abort 叫停的那次跑出 JobDone,比如两件事前后脚发生),原逻辑会把
// 已经 skipped 的阶段悄悄改回 pending,在一张终态单子上留一行 debris。
func TestOnOSCJobFinished_DoesNotResurrectAnAlreadyAbortedRelease(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 72
	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	if err := fx.repo.UpdateReleaseStage(fx.stage.ID, map[string]any{
		"status": out.status, "log": out.log,
	}); err != nil {
		t.Fatalf("写阶段失败: %v", err)
	}
	fx.markReleaseWaiting()
	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	stageBeforeCallback := fx.reloadStage()

	// 迁移这次没有被 Abort 打断,是自己跑完的 —— 回调迟到了,单子已经是终态。
	fx.svc.OnOSCJobFinished(&osc.Job{ID: 72, Status: osc.JobDone})

	if got := fx.reloadRelease(); got.Status != model.RunAborted {
		t.Errorf("发布单状态是 %s,期望仍然是 aborted", got.Status)
	}
	if got := fx.reloadStage(); got.Status != stageBeforeCallback.Status {
		t.Errorf("阶段状态从 %s 变成了 %s —— 终态单子上留了一行 debris",
			stageBeforeCallback.Status, got.Status)
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
