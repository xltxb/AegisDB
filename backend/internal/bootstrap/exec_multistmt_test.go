package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// A1: a terminal command may bundle several statements. The capability matrix
// keys on the LEADING verb only, so a benign leading SELECT must not smuggle a
// mutating tail statement past the matrix on backends that accept stacked
// queries. `SELECT 1; UPDATE ...` on PROD (admin write=approve) must be judged by
// every statement and intercepted, not silently executed as a read.
func TestExec_StackedStatementTailIsJudged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "SELECT 1; UPDATE orders SET status = 'x'",
		"reason":       "stacked statement",
	})
	// The tail UPDATE requires approval in PROD; the whole command must be
	// intercepted rather than allowed (code 0).
	eq(t, r.Code, resp.CodeIntercepted, "stacked SELECT;UPDATE on PROD")
}
