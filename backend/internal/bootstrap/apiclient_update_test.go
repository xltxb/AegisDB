package bootstrap

// 凭据编辑的部分更新语义(审查修复 #1)。
//
// The console's enable/disable switch PUTs only {enabled}. The update must
// treat every omitted field as "keep", because the fields it would otherwise
// zero are security controls: a wiped allow_ips turns an IP-pinned credential
// into an any-source one, silently, on an action whose intent was the opposite
// (the operator was DISABLING something).

import (
	"encoding/json"
	"net/http"
	"testing"
)

type apiClientRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	AllowIPs string `json:"allowIps"`
	Scopes   string `json:"scopes"`
	Enabled  bool   `json:"enabled"`
}

func (a *testApp) apiClientByName(token, name string) apiClientRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/api-clients", token, nil)
	var rows []apiClientRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		a.t.Fatalf("decode api clients: %v", err)
	}
	for _, c := range rows {
		if c.Name == name {
			return c
		}
	}
	a.t.Fatalf("api client %q not found", name)
	return apiClientRow{}
}

func TestAPIClientToggleKeepsAllowlistAndScopes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	uid := app.userIDByEmail(admin, "zhangwei@vela.io")
	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "CI-Pinned", "userId": uid,
		"allowIps": "10.20.0.0/16", "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create client")
	created := app.apiClientByName(admin, "CI-Pinned")
	eq(t, created.AllowIPs, "10.20.0.0/16", "allowlist stored")

	// The exact payload the console switch sends: enabled and NOTHING else.
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(created.ID), admin,
		map[string]any{"enabled": false})
	eq(t, r.Code, 0, "disable")

	got := app.apiClientByName(admin, "CI-Pinned")
	eq(t, got.Enabled, false, "switch did its one job")
	// …and ONLY its one job.
	eq(t, got.AllowIPs, "10.20.0.0/16", "IP allowlist survives a toggle")
	eq(t, got.Scopes, "release:read", "scopes survive a toggle")

	// Round-trip back on: still intact.
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(created.ID), admin,
		map[string]any{"enabled": true})
	eq(t, r.Code, 0, "re-enable")
	got = app.apiClientByName(admin, "CI-Pinned")
	eq(t, got.Enabled, true, "re-enabled")
	eq(t, got.AllowIPs, "10.20.0.0/16", "allowlist survives the round trip")
}

// TestAPIClientUpdateOmittedEnabledKeepsState — the dual of the wipe: a payload
// that edits the allowlist but says nothing about enabled must not disable the
// credential as a side effect.
func TestAPIClientUpdateOmittedEnabledKeepsState(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	uid := app.userIDByEmail(admin, "zhangwei@vela.io")
	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "CI-Edit", "userId": uid, "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create client")
	created := app.apiClientByName(admin, "CI-Edit")
	eq(t, created.Enabled, true, "created enabled")

	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(created.ID), admin,
		map[string]any{"allowIps": "192.168.1.0/24"})
	eq(t, r.Code, 0, "edit allowlist")

	got := app.apiClientByName(admin, "CI-Edit")
	eq(t, got.AllowIPs, "192.168.1.0/24", "allowlist updated")
	eq(t, got.Enabled, true, "an allowlist edit must not disable the credential")

	// An explicit empty string is still an EDIT — "回到不限来源" is a decision an
	// operator can make, and it must remain expressible.
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(created.ID), admin,
		map[string]any{"allowIps": ""})
	eq(t, r.Code, 0, "clear allowlist explicitly")
	eq(t, app.apiClientByName(admin, "CI-Edit").AllowIPs, "", "explicit clear works")
}
