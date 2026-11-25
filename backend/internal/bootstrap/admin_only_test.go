package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Permission management is platform-admin only: the DBA lead (owner) has no
// perms menu, so it can neither view nor mutate roles/users. Admin can.
func TestAdminOnly_OwnerCannotEditPermissions(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("zhangwei@vela.io", "vela123") // DBA 负责人 (owner)
	admin := app.login("linwei@vela.io", "vela123")

	// role ids via admin (owner can no longer even list roles)
	rr := app.do(http.MethodGet, "/api/v1/roles", admin, nil)
	eq(t, rr.Code, 0, "admin can list roles")
	var roles []struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rr.Data, &roles)
	var roID, adminRoID int64
	for _, r := range roles {
		if r.Code == "ro" {
			roID = r.ID
		}
		if r.Code == "admin" {
			adminRoID = r.ID
		}
	}
	if roID == 0 {
		t.Fatal("ro role not found")
	}

	// owner cannot even VIEW the permission pages
	eq(t, app.do(http.MethodGet, "/api/v1/roles", owner, nil).Code, 40300, "owner blocked: list roles")
	eq(t, app.do(http.MethodGet, "/api/v1/users", owner, nil).Code, 40300, "owner blocked: list users")

	// owner is BLOCKED on every role/permission mutation
	blocked := map[string]apiResp{
		"capabilities": app.do(http.MethodPut, "/api/v1/roles/"+itoa(roID)+"/capabilities", owner, map[string]any{"matrix": map[string]any{}}),
		"menus":        app.do(http.MethodPut, "/api/v1/roles/"+itoa(roID)+"/menus", owner, map[string]any{"menus": map[string]any{"settings": true}}),
		"tags":         app.do(http.MethodPut, "/api/v1/roles/"+itoa(roID)+"/tags", owner, map[string]any{"tags": []string{}}),
		"member":       app.do(http.MethodPost, "/api/v1/roles/"+itoa(adminRoID)+"/members", owner, map[string]any{"userId": 1}),
	}
	for name, r := range blocked {
		eq(t, r.Code, 40300, "owner blocked on "+name)
	}

	// owner cannot reset another user's password either
	pw := app.do(http.MethodPost, "/api/v1/users/1/password", owner, map[string]any{"password": "hacked123"})
	eq(t, pw.Code, 40300, "owner blocked on password reset")

	// admin CAN perform the same edits
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roID)+"/menus", admin, map[string]any{"menus": map[string]any{"settings": false}}).Code, 0, "admin can edit menus")
}
