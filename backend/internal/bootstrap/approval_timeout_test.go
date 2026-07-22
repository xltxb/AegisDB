package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

type auditResultRow struct {
	Result     string `json:"result"`
	ApprovalNo string `json:"approvalNo"`
}

func (a *testApp) countEscalations(token, apNo string) int {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/audit", token, nil)
	var rows []auditResultRow
	_ = json.Unmarshal(r.Data, &rows)
	n := 0
	for _, row := range rows {
		if row.ApprovalNo == apNo && row.Result == "warn" {
			n++
		}
	}
	return n
}

// Under the auto-escalate timeout policy, each overdue approval must escalate
// exactly ONCE — not on every 60s sweep. Regression for auto-escalate never
// marking the ticket, so repeated sweeps re-audited + re-notified forever (R13).
func TestApprovalTimeout_AutoEscalateOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// escalate immediately-overdue pending approvals
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"approval.onTimeout": "auto-escalate", "approval.timeoutMinutes": 0,
	}).Code, 0, "configure auto-escalate")

	// create a pending high-risk approval
	prod := app.connIDByEnv(admin, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "cleanup",
	})
	eq(t, r.Code, 42200, "high-risk intercepted")
	var er struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &er)
	if er.ApprovalNo == "" {
		t.Fatal("expected an approval number")
	}

	// run the timeout sweep three times (simulating three 60s ticks)
	app.svc.SweepApprovalTimeouts()
	app.svc.SweepApprovalTimeouts()
	app.svc.SweepApprovalTimeouts()

	if n := app.countEscalations(admin, er.ApprovalNo); n != 1 {
		t.Errorf("expected exactly 1 escalation audit for %s, got %d", er.ApprovalNo, n)
	}
}
