package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/totp"
)

// A TOTP code consumed for a PROD step-up must not be replayable to disable MFA.
// Regression for MFADisable using totp.Validate without consuming the counter
// (R15) — an attacker who captured one step-up code could turn MFA off.
func TestMFA_StepUpCodeCannotAlsoDisableMFA(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	// admin-bind MFA (does not consume a code, so the counter starts clean)
	bind := app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	eq(t, bind.Code, 0, "bind mfa")
	var b struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(bind.Data, &b)
	if b.Secret == "" {
		t.Fatal("expected a bound secret")
	}

	token := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	code := totp.Code(b.Secret, time.Now())

	// use the code for a PROD step-up (passes MFA → intercepted), consuming it
	up := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x", "mfaCode": code,
	})
	eq(t, up.Code, 42200, "step-up passes MFA and intercepts")

	// replaying the same code to disable MFA must be rejected
	dis := app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]string{"code": code})
	if dis.Code == 0 {
		t.Error("a consumed step-up code must not be replayable to disable MFA")
	}
}
