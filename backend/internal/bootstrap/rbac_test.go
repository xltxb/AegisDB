package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// US#3-4: the menu set returned by /auth/me converges to the role's grants, and
// the same menu keys gate the protected endpoints. A read-only developer (ro)
// sees terminal + audit but not perms/settings, and is refused (40300) on the
// settings endpoint while still being allowed on audit.
func TestRBAC_ReadonlyRoleMenuConvergence(t *testing.T) {
	app := newTestApp(t)
	token := app.login("zhaolei@vela.io", "vela123") // ro role

	r := app.do(http.MethodGet, "/api/v1/auth/me", token, nil)
	eq(t, r.Code, 0, "auth/me response code")
	var me struct {
		RoleCode string          `json:"roleCode"`
		Menus    map[string]bool `json:"menus"`
	}
	if err := json.Unmarshal(r.Data, &me); err != nil {
		t.Fatalf("me decode: %v", err)
	}
	eq(t, me.RoleCode, "ro", "seeded role code")
	eq(t, me.Menus["terminal"], true, "ro sees terminal")
	eq(t, me.Menus["audit"], true, "ro sees audit")
	eq(t, me.Menus["perms"], false, "ro hidden from perms")
	eq(t, me.Menus["settings"], false, "ro hidden from settings")

	// Menu guard enforces the same boundary server-side.
	denied := app.do(http.MethodGet, "/api/v1/settings", token, nil)
	eq(t, denied.Code, 40300, "ro GET /settings should be forbidden")

	allowed := app.do(http.MethodGet, "/api/v1/audit", token, nil)
	eq(t, allowed.Code, 0, "ro GET /audit should be allowed")
}

// The System Settings menu is admin-only: the platform admin sees it and can
// reach /settings, while the DBA owner (an approver, not an admin) neither sees
// the menu nor is allowed through the guard.
func TestRBAC_SettingsMenuIsAdminOnly(t *testing.T) {
	app := newTestApp(t)

	menusOf := func(token string) map[string]bool {
		r := app.do(http.MethodGet, "/api/v1/auth/me", token, nil)
		eq(t, r.Code, 0, "auth/me response code")
		var me struct {
			Menus map[string]bool `json:"menus"`
		}
		if err := json.Unmarshal(r.Data, &me); err != nil {
			t.Fatalf("me decode: %v", err)
		}
		return me.Menus
	}

	admin := app.login("linwei@vela.io", "vela123")
	eq(t, menusOf(admin)["settings"], true, "admin sees settings menu")
	eq(t, app.do(http.MethodGet, "/api/v1/settings", admin, nil).Code, 0, "admin GET /settings allowed")

	owner := app.login("zhangwei@vela.io", "vela123") // DBA owner (approver, not admin)
	eq(t, menusOf(owner)["settings"], false, "owner must not see settings menu")
	eq(t, app.do(http.MethodGet, "/api/v1/settings", owner, nil).Code, 40300, "owner GET /settings forbidden")
}
