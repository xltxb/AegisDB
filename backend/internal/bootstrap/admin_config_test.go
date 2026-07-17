package bootstrap

import (
	"net/http"
	"testing"
)

// Only the platform admin may change instance configuration, risk rules, or
// system settings — a non-admin (DBA lead) is blocked on every such mutation.
func TestAdminOnly_OwnerCannotEditConfig(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("zhangwei@vela.io", "vela123") // DBA 负责人
	admin := app.login("linwei@vela.io", "vela123")

	// instance config (connections)
	eq(t, app.do(http.MethodPost, "/api/v1/connections", owner, map[string]any{
		"name": "x", "engine": "MySQL 8.0", "host": "h:3306", "env": "dev", "policy": "strict",
	}).Code, 40300, "owner blocked: create connection")

	// risk rules (dictionary)
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", owner, map[string]any{
		"command": "FOOBAR", "env": map[string]string{"prod": "high", "staging": "off", "dev": "off"},
	}).Code, 40300, "owner blocked: risk command")

	// system settings
	eq(t, app.do(http.MethodPut, "/api/v1/settings", owner, map[string]any{"strictMode": true}).Code, 40300, "owner blocked: settings")

	// admin CAN edit the rule dictionary
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "FOOBAR", "env": map[string]string{"prod": "high", "staging": "off", "dev": "off"},
	}).Code, 0, "admin can edit rules")
}
