package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/totp"
)

// TestLogoutRevokesToken verifies that logging out invalidates the token
// server-side (token-version bump), so a captured token can't be reused (M1).
func TestLogoutRevokesToken(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// token works before logout
	eq(t, app.do(http.MethodGet, "/api/v1/connections", token, nil).Code, 0, "pre-logout request ok")

	eq(t, app.do(http.MethodPost, "/api/v1/auth/logout", token, nil).Code, 0, "logout ok")

	// same token is now rejected (superseded session generation)
	eq(t, app.do(http.MethodGet, "/api/v1/connections", token, nil).Code, 40100, "post-logout request rejected")
}

// TestMFA_CodeCannotBeReplayed verifies a consumed TOTP step-up code is rejected
// on reuse within its validity window (M3 anti-replay).
func TestMFA_CodeCannotBeReplayed(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	secret := app.setupMFA(token)

	code := totp.Code(secret, time.Now())
	// first use of the code passes MFA → reaches the engine (high-risk intercept)
	first := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x", "mfaCode": code,
	})
	eq(t, first.Code, 42200, "first use of code passes MFA (intercept)")

	// replay of the SAME code is rejected
	replay := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x", "mfaCode": code,
	})
	eq(t, replay.Code, 42800, "replayed MFA code rejected")
}

// setupMFA enrolls the current user: fetches a secret, enables MFA, and returns
// the secret for later step-up codes. Enrollment uses a PREVIOUS-window code
// (accepted via ±1-step skew) so that enabling spends that earlier counter (B8),
// leaving the current-window code fresh for the caller's follow-up step-up/
// disable — mirroring real usage where enroll and next action span time steps.
func (a *testApp) setupMFA(token string) string {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("mfa setup: code=%d msg=%s", r.Code, r.Msg)
	}
	var s struct {
		Secret     string `json:"secret"`
		OtpauthURI string `json:"otpauthUri"`
	}
	if err := json.Unmarshal(r.Data, &s); err != nil {
		a.t.Fatalf("mfa setup decode: %v", err)
	}
	if s.Secret == "" || s.OtpauthURI == "" {
		a.t.Fatal("mfa setup returned empty secret/uri")
	}
	en := a.do(http.MethodPost, "/api/v1/auth/mfa/enable", token, map[string]string{
		"code": totp.Code(s.Secret, time.Now().Add(-30*time.Second)),
	})
	eq(a.t, en.Code, 0, "mfa enable code")
	return s.Secret
}

// FR Session&Security · Require MFA: once enrolled, a PROD op is blocked until a
// valid TOTP step-up code is supplied. requireMFA is on in the seeded settings.
func TestMFA_ProdStepUpRequiredThenPasses(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// before enrollment, MFA does not gate (keeps existing flows working):
	pre := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x",
	})
	eq(t, pre.Code, 42200, "pre-enroll exec should intercept (approval), not MFA-block")

	secret := app.setupMFA(token)

	// enrolled + no code → blocked with CodeMFARequired.
	blocked := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x",
	})
	eq(t, blocked.Code, 42800, "enrolled PROD exec without code → MFA required")

	// wrong code → still blocked.
	bad := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x", "mfaCode": "000000",
	})
	eq(t, bad.Code, 42800, "wrong MFA code → still blocked")

	// valid code → passes MFA and reaches the engine (high-risk → intercept).
	ok := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x",
		"mfaCode": totp.Code(secret, time.Now()),
	})
	eq(t, ok.Code, 42200, "valid MFA code → intercepted for approval")

	// a non-PROD op never needs MFA.
	dev := app.connIDByEnv(token, "dev")
	d := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1;",
	})
	eq(t, d.Code, 0, "dev exec never MFA-gated")
}

// Disabling MFA (with a valid code) lifts the PROD step-up requirement.
func TestMFA_DisableLiftsRequirement(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token)

	dis := app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]string{
		"code": totp.Code(secret, time.Now()),
	})
	eq(t, dis.Code, 0, "mfa disable code")

	prod := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "x",
	})
	eq(t, r.Code, 42200, "after disable, PROD exec intercepts (no MFA gate)")
}

// FR Session&Security · Session TTL: the issued token honors security.sessionTTL.
func TestSessionTTL_FromSetting(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	set := app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"security.sessionTTL": "4h"})
	eq(t, set.Code, 0, "save sessionTTL")

	exp := app.loginExpiry("linwei@vela.io", "vela123")
	d := time.Until(exp)
	if d < 3*time.Hour || d > 5*time.Hour {
		t.Errorf("sessionTTL=4h: token expiry %v from now, want ~4h", d)
	}
}

// loginExpiry logs in and parses the expiresAt timestamp.
func (a *testApp) loginExpiry(email, pass string) time.Time {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": email, "password": pass})
	eq(a.t, r.Code, 0, "login code")
	var d struct {
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		a.t.Fatalf("login decode: %v", err)
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", d.ExpiresAt, time.Local)
	if err != nil {
		a.t.Fatalf("parse expiresAt %q: %v", d.ExpiresAt, err)
	}
	return ts
}

// FR Session&Security · IP allowlist: when enabled, a source IP outside the list
// is blocked; loopback and listed IPs pass.
func TestIPAllowlist_BlocksForeignIP(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// enable with a list that excludes 8.8.8.8.
	set := app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"security.ipAllowEnabled": true,
		"security.ipAllowlist":    "10.20.0.0/16, 203.0.113.5",
	})
	eq(t, set.Code, 0, "save ip allowlist")

	// loopback (no forwarded header) still passes — no self-lockout.
	eq(t, app.do(http.MethodGet, "/api/v1/connections", token, nil).Code, 0, "loopback passes")

	// a foreign forwarded IP is blocked (gin trusts proxies by default).
	eq(t, app.getWithIP("/api/v1/connections", token, "8.8.8.8").Code, 40301, "foreign IP blocked")

	// an IP inside the CIDR passes.
	eq(t, app.getWithIP("/api/v1/connections", token, "10.20.7.9").Code, 0, "listed CIDR passes")
}

// getWithIP issues a GET with an X-Forwarded-For header to simulate a source IP.
func (a *testApp) getWithIP(path, token, ip string) apiResp {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", ip)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("get %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var out apiResp
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&out); err != nil {
		a.t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

// EU1: /auth/mfa/setup re-issues an enrollment secret, and to do so it cleared
// mfa_enabled — with nothing but a session required. So whoever held a session
// could switch off a user's second factor and receive a fresh secret in the
// response, after which password-only login worked again and the PROD step-up
// treated the account as never enrolled. /auth/mfa/disable demands a valid code
// for the same outcome; an attacker simply used the cheaper door. Re-enrolment
// must not disarm a factor that is currently protecting the account.
func TestMFA_SetupCannotDisarmAnEnabledFactorWithoutProof(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token) // MFA is now enabled

	r := app.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, nil)
	if r.Code == 0 {
		t.Error("re-enrolment succeeded without proving possession of the current factor")
	}

	// The factor is still armed: password-only login must not be enough.
	if tok := app.loginRaw("linwei@vela.io", "vela123"); tok.Code == 0 {
		t.Error("password-only login succeeded — MFA was disarmed by the setup call")
	}
	// And a valid current code still logs in, i.e. the original secret is intact.
	ok := app.loginRaw("linwei@vela.io", "vela123", totp.Code(secret, time.Now()))
	eq(t, ok.Code, 0, "login with the original factor still works")
}
