package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// A1: a terminal command may bundle several statements. The capability matrix
// keys on the LEADING verb only, so a benign leading SELECT must not smuggle a
// mutating tail statement past the matrix on backends that accept stacked
// queries. `SELECT 1; UPDATE ...` on PROD (admin write=approve) must be judged by
// every statement and intercepted, not silently executed as a read.
func TestExec_StackedStatementTailIsJudged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "SELECT 1; UPDATE orders SET status = 'x'",
		"reason":       "stacked statement",
	})
	// The tail UPDATE requires approval in PROD; the whole command must be
	// intercepted rather than allowed (code 0).
	eq(t, r.Code, resp.CodeIntercepted, "stacked SELECT;UPDATE on PROD")
}

// ER3: a statement may begin with a stray separator (`;UPDATE …`). ParseVerb
// finds no leading keyword there, and an unrecognised verb used to map to the
// `select` capability — so a write landed in the read dimension and ran under a
// role that is only allowed to read. PostgreSQL and SQLite both accept the
// leading separator, so this executed for real.
func TestExec_LeadingSeparatorDoesNotDowngradeCapability(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          ";UPDATE accounts SET balance = 0",
		"reason":       "leading separator",
	})
	if r.Code == 0 {
		t.Fatalf("a PROD UPDATE ran unjudged because of a leading ';' (code=%d)", r.Code)
	}
	eq(t, r.Code, resp.CodeIntercepted, "leading-separator UPDATE on PROD")
}

// A batch pasted into the terminal arrives as ONE command string. Each
// sub-statement is judged (A1), but the verdict used to keep the FIRST
// approve-ranked statement's risk and rule: `UPDATE …(mid, matrix); DELETE
// …(high, dictionary)` produced a ticket graded mid with the dictionary rule
// dropped, so the approver reviewed a high-risk batch under a mid-risk label.
// The strictest statement must govern the whole batch — highest action, then
// highest risk — and every triggered rule must survive onto the ticket.
func TestExec_BatchVerdictEscalatesToStrictestStatement(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "UPDATE orders SET status='x'; DELETE FROM orders",
		"reason":       "batch with a high-risk tail",
	})
	eq(t, r.Code, resp.CodeIntercepted, "batch with approve statements intercepted")
	var d struct {
		ApprovalNo string `json:"approvalNo"`
		Risk       string `json:"risk"`
		Rule       string `json:"rule"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		t.Fatalf("decode exec data: %v", err)
	}
	eq(t, d.Risk, "high", "batch risk graded by its strictest statement, not its first")
	if !strings.Contains(d.Rule, "高危命令字典") {
		t.Errorf("dictionary rule of the tail DELETE lost from the verdict: rule=%q", d.Rule)
	}
	if !strings.Contains(d.Rule, "能力矩阵") {
		t.Errorf("matrix rule of the leading UPDATE lost from the verdict: rule=%q", d.Rule)
	}
	// The stored ticket must carry the escalated grade too — approvers route on it.
	ap := app.approvalSnapshot(token, d.ApprovalNo)
	eq(t, ap.RiskLevel, "high", "stored approval risk level")
}

// The terminal pre-check (/risk/check) drives the approval-reason modal. It
// used to judge the RAW string — leading verb only — so a batch whose risky
// statement was not first ("SELECT 1; UPDATE …") pre-checked as allow: no
// modal, no reason, and the operator was told the batch was safe even though
// Exec would intercept it. The pre-check must judge every statement, same as
// Exec does.
func TestRiskCheck_BatchTailStatementGoverns(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	got := app.riskCheck(token, prodConn, "SELECT 1; UPDATE orders SET status='x' WHERE id=1")
	eq(t, got.Action, "approve", "risky tail statement governs the pre-check")
	eq(t, got.RequiresApproval, true, "pre-check demands approval for the batch")
	eq(t, got.Risk, "mid", "pre-check risk from the strictest statement")
}
