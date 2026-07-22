package bootstrap

import (
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
