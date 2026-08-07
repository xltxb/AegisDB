package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// The tier/environment split exists because both rule lookups treat a missing
// row as permission granted (repository.CapabilityLevel → allow, matchCommand →
// off). Every test here guards a path that could leave an instance governed by
// no rules at all — a failure mode with no error message and no visible symptom
// until someone drops a table.

type tierRow struct {
	Code            string `json:"code"`
	DisplayName     string `json:"displayName"`
	RequireMFA      bool   `json:"requireMfa"`
	ScanBaseline    bool   `json:"scanBaseline"`
	CountsInPending bool   `json:"countsInPending"`
	ConnLayer       string `json:"connLayer"`
	DefaultRole     string `json:"defaultRole"`
}

type envRow struct {
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	TierCode    string `json:"tierCode"`
}

func (a *testApp) tiers(token string) []tierRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/env-tiers", token, nil)
	eq(a.t, r.Code, 0, "list tiers")
	var out []tierRow
	_ = json.Unmarshal(r.Data, &out)
	return out
}

func (a *testApp) environments(token string) []envRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/environments", token, nil)
	eq(a.t, r.Code, 0, "list environments")
	var out []envRow
	_ = json.Unmarshal(r.Data, &out)
	return out
}

// countRuleRows reports how many capability and dictionary rows are keyed by a
// tier — the two numbers that decide whether that tier is regulated.
func (a *testApp) countRuleRows(tier string) (caps, cmds int64) {
	a.t.Helper()
	db := a.repo.DB()
	db.Model(&model.RoleCapability{}).Where("env = ?", tier).Count(&caps)
	db.Model(&model.RiskCommand{}).Where("env = ?", tier).Count(&cmds)
	return
}

// The four built-in tiers seed with exactly the behaviour that used to be
// hardcoded: only prod forces MFA, counts toward pending, and is the scan
// baseline; conn_layer/default_role match the connEnvMeta map they replaced.
func TestEnvTier_SeedsBuiltinsWithTodaysBehaviour(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	byCode := map[string]tierRow{}
	for _, tr := range app.tiers(token) {
		byCode[tr.Code] = tr
	}
	eq(t, len(byCode), 4, "seeded tier count")

	eq(t, byCode["prod"].RequireMFA, true, "prod forces MFA")
	eq(t, byCode["prod"].ScanBaseline, true, "prod is the script-scan baseline")
	eq(t, byCode["prod"].CountsInPending, true, "prod counts toward pending")
	eq(t, byCode["prod"].ConnLayer, "L1 核心 · 写", "prod layer carried over from connEnvMeta")
	eq(t, byCode["dev"].DefaultRole, "developer", "dev default role carried over from connEnvMeta")

	for _, code := range []string{"gli", "staging", "dev"} {
		eq(t, byCode[code].RequireMFA, false, code+" must not force MFA")
		eq(t, byCode[code].ScanBaseline, false, code+" must not be the scan baseline")
	}

	// Exactly one baseline, always. None means matchCommand finds no rows and
	// every uploaded script scans clean without any error being raised.
	n := 0
	for _, tr := range byCode {
		if tr.ScanBaseline {
			n++
		}
	}
	eq(t, n, 1, "exactly one scan baseline")
}

// The migration moves no data: each built-in tier gets an identically-named
// environment, which is what makes every pre-existing tbl_connection.env value
// already valid.
func TestEnvTier_EnvironmentsMirrorTiersSoNoBackfillIsNeeded(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	for _, e := range app.environments(token) {
		eq(t, e.TierCode, e.Code, "seeded environment "+e.Code+" binds the tier of the same name")
	}
	// The demo fixtures created connections before any of this existed; each one
	// must still resolve to a tier.
	cr := app.do(http.MethodGet, "/api/v1/connections", token, nil)
	eq(t, cr.Code, 0, "list connections")
	var conns []struct {
		Env string `json:"env"`
	}
	_ = json.Unmarshal(cr.Data, &conns)
	if len(conns) == 0 {
		t.Fatal("expected seeded connections to assert on")
	}
	known := map[string]bool{}
	for _, e := range app.environments(token) {
		known[e.Code] = true
	}
	for _, c := range conns {
		if !known[c.Env] {
			t.Errorf("existing connection env %q does not resolve to an environment — the upgrade needed a backfill after all", c.Env)
		}
	}
}

// Creating a tier clones the template's rule rows in the same transaction. A
// tier that exists without them is not a blank slate; it is an environment where
// every capability lookup and every dictionary lookup falls through to allowed.
func TestEnvTier_CreateClonesTemplateRules(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	wantCaps, wantCmds := app.countRuleRows("prod")
	if wantCaps == 0 || wantCmds == 0 {
		t.Fatal("template tier has no rules to clone — fixture problem")
	}

	r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "templateCode": "prod",
		"requireMfa": true, "dangerBanner": true, "countsInPending": true,
		"connLayer": "L1 核心 · 写", "defaultRole": "dba_l2",
	})
	eq(t, r.Code, 0, "create tier from template")

	gotCaps, gotCmds := app.countRuleRows("prod-hk")
	eq(t, gotCaps, wantCaps, "cloned capability rows")
	eq(t, gotCmds, wantCmds, "cloned dictionary rows")

	// And an instance may now be created in an environment on that tier.
	er := app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "prod-hk-1", "displayName": "香港生产集群1", "tierCode": "prod-hk",
	})
	eq(t, er.Code, 0, "create environment on the new tier")
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "hkconn", "engine": "MySQL 8.0", "host": "10.0.0.9:3306",
		"env": "prod-hk-1", "policy": "strict",
	})
	eq(t, cr.Code, 0, "create connection in the new environment")
}

// A tier can only be created from a template that exists. Without this an
// operator could name any string and get an unregulated tier.
func TestEnvTier_RejectsUnknownTemplateAndBadCode(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "ghost", "displayName": "无模板", "templateCode": "nope",
	}); r.Code == 0 {
		t.Error("a tier cloned from a non-existent template must be rejected")
	}
	if _, cmds := app.countRuleRows("ghost"); cmds != 0 {
		t.Error("a rejected tier must leave no rule rows behind")
	}
	for _, bad := range []string{"", "has space", "-leading", "under_score", "点五"} {
		if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
			"code": bad, "displayName": "x", "templateCode": "prod",
		}); r.Code == 0 {
			t.Errorf("code %q must be rejected", bad)
		}
	}

	// Codes are normalised, not rejected, for case and surrounding space — the
	// same lower+trim the connection endpoints have always applied, so "PROD "
	// and "prod" cannot become two tiers with two separate rule sets.
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": " CANARY ", "displayName": "金丝雀", "templateCode": "prod",
	}).Code, 0, "a code differing only in case/space is normalised")
	found := false
	for _, tr := range app.tiers(admin) {
		if tr.Code == "canary" {
			found = true
		}
	}
	if !found {
		t.Error(`" CANARY " should have been stored as "canary"`)
	}
	// …and normalising must not open a duplicate route around the existing tier.
	if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "PROD", "displayName": "dup", "templateCode": "staging",
	}); r.Code == 0 {
		t.Error(`"PROD" must collide with the existing "prod" tier, not create a second one`)
	}
	// Duplicates too — a second create must not overwrite a live tier's rules.
	if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "prod", "displayName": "dup", "templateCode": "staging",
	}); r.Code == 0 {
		t.Error("creating a tier that already exists must be rejected")
	}
}

// Deleting a tier that still backs environments would strand their instances on
// a tier that no longer has rules.
func TestEnvTier_DeleteGuards(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// In use by the identically-named seeded environment.
	if r := app.do(http.MethodDelete, "/api/v1/env-tiers/staging", admin, nil); r.Code == 0 {
		t.Error("a tier still bound by an environment must not be deletable")
	}
	// Holds the scan baseline.
	if r := app.do(http.MethodDelete, "/api/v1/env-tiers/prod", admin, nil); r.Code == 0 {
		t.Error("the scan-baseline tier must not be deletable")
	}

	// A free tier (no environment bound, not the baseline) deletes, taking its
	// rule rows with it.
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "spare", "displayName": "备用", "templateCode": "dev",
	}).Code, 0, "create a spare tier")
	if caps, _ := app.countRuleRows("spare"); caps == 0 {
		t.Fatal("spare tier should have cloned rules")
	}
	eq(t, app.do(http.MethodDelete, "/api/v1/env-tiers/spare", admin, nil).Code, 0, "delete unused tier")
	caps, cmds := app.countRuleRows("spare")
	eq(t, caps, int64(0), "capability rows removed with the tier")
	eq(t, cmds, int64(0), "dictionary rows removed with the tier")
}

// The scan baseline is single-valued: moving it clears the previous holder, and
// it can never be switched off outright.
func TestEnvTier_ScanBaselineStaysUniqueAndPresent(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// Move the baseline to gli.
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/gli", admin, map[string]any{
		"displayName": "灰度 · GLI", "scanBaseline": true, "connLayer": "L2 灰度", "defaultRole": "dba_l2",
	}).Code, 0, "move baseline to gli")

	n := 0
	for _, tr := range app.tiers(admin) {
		if tr.ScanBaseline {
			n++
		}
	}
	eq(t, n, 1, "still exactly one baseline after moving it")

	// Turning the only baseline off is refused — script scanning would silently
	// report every statement as safe.
	if r := app.do(http.MethodPut, "/api/v1/env-tiers/gli", admin, map[string]any{
		"displayName": "灰度 · GLI", "scanBaseline": false,
	}); r.Code == 0 {
		t.Error("clearing the last scan baseline must be refused")
	}
}

// Adding an environment to an existing tier copies nothing — that is the whole
// point of the split — and the instances in it are governed from the first
// second by the tier's existing rules.
func TestEnvironment_CreateBindsExistingTierWithoutCloning(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	beforeCaps, beforeCmds := app.countRuleRows("prod")
	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "prod-sh", "displayName": "上海生产", "tierCode": "prod",
	}).Code, 0, "create environment on prod")
	afterCaps, afterCmds := app.countRuleRows("prod")
	eq(t, afterCaps, beforeCaps, "no capability rows added")
	eq(t, afterCmds, beforeCmds, "no dictionary rows added")

	// An environment must bind a tier that exists.
	if r := app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "orphan", "displayName": "孤儿", "tierCode": "nope",
	}); r.Code == 0 {
		t.Error("an environment on a non-existent tier must be rejected")
	}
}

// Deleting an environment moves its instances first, atomically. An instance
// left pointing at a deleted code resolves to no tier and therefore no rules.
func TestEnvironment_DeleteMovesInstances(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "prod-sh", "displayName": "上海生产", "tierCode": "prod",
	}).Code, 0, "create environment")
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "shconn", "engine": "MySQL 8.0", "host": "10.0.0.8:3306",
		"env": "prod-sh", "policy": "strict",
	})
	eq(t, cr.Code, 0, "create connection in it")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)

	// A delete without a move target is refused.
	if r := app.do(http.MethodDelete, "/api/v1/environments/prod-sh", admin, map[string]any{"moveTo": ""}); r.Code == 0 {
		t.Error("deleting an environment without a move target must be refused")
	}
	// So is moving to itself.
	if r := app.do(http.MethodDelete, "/api/v1/environments/prod-sh", admin, map[string]any{"moveTo": "prod-sh"}); r.Code == 0 {
		t.Error("moving instances to the environment being deleted must be refused")
	}

	eq(t, app.do(http.MethodDelete, "/api/v1/environments/prod-sh", admin,
		map[string]any{"moveTo": "prod"}).Code, 0, "delete with a move target")

	var moved model.Connection
	if err := app.repo.DB().First(&moved, conn.ID).Error; err != nil {
		t.Fatalf("reload connection: %v", err)
	}
	eq(t, moved.Env, "prod", "instance moved to the target environment")

	for _, e := range app.environments(admin) {
		if e.Code == "prod-sh" {
			t.Error("environment should be gone")
		}
	}
}

// Connections may only be stored against an environment that resolves — the ED5
// guarantee, now enforced from the table rather than a hardcoded whitelist.
func TestEnvironment_UnknownCodeStillRejectedOnConnections(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	for _, bad := range []string{"uat", "pre", "production", ""} {
		r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
			"name": "x" + bad, "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
			"env": bad, "policy": "strict",
		})
		if r.Code == 0 {
			t.Errorf("env %q must be rejected — it would be an instance with no rules", bad)
		}
	}
	// Deleting an environment must close the door behind it: instances can no
	// longer be created against a code that no longer resolves to a tier.
	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "temp-env", "displayName": "临时", "tierCode": "dev",
	}).Code, 0, "create a temporary environment")
	eq(t, app.do(http.MethodDelete, "/api/v1/environments/temp-env", admin,
		map[string]any{"moveTo": "dev"}).Code, 0, "delete it again")
	if r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ghostconn", "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
		"env": "temp-env", "policy": "strict",
	}); r.Code == 0 {
		t.Error("a deleted environment must no longer accept new instances")
	}
}

// Reads are open (the terminal renders from them); every mutation is admin-only.
func TestEnvTier_MutationsAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("zhangwei@vela.io", "vela123")

	if r := app.do(http.MethodGet, "/api/v1/env-tiers", owner, nil); r.Code != 0 {
		t.Error("a non-admin must be able to READ tiers — the console renders from them")
	}
	if r := app.do(http.MethodGet, "/api/v1/environments", owner, nil); r.Code != 0 {
		t.Error("a non-admin must be able to READ environments")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", owner, map[string]any{
		"code": "sneaky", "displayName": "x", "templateCode": "dev",
	}).Code, 40300, "non-admin must not create a tier")
	eq(t, app.do(http.MethodPost, "/api/v1/environments", owner, map[string]any{
		"code": "sneaky", "displayName": "x", "tierCode": "dev",
	}).Code, 40300, "non-admin must not create an environment")
	eq(t, app.do(http.MethodDelete, "/api/v1/environments/dev", owner,
		map[string]any{"moveTo": "prod"}).Code, 40300, "non-admin must not delete an environment")
}
