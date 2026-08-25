package bootstrap

// The release feature in the PERMISSION model: a menu key that can be granted,
// and a capability dimension that can be tightened per tier.
//
// The failure these guard against is the quiet one: a dimension that exists in
// the console but has no rows behind it. A capability with no row reads as
// `allow`, so an administrator would see a matrix full of defaults, believe they
// had reviewed it, and have governed nothing.

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// matrixOf reads a role's capability matrix through the API.
func (a *testApp) matrixOf(token string, roleID int64) map[string]map[string]string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/roles/"+itoa(roleID), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("get role: code=%d msg=%s", r.Code, r.Msg)
	}
	var detail struct {
		Matrix map[string]map[string]string `json:"matrix"`
		Menus  map[string]bool              `json:"menus"`
	}
	if err := json.Unmarshal(r.Data, &detail); err != nil {
		a.t.Fatalf("decode role: %v", err)
	}
	return detail.Matrix
}

// TestReleaseCapabilityHasRowsForEveryRoleAndTier is the "no silent default"
// guard: every role × tier must carry a stored `release` level.
func TestReleaseCapabilityHasRowsForEveryRoleAndTier(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	var tiers []model.EnvTier
	if err := app.repo.DB().Find(&tiers).Error; err != nil {
		t.Fatalf("tiers: %v", err)
	}
	if len(tiers) < 5 {
		t.Fatalf("expected the five built-in tiers, got %d", len(tiers))
	}
	for _, code := range []string{"admin", "owner", "l2", "ro", "audit"} {
		m := app.matrixOf(token, app.roleIDByCode(token, code))
		rel := m[model.CapRelease]
		if rel == nil {
			t.Fatalf("role %s has no release capability row at all", code)
		}
		for _, tier := range tiers {
			if rel[tier.Code] == "" {
				t.Errorf("role %s has no release level for tier %s", code, tier.Code)
			}
		}
	}
}

// TestReleaseCapabilityMirrorsWrite pins the derivation the backfill and the
// seed share: a role that may not write on a tier may not release there, and one
// that needs approval to write needs it to release.
func TestReleaseCapabilityMirrorsWrite(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	for _, code := range []string{"admin", "owner", "l2", "ro", "audit"} {
		m := app.matrixOf(token, app.roleIDByCode(token, code))
		for tier, write := range m["write"] {
			if got := m[model.CapRelease][tier]; got != write {
				t.Errorf("role %s tier %s: release=%s but write=%s — the two must agree", code, tier, got, write)
			}
		}
	}
}

func TestPipelineMenuIsGrantable(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(token, "ro")

	// The seeded read-only role does not hold it…
	r := app.do(http.MethodGet, "/api/v1/roles/"+itoa(roID), token, nil)
	var detail struct {
		Menus map[string]bool `json:"menus"`
	}
	_ = json.Unmarshal(r.Data, &detail)
	if _, present := detail.Menus["pipeline"]; !present {
		t.Fatal("the pipeline menu must appear in the matrix so it can be granted")
	}
	if detail.Menus["pipeline"] {
		t.Error("read-only should not start with the release menu")
	}

	// …and an administrator can grant it through the permissions page.
	menus := detail.Menus
	menus["pipeline"] = true
	up := app.do(http.MethodPut, "/api/v1/roles/"+itoa(roID)+"/menus", token, map[string]any{"menus": menus})
	eq(t, up.Code, 0, "grant pipeline menu")

	r = app.do(http.MethodGet, "/api/v1/roles/"+itoa(roID), token, nil)
	_ = json.Unmarshal(r.Data, &detail)
	if !detail.Menus["pipeline"] {
		t.Error("the grant should stick")
	}
}

// TestReleaseCapabilityDeniesRaisingOnTier is the point of the dimension: the
// statement is harmless and the role may run it, but releases on that tier are
// denied — and the refusal happens at submit, not three stages later.
func TestReleaseCapabilityDeniesRaisingOnTier(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	pid := app.createPipeline(token, "受限流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})

	adminID := app.roleIDByCode(token, "admin")
	m := app.matrixOf(token, adminID)
	m[model.CapRelease]["dev"] = "deny"
	up := app.do(http.MethodPut, "/api/v1/roles/"+itoa(adminID)+"/capabilities", token, map[string]any{"matrix": m})
	eq(t, up.Code, 0, "set matrix")

	r := app.submitRelease(token, map[string]any{
		"title": "无害查询", "pipelineId": pid, "connectionId": dev, "sql": "SELECT 1;",
	})
	if r.Code == 0 {
		t.Fatal("release must be refused when the role's release capability on that tier is deny")
	}
	// The same statement still runs through the terminal: this dimension gates
	// RELEASES, not SQL.
	ex := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1;",
	})
	eq(t, ex.Code, 0, "the terminal path is unaffected")
}

// TestReleaseCapabilityApproveForcesApprovalStage: `approve` does not block the
// release, it demands ceremony — a flow that actually asks a human, even for a
// statement the matrix would have allowed.
func TestReleaseCapabilityApproveForcesApprovalStage(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	adminID := app.roleIDByCode(token, "admin")
	m := app.matrixOf(token, adminID)
	m[model.CapRelease]["dev"] = "approve"
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(adminID)+"/capabilities", token,
		map[string]any{"matrix": m}).Code, 0, "set matrix")

	bare := app.createPipeline(token, "无审批", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	r := app.submitRelease(token, map[string]any{
		"title": "低危变更", "pipelineId": bare, "connectionId": dev, "sql": "SELECT 1;",
	})
	if r.Code == 0 {
		t.Fatal("a flow with no approval stage must be refused when the role needs approval to release")
	}

	withAppr := app.createPipeline(token, "带审批", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	r = app.submitRelease(token, map[string]any{
		"title": "低危变更", "pipelineId": withAppr, "connectionId": dev, "sql": "SELECT 1;",
	})
	eq(t, r.Code, 0, "a flow with an approval stage is accepted")

	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(token, rel.ID, "waiting", "failed", "success")
	eq(t, waiting.Status, "waiting", "it parks on the approval it was forced to have")
}
