package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A script containing high-risk statements must be submitted for approval and
// show up in the approval channel — the `\i file` wrapper isn't itself risky,
// so the approval is created from the per-statement scan.
func TestScriptExecute_RiskyScriptCreatesApproval(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set path")

	run := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"content": "SELECT count(*) FROM orders;\nDROP TABLE orders_2024_q3;\n", "filename": "risky.sql", "connectionId": prod,
	})
	eq(t, run.Code, 42200, "risky script intercepted (approval required)")

	var out struct {
		Exec struct {
			Intercepted bool   `json:"intercepted"`
			ApprovalNo  string `json:"approvalNo"`
			Risk        string `json:"risk"`
		} `json:"exec"`
	}
	if err := json.Unmarshal(run.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Exec.Intercepted || out.Exec.ApprovalNo == "" {
		t.Fatalf("expected an approval number, got %+v", out.Exec)
	}
	eq(t, out.Exec.Risk, "high", "script risk level")

	// it must be visible in the approval channel (approvalByNo fails if absent)
	ap := app.approvalByNo(token, out.Exec.ApprovalNo)
	if len(ap.Steps) == 0 || ap.Steps[0].Status != "active" {
		t.Errorf("expected an active first approval step, got %+v", ap.Steps)
	}

	// and the ticket carries the actual script so approvers see what they sign off
	full := app.approvalDetail(token, out.Exec.ApprovalNo)
	if !strings.Contains(full, "DROP TABLE orders_2024_q3") {
		t.Errorf("approval command should include the risky SQL, got %q", full)
	}
}

// approvalDetail returns the raw command text of an approval by number.
func (a *testApp) approvalDetail(token, apNo string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	var aps []struct {
		ApNo    string `json:"apNo"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(r.Data, &aps)
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap.Command
		}
	}
	return ""
}
