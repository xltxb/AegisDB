package bootstrap

import (
	"net/http"
	"testing"
)

// The initiator of an approval must never be able to approve their own ticket,
// even when they are also a member of the approval chain (owner role). Guards the
// two-person control on high-risk commands (R16).
func TestApproval_InitiatorCannotSelfApprove(t *testing.T) {
	app := newTestApp(t)
	// zhangwei is in the owner role → both a chain approver AND able to submit.
	owner := app.login("zhangwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(owner) // zhangwei is the initiator

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	if r.Code == 0 {
		t.Error("initiator must not be able to approve their own ticket")
	}

	// a different chain member (lina, owner) can still approve it
	other := app.login("lina@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", other, nil).Code, 0,
		"a non-initiator chain member should be able to approve")
}

// Deciding a ticket that was already decided must not be reported as success —
// otherwise A rejects, B approves, and B is told "approved and executed" while
// the real outcome was rejected (R19).
func TestApproval_DecideAlreadyDecidedIsNotSuccess(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("linwei@vela.io", "vela123") // admin: unrestricted, not in the owner chain
	ap := app.submitProdHighRisk(initiator)

	first := app.login("zhangwei@vela.io", "vela123") // owner, chain member
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", first, nil).Code, 0, "first reject succeeds")

	// a second member approving the already-rejected ticket must fail, not lie
	second := app.login("lina@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", second, nil); r.Code == 0 {
		t.Errorf("approving an already-decided ticket must not report success, got code=%d", r.Code)
	}
}
