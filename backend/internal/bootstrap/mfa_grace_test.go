package bootstrap

import (
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/resp"
	"velagateway/pkg/totp"
)

// The PROD step-up asked for a fresh TOTP code on EVERY command. A code changes
// every 30s and is single-use here (anti-replay), so a DBA working through a
// production incident retyped one for each statement — enough friction that the
// realistic response is to switch the policy off entirely.
//
// A code now vouches for a working session on ONE instance: further commands on
// that same connection are allowed until the grace expires. It stays per
// connection (reaching a different instance is a separate decision), and it is
// bound to the session generation, so logging out, a password reset or a role
// change all void it.
func TestMFA_StepUpIsRememberedPerConnection(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")

	execProd := func(connID int64, code string) int {
		body := map[string]any{"connectionId": connID, "sql": "SELECT 1"}
		if code != "" {
			body["mfaCode"] = code
		}
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, body).Code
	}

	// Without a code the first command on this instance is refused.
	eq(t, execProd(prod, ""), resp.CodeMFARequired, "first PROD command needs a code")

	// With a valid code it runs…
	eq(t, execProd(prod, totp.Code(secret, time.Now())), 0, "verified command runs")

	// …and the next commands on the SAME instance no longer ask.
	eq(t, execProd(prod, ""), 0, "second command on the same instance is covered")
	eq(t, execProd(prod, ""), 0, "and the third")
}

// The grace is per instance: verifying against one production database says
// nothing about reaching a different one.
func TestMFA_GraceDoesNotCarryToAnotherConnection(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token)

	first := app.connIDByName(token, "order-cluster")
	second := app.connIDByName(token, "user-cluster")

	exec := func(connID int64, code string) int {
		body := map[string]any{"connectionId": connID, "sql": "SELECT 1"}
		if code != "" {
			body["mfaCode"] = code
		}
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, body).Code
	}

	eq(t, exec(first, totp.Code(secret, time.Now())), 0, "verified on the first instance")
	eq(t, exec(first, ""), 0, "covered on the first instance")
	eq(t, exec(second, ""), resp.CodeMFARequired, "a different instance must be verified separately")
}

// Ending the session voids the grace: the credential that vouched for it is gone.
func TestMFA_GraceIsVoidedWhenTheSessionEnds(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")

	exec := func(tok, code string) int {
		body := map[string]any{"connectionId": prod, "sql": "SELECT 1"}
		if code != "" {
			body["mfaCode"] = code
		}
		return app.do(http.MethodPost, "/api/v1/terminal/exec", tok, body).Code
	}

	eq(t, exec(token, totp.Code(secret, time.Now())), 0, "verified")
	eq(t, exec(token, ""), 0, "covered")

	// Logging out bumps the session generation.
	eq(t, app.do(http.MethodPost, "/api/v1/auth/logout", token, nil).Code, 0, "logout")
	fresh := app.login("linwei@vela.io", "vela123", totp.Code(secret, time.Now().Add(-30*time.Second)))
	eq(t, exec(fresh, ""), resp.CodeMFARequired, "a new session must step up again")
}

// Operators who want the old behaviour can have it: a zero grace asks every time.
func TestMFA_GraceCanBeDisabled(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"security.mfaGraceMinutes": 0})
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")

	exec := func(code string) int {
		body := map[string]any{"connectionId": prod, "sql": "SELECT 1"}
		if code != "" {
			body["mfaCode"] = code
		}
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, body).Code
	}
	eq(t, exec(totp.Code(secret, time.Now())), 0, "verified")
	eq(t, exec(""), resp.CodeMFARequired, "with no grace every command steps up")
}
