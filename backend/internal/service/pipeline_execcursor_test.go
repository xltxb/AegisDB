package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 执行阶段要能从半截继续。
//
// 一条走 OSC 的语句要跑几小时,阶段在那期间停在 waiting。恢复时如果从头再来,
// 前面几条已经落库的变更会被**再执行一遍** —— 那是一次重复的生产变更,而它不会
// 报任何错(一条 ALTER 重跑会报 1061,但一条 UPDATE 不会)。

func TestStageExecute_ResumesFromTheCursorInsteadOfRerunningEverything(t *testing.T) {
	// 夹具:三条语句的发布单,游标停在 2 —— 前两条"已经执行过了"。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2; UPDATE t SET c=3")
	fx.stage.ExecCursor = 2
	fx.saveStage()

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s,日志:%s", out.status, out.log)
	}
	// 只有第三条该被下发。
	if n := fx.exec.count(); n != 1 {
		t.Errorf("下发了 %d 条语句,期望 1 条 —— 前两条被重复执行了", n)
	}
	if got := fx.exec.last(); !strings.Contains(got, "c=3") {
		t.Errorf("下发的是 %q,期望第三条", got)
	}
}

func TestStageExecute_KeepsTheLogWrittenBeforeItPaused(t *testing.T) {
	// driveRelease 每次都用 out.log **覆盖**阶段的 log 字段。恢复时如果从空开始,
	// 前面几条的执行记录会消失 —— 而那正是一次跨了几小时的执行最需要留下的东西。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2")
	fx.stage.ExecCursor = 1
	fx.stage.Log = "· [1/2] 执行成功 · 1 行受影响 (3ms)\n"
	fx.saveStage()

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if !strings.Contains(out.log, "[1/2]") {
		t.Errorf("恢复之后的日志里没有第一条的记录:\n%s", out.log)
	}
	if !strings.Contains(out.log, "[2/2]") {
		t.Errorf("恢复之后的日志里没有第二条的记录:\n%s", out.log)
	}
}

func TestStageExecute_AdvancesTheCursorAsItGoes(t *testing.T) {
	// 游标要**边走边记**,不是跑完一起记:进程在第二条之后挂掉时,库里得写着 2。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2")

	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	got := fx.reloadStage()
	if got.ExecCursor != 2 {
		t.Errorf("执行完两条之后游标是 %d,期望 2", got.ExecCursor)
	}
}

// 影响行数要跨恢复累计。从 0 重新起算的话,一次跨了几小时、分几段跑完的执行,
// 界面上显示的行数会比真实值小 —— 而那个数字是人判断"这次变更动了多少东西"
// 的唯一依据。
func TestStageExecute_KeepsCountingRowsAcrossAResume(t *testing.T) {
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2; UPDATE t SET c=3")
	fx.stage.ExecCursor = 2
	fx.stage.Rows = 20 // 前两条一共影响了 20 行
	fx.saveStage()

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s,日志:%s", out.status, out.log)
	}
	// 假执行器每条报 1 行,所以第三条加 1。
	if out.rows != 21 {
		t.Errorf("影响行数 = %d,期望 21(历史 20 + 本次 1) —— 跨恢复的累计被重置了", out.rows)
	}
}

// "边走边记"这句话本身要有一条测试专门守住它,而不是只看跑完之后的游标值:
// 把落库挪到循环外、跑完一起写一次,TestStageExecute_AdvancesTheCursorAsItGoes
// 一样是绿的(两条都成功时,结果本就是 2)。真正能分开两者的,是**中途失败**时
// 游标停在哪 —— 边走边记的话,第一条已经落库的游标不会被第二条的失败抹掉;
// 挪到循环外的话,失败直接 return,那次写入根本没有发生,游标停在发起前的 0。
func TestStageExecute_StopsTheCursorAtTheStatementThatFailed(t *testing.T) {
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2")
	fx.svc.Executor = &failAtExecutor{failAt: 2}

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunFailed {
		t.Fatalf("第二条失败,阶段应报 failed,实际 %s · 日志:%s", out.status, out.log)
	}
	got := fx.reloadStage()
	if got.ExecCursor != 1 {
		t.Errorf("第二条失败时游标应停在 1(第一条已经做完),实际 %d", got.ExecCursor)
	}
}

// failAtExecutor 只给上面那条用例用:第 failAt 次调用报失败,其余都成功。
type failAtExecutor struct {
	mu     sync.Mutex
	n      int
	failAt int
}

func (f *failAtExecutor) Run(_ context.Context, _ *model.Connection, _ string, _ time.Duration) gateway.ExecResult {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == f.failAt {
		return gateway.ExecResult{Output: "· 模拟失败", Err: errors.New("模拟失败")}
	}
	return gateway.ExecResult{Output: "执行成功 · 1 行受影响", Rows: 1, Ms: 3}
}

func (f *failAtExecutor) Test(*model.Connection) (bool, string) { return true, "" }
