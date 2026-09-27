package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/imtest"
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

	if res := f.Click("p-ln", "AP-999999", true); !strings.Contains(res.Toast, "工单不存在") {
		t.Fatalf("toast = %q", res.Toast)
	}
}
