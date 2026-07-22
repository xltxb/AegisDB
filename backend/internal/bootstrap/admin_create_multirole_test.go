package bootstrap

import (
	"net/http"
	"testing"
)

// Admin can provision an account directly (not invite-only): it is active on
// creation, so the user can sign in immediately with the initial password.
// Short passwords and duplicate emails are rejected.
func TestAdmin_CreateActiveAccount(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")

	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "newbie@vela.io", "name": "New Bie", "password": "initpass9",
		"roleIds": []int64{roID},
	}).Code, 0, "create account")

	// active on creation → sign in right away with the initial password
	eq(t, app.loginCode("newbie@vela.io", "initpass9"), 0, "new account can log in")

	if app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "short@vela.io", "password": "123", "roleIds": []int64{roID}}).Code == 0 {
		t.Error("short password must be rejected")
	}
	if app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "newbie@vela.io", "password": "initpass9", "roleIds": []int64{roID}}).Code == 0 {
		t.Error("duplicate email must be rejected")
	}
}

// A user's permissions are the UNION of every role they hold: granting a second
// role that carries admin rights lets the user pass the admin-only guard, and
// removing it revokes that — resolved live from the DB membership (the guard
// re-reads roles each request, so no re-login is required).
func TestMultiRole_UnionGrantsAndRevokesAdmin(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")
	adminID := app.roleIDByCode(admin, "admin")

	// a plain read-only account
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "grant@vela.io", "password": "initpass9", "roleIds": []int64{roID},
	}).Code, 0, "create ro account")
	user := app.login("grant@vela.io", "initpass9")

	// ro is not admin → the admin-only create endpoint is forbidden
	if got := app.do(http.MethodPost, "/api/v1/users", user, map[string]any{
		"email": "z@vela.io", "password": "initpass9", "roleIds": []int64{roID}}).Code; got != 40300 {
		t.Errorf("ro user blocked from admin create: got %d, want 40300", got)
	}

	target := app.userByEmail(admin, "grant@vela.io")
	// grant a second role that carries admin (ro stays primary)
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin,
		map[string]any{"roleIds": []int64{roID, adminID}}).Code, 0, "assign ro+admin")

	// union now includes admin → the SAME session may create a user
	eq(t, app.do(http.MethodPost, "/api/v1/users", user, map[string]any{
		"email": "z@vela.io", "name": "Zed", "password": "initpass9", "roleIds": []int64{roID}}).Code, 0,
		"union admin lets the ro user create accounts")

	// drop back to ro-only → admin access is revoked again
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin,
		map[string]any{"roleIds": []int64{roID}}).Code, 0, "revoke admin role")
	if got := app.do(http.MethodPost, "/api/v1/users", user, map[string]any{
		"email": "y@vela.io", "password": "initpass9", "roleIds": []int64{roID}}).Code; got != 40300 {
		t.Errorf("after revoking admin, ro user blocked again: got %d, want 40300", got)
	}
}
