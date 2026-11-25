package bootstrap

// 升级到"通过不再代执行"之后,历史工单(旧行为下批准即执行)的 executed_at 是空的,
// 而"已批准 + executed_at 为空 + 非发布单"正是"这张单可以执行"的条件 —— 一条七月份
// 就跑过的 DROP TABLE 会重新变成可以一键再跑。
//
// 回填要挡住这个。但它同时**不能误伤**一张真的在等执行的工单:把它标成已执行,那条
// 命令再也不会跑,而人以为它跑了。两个方向都不能错,所以下面两条要一起看。

import (
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// oldBehaviourTicket 造一张"旧行为下已经跑过"的工单:结果里是真实的执行输出,
// executed_at 空着 —— 正是升级前的数据长的样子。
func (a *testApp) oldBehaviourTicket(result string) *model.Approval {
	a.t.Helper()
	decided := time.Now().Add(-72 * time.Hour)
	ap := &model.Approval{
		ApNo: "AP-OLD-" + itoa(time.Now().UnixNano()%100000), InitiatorID: 1, Initiator: "林伟",
		Command: "DROP TABLE t_history", Status: model.StatusApproved, RiskLevel: model.RiskHigh,
		Result: result, DecidedAt: &decided,
	}
	if err := a.repo.DB().Create(ap).Error; err != nil {
		a.t.Fatalf("造历史工单: %v", err)
	}
	return ap
}

func (a *testApp) executedAtOf(id int64) *time.Time {
	a.t.Helper()
	var ap model.Approval
	if err := a.repo.DB().First(&ap, id).Error; err != nil {
		a.t.Fatalf("读工单: %v", err)
	}
	return ap.ExecutedAt
}

// 历史单必须被标记 —— 它已经跑过了。
func TestBackfill_AHistoricalTicketIsMarkedExecuted(t *testing.T) {
	app := newTestApp(t)
	ap := app.oldBehaviourTicket("执行成功 · 2 行受影响")

	if err := backfillApprovalExecuted(app.repo.DB()); err != nil {
		t.Fatalf("回填: %v", err)
	}
	if app.executedAtOf(ap.ID) == nil {
		t.Fatal("旧行为下已经跑过的工单必须标上执行时刻,否则它还能被再跑一次")
	}

	// 标记之后,发起人再点执行必须被拒。
	admin := app.login("linwei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("历史工单不该还能执行 —— 那条命令七月份就已经跑过了")
	}
}

// 执行失败的历史单同样标记:旧行为下失败就是"这张单用掉了",要重来得重新提单。
// 放它回到可执行状态,等于给了它一次旧行为从来没给过的重试。
func TestBackfill_AHistoricalTicketThatFailedIsAlsoSpent(t *testing.T) {
	app := newTestApp(t)
	ap := app.oldBehaviourTicket("· 数据库执行失败: unable to open database file")

	if err := backfillApprovalExecuted(app.repo.DB()); err != nil {
		t.Fatalf("回填: %v", err)
	}
	if app.executedAtOf(ap.ID) == nil {
		t.Error("失败的历史单也用掉了这次授权,不该回到可执行状态")
	}
}

// ——— 反方向,同等重要 ———
// 一张真的在等发起人执行的工单绝不能被标记。标错了,那条命令再也不会跑,而人以为
// 它跑了 —— 比重复执行更难发现。
func TestBackfill_ATicketGenuinelyAwaitingExecutionIsLeftAlone(t *testing.T) {
	app := newTestApp(t)
	waiting := app.oldBehaviourTicket(model.AwaitingExecution) // 新代码写下的那句话

	if err := backfillApprovalExecuted(app.repo.DB()); err != nil {
		t.Fatalf("回填: %v", err)
	}
	if app.executedAtOf(waiting.ID) != nil {
		t.Fatal("正在等待执行的工单被标成了已执行 —— 那条命令再也不会跑")
	}
}

// 幂等:判据不依赖时间点,所以每次启动都跑也安全。这一条真正防的是"第二次启动把
// 待执行的工单误标" —— 那才是幂等在这里的意义。
func TestBackfill_IsIdempotentAcrossRestarts(t *testing.T) {
	app := newTestApp(t)
	waiting := app.oldBehaviourTicket(model.AwaitingExecution)
	done := app.oldBehaviourTicket("执行成功 · 1 行受影响")

	for i := 0; i < 3; i++ {
		if err := backfillApprovalExecuted(app.repo.DB()); err != nil {
			t.Fatalf("第 %d 次回填: %v", i+1, err)
		}
	}
	if app.executedAtOf(waiting.ID) != nil {
		t.Error("反复启动把待执行的工单标掉了")
	}
	if app.executedAtOf(done.ID) == nil {
		t.Error("历史单该被标记")
	}
}

// 发布单不归这条路管:它的执行由流水线拥有,executed_at 空着是正常状态,
// 标上反而会让人以为它在这里跑过。
func TestBackfill_ReleaseTicketsAreNotTouched(t *testing.T) {
	app := newTestApp(t)
	decided := time.Now().Add(-48 * time.Hour)
	rel := &model.Approval{
		ApNo: "AP-REL-1", InitiatorID: 1, Initiator: "林伟", ReleaseID: 42,
		Command: "ALTER TABLE t ADD COLUMN c INT", Status: model.StatusApproved,
		RiskLevel: model.RiskMid, Result: "· 已批准,由发布流水线继续执行", DecidedAt: &decided,
	}
	if err := app.repo.DB().Create(rel).Error; err != nil {
		t.Fatalf("造发布单: %v", err)
	}
	if err := backfillApprovalExecuted(app.repo.DB()); err != nil {
		t.Fatalf("回填: %v", err)
	}
	if app.executedAtOf(rel.ID) != nil {
		t.Error("发布单的执行归流水线,不该在这里被标记")
	}
}
