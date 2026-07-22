package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// Every audited command must record WHICH database it ran against — both an
// executed command and a risk command (intercepted → pending approval). We exec
// one of each with an explicit target database and assert the audit rows carry it.
func TestExec_TargetDatabaseRecordedOnAudit(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")
	const targetDB = "reporting_db"

	// Low-risk read → executed, audited with the target database.
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "SELECT 1;", "database": targetDB,
	})
	eq(t, r.Code, 0, "SELECT exec response code")

	// High-risk DDL → intercepted (approval pending), also audited with the database.
	ir := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "cleanup", "database": targetDB,
	})
	eq(t, ir.Code, resp.CodeIntercepted, "DROP intercepted code")

	rows := app.do(http.MethodGet, "/api/v1/audit", token, nil)
	eq(t, rows.Code, 0, "audit list code")
	var audit []struct {
		Command  string `json:"command"`
		Database string `json:"database"`
		Result   string `json:"result"`
	}
	if err := json.Unmarshal(rows.Data, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}

	var sawExec, sawRisk bool
	for _, a := range audit {
		if a.Command == "SELECT 1;" {
			sawExec = true
			eq(t, a.Database, targetDB, "executed-command audit target database")
		}
		if a.Command == "DROP TABLE orders;" {
			sawRisk = true
			eq(t, a.Database, targetDB, "risk-command audit target database")
			eq(t, a.Result, "pending", "risk command should be pending approval")
		}
	}
	if !sawExec || !sawRisk {
		t.Fatalf("missing audit rows: exec=%v risk=%v", sawExec, sawRisk)
	}
}
