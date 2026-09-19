package service

import (
	"strings"
	"testing"

	"velagateway/internal/model"
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
