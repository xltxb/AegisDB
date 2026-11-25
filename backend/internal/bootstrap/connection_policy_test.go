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
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

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
	owner := app.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn.ID), owner, map[string]any{"policy": "strict"}).Code, 40300,
		"non-admin must be blocked from changing the policy")
}

// ED5: capability levels and dictionary rules are stored per environment, and a
// lookup that finds no row for an env falls through to "allow". So an
// environment string nobody seeded is not a harmless label — it is an
// unregulated environment, reachable by a typo ("pre", "legal", "Prod " with a
// space). Only a registered environment may be stored.
func TestConnection_RejectsUnknownEnvironment(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	body := func(env string) map[string]any {
		return map[string]any{
			"name": "probe-" + env, "engine": "mysql", "host": "10.0.0.9:3306",
			"env": env, "policy": "strict", "username": "u", "password": "p", "database": "d",
		}
	}
	for _, env := range []string{"pre", "production", "legal", ""} {
		r := app.do(http.MethodPost, "/api/v1/connections", token, body(env))
		if r.Code == 0 {
			t.Errorf("connection created with unregulated env %q — no capability or dictionary rows exist for it, so everything is allowed", env)
		}
	}
	// Every seeded environment still works.
	for _, env := range []string{"prod", "gli", "staging", "uat", "dev"} {
		eq(t, app.do(http.MethodPost, "/api/v1/connections", token, body(env)).Code, 0, "create env="+env)
	}
}

// issue #123:网关策略不参与判定,已从界面上拿掉 —— 新建、编辑都可以不传。新建落 strict(列非空),
// 编辑保持原值;传了仍校验。
func TestConnection_PolicyIsOptional(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	policyOf := func(r apiResp) string {
		t.Helper()
		var c struct {
			ID     int64  `json:"id"`
			Policy string `json:"policy"`
		}
		_ = json.Unmarshal(r.Data, &c)
		return c.Policy
	}
	body := func(policy string) map[string]any {
		b := map[string]any{"name": "no-policy", "engine": "MySQL 8.0", "host": "10.9.9.9:3306", "env": "dev"}
		if policy != "" {
			b["policy"] = policy
		}
		return b
	}
	r := app.do(http.MethodPost, "/api/v1/connections", admin, body(""))
	eq(t, r.Code, 0, "不传 policy 可以新建")
	eq(t, policyOf(r), "strict", "新建缺省 strict")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	put := func(policy string) apiResp {
		return app.do(http.MethodPut, "/api/v1/connections/"+itoa(c.ID), admin, body(policy))
	}
	eq(t, put("audit-only").Code, 0, "传了照样能改")
	r = put("")
	eq(t, r.Code, 0, "不传 policy 可以编辑")
	eq(t, policyOf(r), "audit-only", "编辑缺省保持原值")
	if put("bogus").Code == 0 {
		t.Error("传了就要合法")
	}
}
