package bootstrap

import (
	"net/http"
	"testing"
)

// C3: PatchUser must reject an out-of-vocabulary status. An arbitrary value
// leaves a user in a limbo state that login (only "active" passes) and the
// disabled-session check don't cover, so still-valid JWTs keep working.
func TestPatchUser_StatusWhitelist(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	bad := app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin, map[string]any{"status": "bogus"})
	if bad.Code == 0 {
		t.Error("an invalid status must be rejected, got ok")
	}

	ok := app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin, map[string]any{"status": "disabled"})
	eq(t, ok.Code, 0, "a valid status (disabled) must be accepted")
}
