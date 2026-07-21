package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/resp"
	"velagateway/pkg/totp"
)

// An MFA-enrolled account must present a valid TOTP code at login (two-step login);
// a non-enrolled account still logs in with the password alone (opt-in MFA).
func TestLogin_MFAEnrolledUserMustProvideCode(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	bind := app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	eq(t, bind.Code, 0, "bind mfa")
	var b struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(bind.Data, &b)
	if b.Secret == "" {
		t.Fatal("expected a bound secret")
	}

	loginBody := func(code string) map[string]string {
		m := map[string]string{"email": "chenhao@vela.io", "password": "vela123"}
		if code != "" {
			m["mfaCode"] = code
		}
		return m
	}

	// Password alone → prompted for the second factor (not a token).
	r := app.do(http.MethodPost, "/api/v1/auth/login", "", loginBody(""))
	eq(t, r.Code, resp.CodeMFARequired, "enrolled login without a code must be rejected")

	// Wrong code → rejected.
	if bad := app.do(http.MethodPost, "/api/v1/auth/login", "", loginBody("000001")); bad.Code == 0 {
		t.Error("a wrong MFA code must not log in")
	}

	// Correct code → success.
	ok := app.do(http.MethodPost, "/api/v1/auth/login", "", loginBody(totp.Code(b.Secret, time.Now())))
	eq(t, ok.Code, 0, "correct MFA code should log in")

	// A non-enrolled account is unaffected (password only).
	eq(t, app.loginCode("linwei@vela.io", "vela123"), 0, "non-enrolled login unaffected")
}
