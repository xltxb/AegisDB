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

// ER3: a statement may begin with a stray separator (`;UPDATE …`). ParseVerb
// finds no leading keyword there, and an unrecognised verb used to map to the
// `select` capability — so a write landed in the read dimension and ran under a
// role that is only allowed to read. PostgreSQL and SQLite both accept the
// leading separator, so this executed for real.
func TestExec_LeadingSeparatorDoesNotDowngradeCapability(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          ";UPDATE accounts SET balance = 0",
		"reason":       "leading separator",
	})
	if r.Code == 0 {
		t.Fatalf("a PROD UPDATE ran unjudged because of a leading ';' (code=%d)", r.Code)
	}
	eq(t, r.Code, resp.CodeIntercepted, "leading-separator UPDATE on PROD")
}
