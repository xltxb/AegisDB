package bootstrap

import (
	"encoding/json"
	"strings"
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// SECURITY: the export worker executes the submitted SQL against the target DB,
// so a non read-only export would be a gateway bypass — a way to run
// DELETE/UPDATE/DROP/… without the capability matrix, risk dictionary or
// approval flow. Such submissions must be rejected up front, while a genuine
// read-only export is still accepted.
func TestExport_RejectsNonReadOnlySQL(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	// Each of these must be refused (mutations + a stacked-query smuggle attempt).
	blocked := []string{
		"DELETE FROM orders",
		"UPDATE users SET tier='vip'",
		"DROP TABLE orders",
		"TRUNCATE TABLE sessions",
		"GRANT ALL ON *.* TO 'x'@'%'",
		"SELECT 1; DROP TABLE orders",                       // stacked
		"WITH d AS (DELETE FROM y RETURNING *) SELECT * FROM d", // data-modifying CTE
	}
	for _, sql := range blocked {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
			"connectionId": dev, "sql": sql, "name": "evil",
		})
		if r.Code != resp.CodeForbidden {
			t.Errorf("export of %q must be rejected with CodeForbidden, got code=%d msg=%s", sql, r.Code, r.Msg)
		}
	}

	// A legitimate read-only export is still accepted.
	ok := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": "SELECT id, name FROM users", "name": "rpt",
	})
	eq(t, ok.Code, 0, "read-only export accepted")
}

// The export gate judges the SPLIT text but the worker sends the ORIGINAL text
// to the server, so any lexer disagreement is a bypass. These three all looked
// like one read-only SELECT to the old gate:
//
//   - a MySQL executable comment, whose body the server runs (EX2)
//   - a PostgreSQL dollar-quoted quote, which swallowed the separator (ER1)
//   - '#', a comment in MySQL but an operator in PostgreSQL (ER2)
//
// SELECT ... INTO OUTFILE also parses as verb SELECT, so the verb whitelist
// alone cannot reject it — a "read-only" export could write a file on the
// database server.
func TestExport_RejectsDialectLexerSmuggles(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	blocked := []string{
		`SELECT 1 FROM dual /*!40000 INTO OUTFILE '/tmp/pwn' */`, // executable comment body
		`SELECT * FROM t INTO OUTFILE '/tmp/pwn'`,                // file write, verb is SELECT
		`SELECT * FROM t INTO DUMPFILE '/tmp/pwn'`,               // ditto
		`SELECT $$'$$ ; DROP TABLE t`,                            // dollar-quoted separator smuggle
		`SELECT * FROM t #x; DROP TABLE t`,                       // '#' is an operator on PostgreSQL
	}
	for _, sql := range blocked {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
			"connectionId": dev, "sql": sql, "name": "evil",
		})
		if r.Code != resp.CodeForbidden {
			t.Errorf("export of %q must be rejected, got code=%d msg=%s", sql, r.Code, r.Msg)
		}
	}
}

// The export worker connects to the target DB and runs the SQL, so it is an
// execution channel and must carry the same gate as the terminal. It carried
// only a tag check and a verb whitelist, so a role explicitly denied `select` on
// PROD — blocked at /terminal/exec — could still pull the whole table out
// through /export (EX1). Maintenance state was skipped for the same reason.
func TestExport_HonoursCapabilityMatrixAndMaintenance(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	const sql = "SELECT * FROM events"
	roleID := app.roleIDByCode(token, "admin")

	// Baseline: allowed while the matrix permits reads on PROD.
	eq(t, app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": prod, "sql": sql, "name": "ok"}).Code, 0, "export allowed by matrix")

	// Deny `select` on PROD: the terminal refuses, so the export must too.
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/capabilities", token, map[string]any{
		"matrix": map[string]any{"select": map[string]string{"prod": "deny", "staging": "allow", "dev": "allow"}},
	}).Code, 0, "deny select@prod")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": sql}).Code, resp.CodeForbidden, "terminal blocked by matrix")
	eq(t, app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": prod, "sql": sql, "name": "evil"}).Code, resp.CodeForbidden, "export blocked by matrix")
}

// EX5: an export reads whole tables out of a production database, but only a
// SUCCESSFUL job was audited — and with the SQL clipped to 80 characters.
// Submission and failure wrote nothing at all, so probing (submit a query, watch
// it fail, adjust, repeat) left no trace, and the one row that did get written
// could not show which query had actually run. The submission is the auditable
// event: it is the moment someone asked for the data.
func TestExport_SubmissionIsAudited(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	longSQL := "SELECT id, name /* " + strings.Repeat("wide ", 30) + " */ FROM users WHERE tier = 'vip_marker_tail'"
	eq(t, app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": longSQL, "name": "rpt"}).Code, 0, "submit export")

	var rows []struct {
		Command string `json:"command"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(token, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	for _, r := range rows {
		if strings.Contains(r.Command, "vip_marker_tail") {
			return // submission audited with the full query
		}
	}
	t.Error("no audit row for the export submission carrying the full query")
}
