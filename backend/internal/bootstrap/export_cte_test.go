package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// A multi-line CTE query — the everyday shape of a DWS/PostgreSQL analytical
// export — was refused as "not read-only": the export gate keys on the leading
// verb, and WITH fell into the default (write) capability. The effective verb
// of a WITH statement is its MAIN verb; a CTE that carries a mutation
// (`WITH d AS (DELETE … RETURNING …) SELECT …` really deletes, ER9) must keep
// being refused.
func TestExport_CTESelectAcceptedMutatingCTERefused(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")

	cte := "WITH monthly AS (\n  SELECT user_id, SUM(amount) AS total\n  FROM orders\n  GROUP BY user_id\n)\nSELECT * FROM monthly WHERE total > 100"
	r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": cte, "name": "cte-monthly",
	})
	eq(t, r.Code, 0, "read-only CTE export accepted")

	mut := "WITH gone AS (\n  DELETE FROM orders WHERE status='void' RETURNING id\n)\nSELECT count(*) FROM gone"
	mr := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": mut, "name": "cte-delete",
	})
	eq(t, mr.Code, resp.CodeForbidden, "mutating CTE still refused")
}
