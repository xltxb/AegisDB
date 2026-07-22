package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An admin can edit an existing instance's full config (address, engine, env,
// policy, credentials, database) via PUT. Invalid policies are rejected and
// non-admins are blocked.
func TestConnection_AdminUpdatesInstance(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "editme", "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
		"env": "staging", "policy": "strict", "username": "u", "password": "secret", "database": "app",
	})
	eq(t, cr.Code, 0, "create connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)

	// Full edit — address, engine, env, policy, database all change; blank password
	// keeps the stored one.
	up := app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn.ID), admin, map[string]any{
		"name": "editme2", "engine": "PostgreSQL 15", "host": "10.0.0.9:5432",
		"env": "prod", "policy": "audit-only", "username": "u2", "password": "", "database": "newdb",
	})
	eq(t, up.Code, 0, "update connection code")
	var updated struct {
		Name     string `json:"name"`
		Engine   string `json:"engine"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Env      string `json:"env"`
		Policy   string `json:"policy"`
		Username string `json:"username"`
		Database string `json:"database"`
	}
	_ = json.Unmarshal(up.Data, &updated)
	eq(t, updated.Name, "editme2", "name")
	eq(t, updated.Engine, "PostgreSQL 15", "engine")
	eq(t, updated.Host, "10.0.0.9", "host")
	eq(t, updated.Port, 5432, "port")
	eq(t, updated.Env, "prod", "env")
	eq(t, updated.Policy, "audit-only", "policy")
	eq(t, updated.Username, "u2", "username")
	eq(t, updated.Database, "newdb", "database")

	// Invalid policy is rejected.
	if bad := app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn.ID), admin, map[string]any{
		"name": "x", "engine": "MySQL 8.0", "host": "1.1.1.1:3306", "env": "dev", "policy": "bogus",
	}); bad.Code == 0 {
		t.Error("an invalid gateway policy must be rejected on update")
	}

	// Non-admins cannot edit instance config.
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn.ID), owner, map[string]any{
		"name": "x", "engine": "MySQL 8.0", "host": "1.1.1.1:3306", "env": "dev", "policy": "strict",
	}).Code, 40300, "non-admin blocked from editing an instance")
}
