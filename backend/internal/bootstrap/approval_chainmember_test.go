package bootstrap

import (
	"net/http"
	"testing"
)

// Only a member on the approval's chain may approve/reject it — holding the
// approve menu is not enough. Here L2 (Chen Hao) has the approve menu but is not
// on the chain (owner members are), so it is blocked; the owner can act.
func TestApproval_OnlyChainMemberCanDecide(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")     // has approve menu, NOT on the chain
	owner := app.login("zhangwei@vela.io", "vela123") // on the chain

	ap := app.submitProdHighRisk(admin) // chain = owner members

	// L2 is not a chain member → blocked
	blocked := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", l2, nil)
	eq(t, blocked.Code, 40300, "non-chain member blocked from approving")

	// still pending: its first step is still active
	got := app.approvalByNo(admin, ap.ApNo)
	if got.Steps[0].Status != "active" {
		t.Fatalf("approval should still be pending after blocked attempt, first step = %q", got.Steps[0].Status)
	}

	// L2 also cannot reject
	if app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", l2, nil).Code == 0 {
		t.Error("non-chain member must not be able to reject")
	}

	// a chain member (owner) can approve
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0, "chain member can approve")
}
