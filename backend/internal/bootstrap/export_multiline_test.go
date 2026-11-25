package bootstrap

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// A plain multi-line SELECT ending with a semicolon — the finger habit carried
// over from the terminal, which strips it client-side — used to submit fine and
// then FAIL at execution: the worker handed the RAW text to the driver, and
// some drivers refuse a statement with a trailing semicolon/comment tail
// (sqlite: "not an error (21)"; the pg extended protocol is similarly strict).
// The worker must execute the normalised single statement.
func TestExport_MultilineAndTrailingSemicolonExecute(t *testing.T) {
	dbfile := filepath.Join(t.TempDir(), "ml.db")
	raw, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE orders (id INTEGER, status TEXT, amount INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO orders VALUES (1,'a',10),(2,'b',20),(3,'c',30)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	raw.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "ml-sqlite", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": dbfile,
	})
	eq(t, cr.Code, 0, "create real connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")

	cases := map[string]string{
		"multiline":       "SELECT id,\n       status,\n       amount\nFROM orders\nWHERE amount > 5\nORDER BY id",
		"crlf":            "SELECT id, status\r\nFROM orders\r\nWHERE amount > 5",
		"trailing-semi":   "SELECT id\nFROM orders;\n",
		"semi-then-cmt":   "SELECT id FROM orders;\n-- 完",
		"comment-inline":  "SELECT id, -- 主键\n  status\nFROM orders",
	}
	for name, q := range cases {
		id := app.submitExport(token, conn.ID, q, name)
		job := app.waitJob(token, id)
		if job.Status != "done" || job.Rows != 3 {
			t.Errorf("%s: want done/3 rows, got %s/%d (err %q)", name, job.Status, job.Rows, job.Error)
		}
	}
}
