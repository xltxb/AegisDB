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

// EU2: effective permissions are the union of tbl_user.role_id and the
// tbl_role_member rows. Every account-creation path writes BOTH for the primary
// role, but "remove member" only deleted the membership row — so revoking the
// role an account was created with removed it from the role's member list in the
// UI while the user kept every permission it granted. An administrator who
// revoked platform-admin from someone would be told it worked and be wrong.
func TestMultiRole_RemovingMemberRevokesThePrimaryRoleToo(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	adminID := app.roleIDByCode(admin, "admin")
	roID := app.roleIDByCode(admin, "ro")

	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "temp-admin@vela.io", "name": "Temp Admin", "password": "initpass9",
		"roleIds": []int64{adminID, roID}, // admin is the primary role
	}).Code, 0, "create account holding admin + ro")

	victim := app.login("temp-admin@vela.io", "initpass9")
	eq(t, app.do(http.MethodGet, "/api/v1/users", victim, nil).Code, 0, "admin rights before revocation")

	uid := app.userByEmail(admin, "temp-admin@vela.io").ID
	eq(t, app.do(http.MethodDelete, "/api/v1/roles/"+itoa(adminID)+"/members/"+itoa(uid), admin, nil).Code, 0, "remove member")

	// The revocation must actually bite — the guard re-reads roles per request.
	fresh := app.login("temp-admin@vela.io", "initpass9") // revocation also ends old sessions
	if app.do(http.MethodGet, "/api/v1/users", fresh, nil).Code == 0 {
		t.Error("admin rights survived removal from the role: the primary role_id still grants them")
	}
	// The account is downgraded, not broken — it keeps working as its other role.
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", fresh, nil).Code, 0, "account still usable after downgrade")
}

// Revoking a user's LAST role would leave role_id pointing at nothing, which
// fails session construction and locks the account instead of downgrading it.
// That must be refused outright rather than half-applied.
func TestMultiRole_RemovingTheOnlyRoleIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")

	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "solo@vela.io", "name": "Solo", "password": "initpass9",
		"roleIds": []int64{roID},
	}).Code, 0, "create single-role account")
	uid := app.userByEmail(admin, "solo@vela.io").ID

	if app.do(http.MethodDelete, "/api/v1/roles/"+itoa(roID)+"/members/"+itoa(uid), admin, nil).Code == 0 {
		t.Error("removing the user's only role should be refused")
	}
	eq(t, app.loginCode("solo@vela.io", "initpass9"), 0, "account still works")
}
