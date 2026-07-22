package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

type riskCmdRow struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
}

func (a *testApp) riskLevel(token, command, env string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/risk-commands", token, nil)
	var rows []riskCmdRow
	_ = json.Unmarshal(r.Data, &rows)
	for _, c := range rows {
		if c.Command == command {
			return c.Env[env]
		}
	}
	return ""
}

// Upserting a risk command for one environment must NOT change its level in the
// other environments. Guards the contract the rules UI relies on: creating a
// staging rule for DROP must leave the seeded PROD=high interception intact
// (R11 — the UI used to send off-defaults for the untouched envs).
func TestRiskCommands_PartialUpsertKeepsOtherEnvs(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// seed: DROP is high on prod
	eq2(t, app.riskLevel(admin, "DROP", "prod"), "high", "seeded prod DROP level")

	// change only staging
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin,
		map[string]any{"command": "DROP", "env": map[string]string{"staging": "high"}}).Code, 0, "upsert staging DROP")

	// prod is untouched, staging updated
	eq2(t, app.riskLevel(admin, "DROP", "prod"), "high", "prod DROP must stay high")
	eq2(t, app.riskLevel(admin, "DROP", "staging"), "high", "staging DROP now high")
}

func eq2(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", msg, got, want)
	}
}
