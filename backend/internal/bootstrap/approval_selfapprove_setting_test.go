package bootstrap

import (
	"net/http"
	"testing"
)

// With approval.allowSelfApprove enabled, the initiator (who is also a chain
// member) may approve their own ticket — the opt-in escape hatch for small teams.
// The default-off behaviour is covered by TestApproval_InitiatorCannotSelfApprove.
func TestApproval_SelfApproveAllowedWhenEnabled(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"approval.allowSelfApprove": true,
	}).Code, 0, "enable self-approval")

	owner := app.login("zhangwei@vela.io", "vela123") // owner: chain member + can submit
	ap := app.submitProdHighRisk(owner)               // zhangwei is the initiator

	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0,
		"initiator should be able to self-approve when the setting is on")
}
