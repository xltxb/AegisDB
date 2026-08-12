package bootstrap

import (
	"net/http"
	"testing"
)

// US#22: the SAME high-risk command is layered by environment — a DROP runs
// freely on DEV (dictionary off + capability allow) but is intercepted for
// approval on PROD (dictionary high). Pure verdict, no side effects.
func TestEnvLayering_SameDropDevAllowsProdIntercepts(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const sql = "DROP TABLE orders;"

	dev := app.riskCheck(token, app.connIDByEnv(token, "dev"), sql)
	if dev.RequiresApproval {
		t.Errorf("DROP on DEV should be allowed, got action=%q risk=%q", dev.Action, dev.Risk)
	}
	eq(t, dev.Action, "allow", "DEV DROP action")

	prod := app.riskCheck(token, app.connIDByEnv(token, "prod"), sql)
	if !prod.RequiresApproval {
		t.Errorf("DROP on PROD should require approval, got action=%q", prod.Action)
	}
	eq(t, prod.Risk, "high", "PROD DROP risk")
}

// US#23: with strict mode on, a DELETE/UPDATE lacking a WHERE clause is upgraded
// to high and intercepted — even on an otherwise-permissive DEV instance — while
// the same statement WITH a WHERE clause stays allowed.
func TestStrictMode_NoWhereDeleteIntercepted(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	devConn := app.connIDByEnv(token, "dev")

	// Baseline (strict off): a bare DELETE on DEV is allowed.
	if before := app.riskCheck(token, devConn, "DELETE FROM orders"); before.RequiresApproval {
		t.Fatalf("precondition: bare DELETE on DEV should be allowed when strict is off, got %q", before.Action)
	}

	// Flip strict mode on via the settings endpoint (hot, no restart).
	r := app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"strictMode": true})
	eq(t, r.Code, 0, "enable strict mode response code")

	noWhere := app.riskCheck(token, devConn, "DELETE FROM orders")
	if !noWhere.RequiresApproval {
		t.Errorf("bare DELETE should be intercepted under strict mode, got action=%q", noWhere.Action)
	}
	eq(t, noWhere.Risk, "high", "no-WHERE DELETE risk under strict mode")

	withWhere := app.riskCheck(token, devConn, "DELETE FROM orders WHERE id = 1")
	if withWhere.RequiresApproval {
		t.Errorf("DELETE WHERE should stay allowed under strict mode, got action=%q", withWhere.Action)
	}
}

// Regression: an input with no leading SQL keyword (bare number, blank, or a
// comment-only line) must NOT be intercepted as a "write" on PROD — that
// produced a confusing "能力矩阵 · 需审批" prompt for something like "1". A real
// embedded high-risk command is still caught by the dictionary scan, though.
func TestBlankInput_NotInterceptedButStillScanned(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	for _, sql := range []string{"1", "   ", "-- just a comment", "42;"} {
		v := app.riskCheck(token, prod, sql)
		if v.RequiresApproval || v.Action != "allow" {
			t.Errorf("non-command %q on PROD should be allowed, got action=%q risk=%q rule=%q", sql, v.Action, v.Risk, v.Command)
		}
	}

	// Security preserved: an embedded DROP is still intercepted even with a
	// non-keyword prefix.
	danger := app.riskCheck(token, prod, "1; DROP TABLE orders")
	if !danger.RequiresApproval {
		t.Errorf("embedded DROP must still be intercepted, got action=%q", danger.Action)
	}
	eq(t, danger.Risk, "high", "embedded DROP risk")
}

// US#26: editing the risk dictionary takes effect immediately — no session
// restart. Flipping DROP@dev from off to high flips the live verdict.
func TestRiskDictionary_HotReload(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	devConn := app.connIDByEnv(token, "dev")
	const sql = "DROP TABLE staging_tmp;"

	if before := app.riskCheck(token, devConn, sql); before.RequiresApproval {
		t.Fatalf("precondition: DROP@dev should be allowed before the edit, got %q", before.Action)
	}

	r := app.do(http.MethodPatch, "/api/v1/risk-commands/DROP", token, map[string]any{
		"tier": "dev", "level": "high",
	})
	eq(t, r.Code, 0, "patch risk command response code")

	after := app.riskCheck(token, devConn, sql)
	if !after.RequiresApproval {
		t.Errorf("DROP@dev should require approval right after the dictionary edit, got action=%q", after.Action)
	}
	eq(t, after.Risk, "high", "DROP@dev risk after hot edit")
}
