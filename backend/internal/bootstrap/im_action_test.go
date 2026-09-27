package bootstrap

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"velagateway/internal/imchannel/imtest"
	"velagateway/internal/model"
)

// runningFake 装好一个开着交互审批、长连接已在跑的假渠道。
func (a *testApp) runningFake(token string) *imtest.Fake {
	a.t.Helper()
	f := a.fakeIM("fake")
	a.enableIM(token, "fake")
	f.WaitRunning(a.t)
	return f
}

func TestIM_UnboundClickIsRefusedAndAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	ap := app.submitProdHighRisk(admin)

	res := f.Click("stranger", ap.ApNo, true)
	if !strings.Contains(res.Toast, "未绑定") {
		t.Fatalf("toast = %q, want an unbound-account message", res.Toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "a stranger's click decides nothing")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "fake:stranger" || rows[0].ApprovalNo != ap.ApNo {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestIM_StrangerWithHugePlatformIDIsStillAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	ap := app.submitProdHighRisk(admin)

	f.Click(strings.Repeat("x", 300), ap.ApNo, true)
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 {
		t.Fatalf("a huge platform id must still leave one audit row, got %d", len(rows))
	}
	if n := len([]rune(rows[0].ActorName)); n > 64 {
		t.Fatalf("actor name is %d runes, column is 64", n)
	}
}

func TestIM_BoundButNotOnChainIsRefusedWithTheChainReason(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("chenhao@vela.io", "fake", "p-ch")
	ap := app.submitProdHighRisk(admin)

	res := f.Click("p-ch", ap.ApNo, true)
	eq(t, res.Toast, "仅审批链成员可处理该工单", "BlockNotInChain reason, verbatim")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "still pending")
}

func TestIM_SelfApprovalFollowsTheSameSettingAsTheConsole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(owner)

	res := f.Click("p-zw", ap.ApNo, true)
	if !strings.Contains(res.Toast, "不能审批自己发起的工单") {
		t.Fatalf("toast = %q, want BlockSelf reason", res.Toast)
	}
	// admin (linwei) is neither the initiator nor a chain member for this ticket
	// (the owner chain is zhangwei/lina — see TestApproval_VisibleOnlyToInitiatorAndChain),
	// so the visibility check reads back through the initiator (owner) instead.
	eq(t, app.approvalRow(owner, ap.ApNo).Status, "pending", "self-approval blocked")

	// 打开开关后同一次点击放行 —— 证明判定走的是 DecideBlockFor,不是另写的一份。
	app.setSettings(admin, map[string]any{"approval.allowSelfApprove": true})
	f.Click("p-zw", ap.ApNo, true)
	eq(t, app.approvalRow(owner, ap.ApNo).Status, "approved", "allowed once the setting says so")
}

func TestIM_DisabledOrServiceAccountBindingIsTreatedAsStranger(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	zw := app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ln := app.bindIM("lina@vela.io", "fake", "p-ln")
	if err := app.repo.UpdateUserFields(zw, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserFields(ln, map[string]any{"kind": "service"}); err != nil {
		t.Fatal(err)
	}
	ap := app.submitProdHighRisk(admin)

	for _, pid := range []string{"p-zw", "p-ln"} {
		if res := f.Click(pid, ap.ApNo, true); !strings.Contains(res.Toast, "未绑定") {
			t.Errorf("%s: toast = %q, want refusal as unbound", pid, res.Toast)
		}
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "still pending")
}

func TestIM_ChainMemberApprovesAndTheAuditNamesTheChannel(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin)

	res := f.Click("p-zw", ap.ApNo, true)
	eq(t, res.Toast, "已批准,请发起人回控制台执行", "approve toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved")
	rows, _ := app.repo.AuditByOperatorLike("%(fake)")
	if len(rows) != 1 || rows[0].ApprovalNo != ap.ApNo {
		t.Fatalf("audit rows with channel label = %+v", rows)
	}

	// 重投 / 连点:回显状态,不再决策一次
	again := f.Click("p-zw", ap.ApNo, true)
	if !strings.Contains(again.Toast, "已处理") {
		t.Fatalf("second click toast = %q", again.Toast)
	}
	rows, _ = app.repo.AuditByOperatorLike("%(fake)")
	eq(t, len(rows), 1, "the duplicate click wrote no second decision")
}

func TestIM_RejectClick(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")
	ap := app.submitProdHighRisk(admin)

	eq(t, f.Click("p-ln", ap.ApNo, false).Toast, "已驳回", "reject toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "rejected", "rejected")
}

func TestIM_ClickAfterConsoleDecisionEchoesState(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")
	ap := app.submitProdHighRisk(admin)
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "console reject")

	if res := f.Click("p-ln", ap.ApNo, true); !strings.Contains(res.Toast, "已处理") {
		t.Fatalf("toast = %q, want already-handled", res.Toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "rejected", "the console decision stands")
}

func TestIM_ClickOnUnknownTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")

	// 不存在的单号与「你不在链上」说同一句话:按钮点击可以被伪造,单号是否存在、
	// 最后怎样了,不该对随便哪个绑定过的人有问必答。
	eq(t, f.Click("p-ln", "AP-999999", true).Toast, "仅审批链成员可处理该工单",
		"an unknown ticket answers like a ticket you are not on")
}

// 不在链上的人点一张已处理的单,得不到它的终态。
func TestIM_NonChainClickOnASettledTicketLearnsNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("chenhao@vela.io", "fake", "p-ch") // 绑了,但不在链上
	ap := app.submitProdHighRisk(admin)
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "console reject")

	res := f.Click("p-ch", ap.ApNo, true)
	eq(t, res.Toast, "仅审批链成员可处理该工单", "no hint of the final state for an outsider")
}

// 控制台的审批接口先过 menu("approve") 才问 DecideBlockFor;IM 点击没有那道中间件,
// 所以菜单这一条必须落在 DecideBlockFor 里 —— 否则角色被收回审批菜单的人,在 IM 里还能批。
func TestIM_ChainMemberWithoutTheApproveMenuIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	zw := app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin) // 工单建好时 zhangwei 还有审批菜单

	u, err := app.repo.GetUserByID(zw)
	if err != nil {
		t.Fatal(err)
	}
	for _, rid := range app.repo.EffectiveRoleIDs(u) {
		if err := app.repo.SetMenus(rid, map[string]bool{"approve": false}); err != nil {
			t.Fatal(err)
		}
	}

	res := f.Click("p-zw", ap.ApNo, true)
	eq(t, res.Toast, "你的角色当前没有审批权限,请联系管理员", "the approve-menu reason, verbatim")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "still pending")
}

// decisionRows 数这张单上的决定审计行:发起那一行 operator 为空,批 / 驳那一行带着审批人。
func (a *testApp) decisionRows(apNo string) int64 {
	a.t.Helper()
	var n int64
	if err := a.repo.DB().Model(&model.AuditLog{}).
		Where("approval_no = ? AND operator <> ''", apNo).Count(&n).Error; err != nil {
		a.t.Fatal(err)
	}
	return n
}

// 平台重投:同一次点击几乎同时到了两遍,只能决定一次。
func TestIM_ConcurrentDuplicateClicksDecideOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Click("p-zw", ap.ApNo, true)
		}()
	}
	wg.Wait()

	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved")
	rows, _ := app.repo.AuditByOperatorLike("%(fake)")
	eq(t, len(rows), 1, "exactly one approve audit row carrying the channel label")
	eq(t, app.decisionRows(ap.ApNo), int64(1), "exactly one decision recorded")
}

// IM 与控制台同时批同一张单:只有一边算数。
func TestIM_ClickRacingTheConsoleDecidesOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")
	ap := app.submitProdHighRisk(admin)
	owner := app.login("zhangwei@vela.io", "vela123")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		f.Click("p-ln", ap.ApNo, true)
	}()
	go func() {
		defer wg.Done()
		app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	}()
	wg.Wait()

	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved")
	eq(t, app.decisionRows(ap.ApNo), int64(1), "exactly one decision recorded across both paths")
}
