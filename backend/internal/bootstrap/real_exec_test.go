package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/pkg/crypto"
)

// Real execution path: a connection configured with credentials (here a real
// PostgreSQL database) runs the SQL against the actual database — the export CSV
// contains the real rows, and terminal exec returns the real row count. This
// exercises the same database/sql pipeline used for MySQL/TiDB/DWS/Oracle.
func TestRealExec_PGRunsAgainstRealDB(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	// a real database with known contents, registered as a real connection
	connID, _ := app.newPGConn(token, "real-pg", "dev",
		`CREATE TABLE people (id INTEGER, name TEXT, email TEXT)`,
		`INSERT INTO people VALUES (1,'Ada Lovelace','ada@x.io'),(2,'Alan Turing','alan@x.io'),(3,'Grace Hopper','grace@x.io')`)
	conn := struct{ ID int64 }{connID}

	// export the real table
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")
	id := app.submitExport(token, conn.ID, "SELECT id, name, email FROM people ORDER BY id", "people")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "export status")
	eq(t, job.Rows, 3, "real exported row count")

	body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(job.fileParts()[0]), token)
	csv, err := crypto.ZipDecrypt(body, job.Password)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	text := string(csv)
	for _, want := range []string{"id,name,email", "Ada Lovelace", "ada@x.io", "Grace Hopper"} {
		if !strings.Contains(text, want) {
			t.Errorf("exported CSV missing real data %q; got:\n%s", want, text)
		}
	}

	// terminal exec runs against the real DB too
	er := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": conn.ID, "sql": "SELECT * FROM people",
	})
	eq(t, er.Code, 0, "real exec code")
	var exec struct {
		Rows int `json:"rows"`
	}
	_ = json.Unmarshal(er.Data, &exec)
	eq(t, exec.Rows, 3, "real exec row count")
}
