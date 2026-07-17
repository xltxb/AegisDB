package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/resp"
	"velagateway/pkg/totp"
)

// B8: the code used to ENABLE MFA must not double as a PROD step-up within its
// ~90s window. Enrollment reset the counter to 0 without consuming the code, so a
// captured enrollment code could authorize one PROD operation. Enabling must now
// spend that counter.
func TestMFA_EnrollmentCodeCannotAlsoStepUp(t *testing.T) {
	app := newTestApp(t)
	token := app.login("chenhao@vela.io", "vela123")

	// Self-service enrollment: setup issues a secret, enable verifies the code.
	setup := app.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, nil)
	eq(t, setup.Code, 0, "mfa setup")
	var s struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(setup.Data, &s)
	if s.Secret == "" {
		t.Fatal("expected a setup secret")
	}

	code := totp.Code(s.Secret, time.Now())
	en := app.do(http.MethodPost, "/api/v1/auth/mfa/enable", token, map[string]string{"code": code})
	eq(t, en.Code, 0, "mfa enable")

	// Reusing the enrollment code for a PROD step-up must be rejected as MFA.
	prod := app.connIDByEnv(token, "prod")
	up := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x", "mfaCode": code,
	})
	eq(t, up.Code, resp.CodeMFARequired, "reused enrollment code must fail MFA step-up")
}
