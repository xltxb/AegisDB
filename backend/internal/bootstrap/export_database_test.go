package bootstrap

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// The target database chosen at export time must be persisted on the job so the
// async worker runs against that schema, and the record shows which database was
// exported.
func TestExport_TargetDatabasePersistedOnJob(t *testing.T) {
	defer os.RemoveAll("export")
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": "SELECT id, name FROM users", "name": "rpt", "database": "analytics_db",
	})
	eq(t, r.Code, 0, "submit export code")

	var job struct {
		Database string `json:"database"`
	}
	if err := json.Unmarshal(r.Data, &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	eq(t, job.Database, "analytics_db", "export job target database")
}
