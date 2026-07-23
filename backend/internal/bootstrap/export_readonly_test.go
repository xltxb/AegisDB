package bootstrap

import (
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
