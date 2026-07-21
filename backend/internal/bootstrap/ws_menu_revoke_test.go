package bootstrap

import (
	"net/http"
	"testing"
)

// B9: revoking a role's terminal menu must cut off an already-open WS session on
// its next command. Menu changes don't bump the token version, so the per-exec
// re-validation has to re-check the menu too — otherwise a live socket keeps
// executing SQL after its access was pulled. Menus are resolved as the union
// across the user's roles; linwei holds only the admin role, so revoking admin's
// terminal menu removes access.
func TestTerminalWS_MenuRevokedMidConnection(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123") // admin: terminal menu + all connections
	adminRole := app.roleIDByCode(token, "admin")
	dev := app.connIDByEnv(token, "dev")

	c := app.dialWS(token)
	defer c.Close()

	if typ := wsExecType(t, c, dev, "SELECT 1"); typ != "output" {
		t.Fatalf("first exec: got type %q, want output", typ)
	}

	// revoke the terminal menu for the admin role (this connection's user)
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(adminRole)+"/menus", token,
		map[string]any{"menus": map[string]bool{"terminal": false}}).Code, 0, "revoke terminal menu")

	// the next command on the same open socket must be refused
	if typ := wsExecType(t, c, dev, "SELECT 1"); typ != "session_revoked" {
		t.Errorf("post-revoke exec: got type %q, want session_revoked", typ)
	}
}
