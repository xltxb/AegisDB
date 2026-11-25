package bootstrap

import (
	"strings"
	"encoding/json"
	"net/http"
	"testing"
)

type userRow struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	MFAEnabled bool   `json:"mfaEnabled"`
}

func (a *testApp) userByEmail(token, email string) userRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/users", token, nil)
	var us []userRow
	_ = json.Unmarshal(r.Data, &us)
	for _, u := range us {
		if u.Email == email {
			return u
		}
	}
	a.t.Fatalf("user %q not found", email)
	return userRow{}
}

func (a *testApp) loginCode(email, pw string) int {
	r := a.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": email, "password": pw})
	return r.Code
}

// Admin can reset a user's password and manage their OTP binding.
func TestAdmin_UserPasswordAndOTP(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	// reset password → new one works, old one stops working
	eq(t, app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/password", admin, map[string]any{"password": "newpass9"}).Code, 0, "set password")
	eq(t, app.loginCode("chenhao@vela.io", "newpass9"), 0, "login with new password")
	if app.loginCode("chenhao@vela.io", "vela123") == 0 {
		t.Error("old password should no longer work")
	}
	// too-short password is rejected
	if app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/password", admin, map[string]any{"password": "123"}).Code == 0 {
		t.Error("short password should be rejected")
	}

	// bind OTP → returns secret + otpauth, user shows mfaEnabled
	bind := app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	eq(t, bind.Code, 0, "bind otp")
	var b struct {
		Secret     string `json:"secret"`
		OtpauthURI string `json:"otpauthUri"`
	}
	_ = json.Unmarshal(bind.Data, &b)
	if b.Secret == "" || b.OtpauthURI == "" {
		t.Fatalf("expected secret + otpauth, got %+v", b)
	}
	if !app.userByEmail(admin, "chenhao@vela.io").MFAEnabled {
		t.Error("user should show mfaEnabled after bind")
	}

	// reset OTP → unbound
	eq(t, app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/reset", admin, nil).Code, 0, "reset otp")
	if app.userByEmail(admin, "chenhao@vela.io").MFAEnabled {
		t.Error("user should show mfa off after reset")
	}
}

// PatchUser must apply a status change through a targeted column update (not a
// full-row Save of a stale snapshot). Disabling then re-enabling must take
// effect on login — this also guards the correct column mapping for the R8 fix.
func TestAdmin_PatchUserStatusTakesEffect(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	eq(t, app.loginCode("chenhao@vela.io", "vela123"), 0, "login before disable")

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "disable user")
	if app.loginCode("chenhao@vela.io", "vela123") == 0 {
		t.Error("disabled user must not be able to log in")
	}

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(target.ID), admin,
		map[string]any{"status": "active"}).Code, 0, "re-enable user")
	eq(t, app.loginCode("chenhao@vela.io", "vela123"), 0, "login after re-enable")
}

// EU4: recordAudit is called for logins, executions, exports and approval
// transitions — but not for a single administrative mutation. That leaves the
// most sensitive actions in the system unrecorded: whoever holds an admin
// session can reset an approver's password, bind a new MFA secret (the endpoint
// returns it in plain text), sign in as that approver to authorise their own
// high-risk ticket, then set the password back. The hash chain would faithfully
// record "the approver approved it" and contain nothing about the impersonation.
// Account-altering actions must land in the same immutable log.
func TestAdmin_AccountMutationsAreAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")
	tid := itoa(target.ID)

	eq(t, app.do(http.MethodPost, "/api/v1/users/"+tid+"/password", admin, map[string]any{"password": "newpass99"}).Code, 0, "reset password")
	eq(t, app.do(http.MethodPost, "/api/v1/users/"+tid+"/mfa/bind", admin, nil).Code, 0, "bind mfa")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+tid, admin, map[string]any{"status": "disabled"}).Code, 0, "disable account")

	var rows []struct {
		Actor   string `json:"actor"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(admin, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	// Each mutation must be traceable to the administrator who made it.
	for _, want := range []string{"password", "mfa", "status"} {
		found := false
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Command), want) && strings.Contains(r.Command, "chenhao@vela.io") {
				found = true
				eq(t, r.Actor, "Lin Wei", "audited actor for "+want)
			}
		}
		if !found {
			t.Errorf("no audit row for the %q change to chenhao@vela.io", want)
		}
	}
}

// EU4 (second half): the capability matrix, menu grants, tag grants and role
// membership decide what every user may do. Editing them is a privilege change
// and belongs in the immutable log for the same reason account edits do —
// otherwise someone can widen their own role, act, and narrow it back with the
// chain showing only the action, never the grant that permitted it.
func TestAdmin_PermissionChangesAreAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")
	rid := itoa(roID)

	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+rid+"/capabilities", admin, map[string]any{
		"matrix": map[string]any{"ddl": map[string]string{"prod": "allow"}}}).Code, 0, "widen matrix")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+rid+"/tags", admin, map[string]any{
		"tags": []string{"orders"}}).Code, 0, "grant tags")

	var rows []struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(admin, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	for _, want := range []string{"capabilities", "tags"} {
		found := false
		for _, r := range rows {
			if strings.Contains(r.Command, "admin.role."+want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no audit row for the role %s change", want)
		}
	}
}
