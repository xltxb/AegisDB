package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An admin can change an existing instance's gateway policy via PATCH, without the
// patch accidentally toggling the connection's status. Invalid policies are
// rejected and non-admins are blocked.
func TestConnection_AdminSetsGatewayPolicy(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "polconn", "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
		"env": "staging", "policy": "strict",
	})
	eq(t, cr.Code, 0, "create connection")
	var conn struct {
		ID     int64  `json:"id"`
		Policy string `json:"policy"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	eq(t, conn.Policy, "strict", "initial policy")

	// Change the policy → updated, and status must stay unchanged.
	pr := app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn.ID), admin, map[string]any{"policy": "audit-only"})
	eq(t, pr.Code, 0, "patch policy code")
	var updated struct {
		Policy string `json:"policy"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(pr.Data, &updated)
	eq(t, updated.Policy, "audit-only", "policy updated")
	eq(t, updated.Status, conn.Status, "status must not be toggled by a policy patch")

	// An unknown policy is rejected.
	if bad := app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn.ID), admin, map[string]any{"policy": "bogus"}); bad.Code == 0 {
		t.Error("an invalid gateway policy must be rejected")
	}

	// A non-admin cannot change instance config.
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn.ID), owner, map[string]any{"policy": "strict"}).Code, 40300,
		"non-admin must be blocked from changing the policy")
}

// ED5: capability levels and dictionary rules are stored per environment, and a
// lookup that finds no row for an env falls through to "allow". So an
// environment string nobody seeded is not a harmless label — it is an
// unregulated environment, reachable by a typo ("uat", "pre", "Prod " with a
// space). Only the four known environments may be stored.
func TestConnection_RejectsUnknownEnvironment(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	body := func(env string) map[string]any {
		return map[string]any{
			"name": "probe-" + env, "engine": "mysql", "host": "10.0.0.9:3306",
			"env": env, "policy": "strict", "username": "u", "password": "p", "database": "d",
		}
	}
	for _, env := range []string{"uat", "pre", "production", ""} {
		r := app.do(http.MethodPost, "/api/v1/connections", token, body(env))
		if r.Code == 0 {
			t.Errorf("connection created with unregulated env %q — no capability or dictionary rows exist for it, so everything is allowed", env)
		}
	}
	// The four known environments still work.
	for _, env := range []string{"prod", "gli", "staging", "dev"} {
		eq(t, app.do(http.MethodPost, "/api/v1/connections", token, body(env)).Code, 0, "create env="+env)
	}
}
