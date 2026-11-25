package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Script execution carries a chosen target database: a safe script runs against
// it, and a risky script records it on the approval ticket so the approved run
// targets the same database.
func TestScriptExecute_TargetDatabase(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set path")

	// safe script with a target database → accepted
	safe := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": dev, "content": "SELECT 1;", "filename": "s.sql", "database": "orders_db",
	})
	eq(t, safe.Code, 0, "safe script with target db runs")

	// risky script with a target database → approval records the database
	run := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "content": "DROP TABLE orders_2024_q3;", "filename": "r.sql", "database": "analytics_db",
	})
	eq(t, run.Code, 42200, "risky script intercepted")
	var out struct {
		Exec struct {
			ApprovalNo string `json:"approvalNo"`
		} `json:"exec"`
	}
	_ = json.Unmarshal(run.Data, &out)

	r := app.do(http.MethodGet, "/api/v1/approvals?scope=all", token, nil)
	var aps []struct {
		ApNo     string `json:"apNo"`
		Database string `json:"database"`
	}
	_ = json.Unmarshal(approvalItems(t, r.Data), &aps)
	var got string
	for _, a := range aps {
		if a.ApNo == out.Exec.ApprovalNo {
			got = a.Database
		}
	}
	eq(t, got, "analytics_db", "approval records the chosen target database")
}
