package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// auditRow is the GET /audit projection we assert on.
type auditRow struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

// auditRows fetches GET /audit (all risk, default range).
func (a *testApp) auditRows(token string) []auditRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/audit", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list audit: code=%d msg=%s", r.Code, r.Msg)
	}
	var rows []auditRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		a.t.Fatalf("audit decode: %v", err)
	}
	return rows
}

// FR-AUDIT / US#32: an all-safe script executed directly must still leave a full
// audit trail — every statement is recorded as executed, just like a terminal
// command. Observable purely through GET /audit.
func TestScriptExecute_SafeScriptLeavesAuditTrail(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	devConn := app.connIDByEnv(token, "dev")

	// Upload now requires a configured save path.
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set script path")

	// Two distinctly-named safe SELECTs so we can find them in the audit log.
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": devConn,
		"filename":     "safe.sql",
		"content":      "SELECT id FROM gapb_alpha; SELECT name FROM gapb_beta;",
	})
	eq(t, r.Code, 0, "safe script execute response code")

	rows := app.auditRows(token)
	wantSeen := map[string]bool{"gapb_alpha": false, "gapb_beta": false}
	for _, row := range rows {
		for marker := range wantSeen {
			if strings.Contains(row.Command, marker) {
				wantSeen[marker] = true
				eq(t, row.Result, "executed", "audit result for "+marker)
			}
		}
	}
	for marker, seen := range wantSeen {
		if !seen {
			t.Errorf("expected an audit row for safe statement %q, found none", marker)
		}
	}
}
