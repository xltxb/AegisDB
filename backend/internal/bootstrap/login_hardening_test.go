package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type auditCmdRow struct {
	Command string `json:"command"`
	Result  string `json:"result"`
	Actor   string `json:"actor"`
}

// A failed login must leave an audit trail (R29).
func TestLogin_FailureIsAudited(t *testing.T) {
	app := newTestApp(t)

	// one failed attempt
	eq(t, app.loginCode("chenhao@vela.io", "wrong-password"), 40100, "failed login rejected")

	// admin can see a rejected 'login failed' audit row for that email
	admin := app.login("linwei@vela.io", "vela123")
	var rows []auditCmdRow
	_ = json.Unmarshal(app.auditItemsRaw(admin, ""), &rows)
	found := false
	for _, row := range rows {
		if strings.Contains(row.Command, "login failed") && row.Result == "rejected" && row.Actor == "chenhao@vela.io" {
			found = true
		}
	}
	if !found {
		t.Error("expected a rejected 'login failed' audit row for the failed attempt")
	}
}

// Repeated failed logins from one source are rate-limited, so even a correct
// password is refused once the threshold is hit (R29).
func TestLogin_RateLimitedAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t)
	for i := 0; i < 5; i++ {
		app.do(http.MethodPost, "/api/v1/auth/login", "",
			map[string]any{"email": "linwei@vela.io", "password": "wrong"})
	}
	// the correct password is now blocked by the limiter
	r := app.do(http.MethodPost, "/api/v1/auth/login", "",
		map[string]any{"email": "linwei@vela.io", "password": "vela123"})
	if r.Code == 0 {
		t.Errorf("login should be rate-limited after repeated failures, got code=%d", r.Code)
	}
}
