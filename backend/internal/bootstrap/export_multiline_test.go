package bootstrap

import (
	"net/http"
	"testing"
)

// A plain multi-line SELECT ending with a semicolon — the finger habit carried
// over from the terminal, which strips it client-side — used to submit fine and
// then FAIL at execution: the worker handed the RAW text to the driver, and
// some drivers refuse a statement with a trailing semicolon/comment tail
// (the pg extended protocol is strict about this; sqlite used to answer "not an error (21)").
// The worker must execute the normalised single statement.
func TestExport_MultilineAndTrailingSemicolonExecute(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	connID, _ := app.newPGConn(token, "ml-pg", "dev",
		`CREATE TABLE orders (id INTEGER, status TEXT, amount INTEGER)`,
		`INSERT INTO orders VALUES (1,'a',10),(2,'b',20),(3,'c',30)`)
	conn := struct{ ID int64 }{connID}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")

	cases := map[string]string{
		"multiline":      "SELECT id,\n       status,\n       amount\nFROM orders\nWHERE amount > 5\nORDER BY id",
		"crlf":           "SELECT id, status\r\nFROM orders\r\nWHERE amount > 5",
		"trailing-semi":  "SELECT id\nFROM orders;\n",
		"semi-then-cmt":  "SELECT id FROM orders;\n-- 完",
		"comment-inline": "SELECT id, -- 主键\n  status\nFROM orders",
	}
	for name, q := range cases {
		id := app.submitExport(token, conn.ID, q, name)
		job := app.waitJob(token, id)
		if job.Status != "done" || job.Rows != 3 {
			t.Errorf("%s: want done/3 rows, got %s/%d (err %q)", name, job.Status, job.Rows, job.Error)
		}
	}
}
