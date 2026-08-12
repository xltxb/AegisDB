package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/pkg/resp"
	"velagateway/pkg/totp"
)

// Judgement used to key off the connection's env string and compare it against
// the literal "prod". These tests pin the replacement: a decision follows the
// control tier resolved through connection → environment → tier, and follows the
// tier's property flags rather than its name.
//
// The failure mode being guarded is uniformly silent. A lookup keyed by a tier
// that does not exist finds no rows, and both rule layers read no rows as
// permission granted — so a broken resolution does not raise anything, it just
// stops governing.

// newProdLikeTier creates a tier cloned from prod, an environment on it, and an
// instance in that environment. The environment code deliberately differs from
// every tier code: anything still comparing conn.Env to "prod" cannot match it.
func (a *testApp) newProdLikeTier(token, tierCode, envCode, connName string) int64 {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPost, "/api/v1/env-tiers", token, map[string]any{
		"code": tierCode, "displayName": tierCode, "templateCode": "prod",
		"requireMfa": true, "dangerBanner": true, "countsInPending": true,
		"connLayer": "L1 核心 · 写", "defaultRole": "dba_l2",
	}).Code, 0, "create tier "+tierCode)
	eq(a.t, a.do(http.MethodPost, "/api/v1/environments", token, map[string]any{
		"code": envCode, "displayName": envCode, "tierCode": tierCode,
	}).Code, 0, "create environment "+envCode)
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": connName, "engine": "MySQL 8.0", "host": "10.0.0.21:3306",
		"env": envCode, "policy": "strict",
	})
	eq(a.t, r.Code, 0, "create connection "+connName)
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	return c.ID
}

// An instance in a brand-new environment is governed the moment it exists. The
// point of the split: prod-hk gets prod's rules by binding to prod's tier, with
// no rule rows copied and therefore no window in which it is unregulated.
func TestTierJudgement_NewEnvironmentInheritsItsTiersRules(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "tierCode": "prod",
	}).Code, 0, "create environment on the existing prod tier")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "hk-orders", "engine": "MySQL 8.0", "host": "10.0.0.31:3306",
		"env": "prod-hk", "policy": "strict",
	})
	eq(t, r.Code, 0, "create connection in prod-hk")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	// No rule row anywhere mentions "prod-hk"; the dictionary is keyed by tier.
	if caps, cmds := app.countRuleRows("prod-hk"); caps != 0 || cmds != 0 {
		t.Errorf("an environment must not own rule rows (caps=%d cmds=%d)", caps, cmds)
	}
	// It is nonetheless fully governed.
	v := app.riskCheck(admin, c.ID, "DROP TABLE orders;")
	eq(t, v.Risk, "high", "DROP in prod-hk is judged by the prod tier's dictionary")
	if !v.RequiresApproval {
		t.Error("a new production environment must gate DROP from the moment it exists")
	}
}

// A tier created from a template governs its instances immediately — the clone
// is what makes that true, and this asserts it end to end rather than by
// counting rows.
func TestTierJudgement_NewTierGovernsItsInstancesImmediately(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.newProdLikeTier(admin, "prod-hk", "hk-cluster-1", "hk-billing")

	v := app.riskCheck(admin, conn, "DROP TABLE orders;")
	eq(t, v.Risk, "high", "DROP on the new tier is high")
	if !v.RequiresApproval {
		t.Error("an instance on a cloned production tier must be gated, not waved through")
	}

	// The dictionary is only the second layer. Deny the caller's own role on the
	// new TIER and the same instance must become unreachable — which is only true
	// if the capability lookup is keyed by the tier resolved from the instance's
	// environment ("hk-cluster-1") rather than by that environment itself.
	prod := app.connIDByEnv(admin, "prod")
	eq(t, app.riskCheck(admin, prod, "SELECT 1").Action, "allow", "SELECT on prod allowed before the change")
	eq(t, app.riskCheck(admin, conn, "SELECT 1").Action, "allow", "SELECT on the new tier allowed before the change")

	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(app.roleIDByCode(admin, "admin"))+"/capabilities", admin,
		map[string]any{"matrix": map[string]map[string]string{"select": {"prod-hk": "deny"}}}).Code, 0,
		"deny select on the prod-hk tier")

	eq(t, app.riskCheck(admin, conn, "SELECT 1").Action, "deny", "the capability matrix governs the instance by its tier")
	eq(t, app.riskCheck(admin, prod, "SELECT 1").Action, "allow", "and the change is scoped to that tier alone")
}

// MFA step-up follows the tier's RequireMFA flag. The check used to be
// `conn.Env != "prod"`, which silently exempted every additional production
// environment — exactly the instances that most need it.
func TestTierJudgement_MFAFollowsTheFlagNotTheName(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(token)
	conn := app.newProdLikeTier(token, "prod-hk", "hk-cluster-1", "hk-billing")

	exec := func(code string) int {
		body := map[string]any{"connectionId": conn, "sql": "SELECT 1"}
		if code != "" {
			body["mfaCode"] = code
		}
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, body).Code
	}
	eq(t, exec(""), resp.CodeMFARequired, "a RequireMFA tier steps up even though it is not called prod")
	eq(t, exec(totp.Code(secret, time.Now())), 0, "a valid code satisfies it")
}

// …and the converse: turning the flag off stops the step-up, so the property is
// genuinely what is being read.
func TestTierJudgement_MFAStopsWhenTheFlagIsCleared(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")

	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "SELECT 1",
	}).Code, resp.CodeMFARequired, "prod steps up while the flag is set")

	// Clear RequireMFA on the prod tier (it keeps the scan baseline).
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/prod", token, map[string]any{
		"displayName": "生产环境 · PROD", "requireMfa": false, "dangerBanner": true,
		"countsInPending": true, "scanBaseline": true,
		"connLayer": "L1 核心 · 写", "defaultRole": "dba_l2",
	}).Code, 0, "clear requireMfa on prod")

	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "SELECT 1",
	}).Code, 0, "with the flag cleared the same instance no longer steps up")
}

// Regression for the GLI bug. The console sent a hardcoded {prod, staging, dev}
// map and the repository wrote only the keys it was given, so every command an
// operator added was missing its gli row — and a missing row reads as "off".
// The dictionary page looked right while grey-release instances ignored it.
//
// The tier list now comes from the database, so a caller that enumerates tiers
// incompletely (or predates a tier entirely) can no longer leave a hole.
func TestRiskCommands_UpsertReachesEveryTierIncludingOnesTheCallerOmitted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// A tier the caller has never heard of, plus the historically-missed gli.
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "templateCode": "prod",
		"requireMfa": true, "countsInPending": true,
	}).Code, 0, "create an extra tier")

	// Exactly the map the old console sent: gli absent, prod-hk absent.
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "SHUTDOWN",
		"env":     map[string]string{"prod": "high", "staging": "mid", "dev": "off"},
	}).Code, 0, "add a dictionary command")

	for _, tier := range []string{"prod", "gli", "staging", "dev", "prod-hk"} {
		var n int64
		app.repo.DB().Model(&model.RiskCommand{}).
			Where("command = ? AND env = ?", "SHUTDOWN", tier).Count(&n)
		if n != 1 {
			t.Errorf("tier %q has %d rows for SHUTDOWN, want exactly 1 — a missing row reads as off", tier, n)
		}
	}
	// The caller's own values are honoured where it gave them.
	eq2(t, app.riskLevel(admin, "SHUTDOWN", "prod"), "high", "explicit prod level kept")
	eq2(t, app.riskLevel(admin, "SHUTDOWN", "dev"), "off", "explicit dev level kept")
	// An omitted production tier defaults to gated rather than to off, so the
	// command an operator just classified as dangerous cannot run unreviewed.
	eq2(t, app.riskLevel(admin, "SHUTDOWN", "prod-hk"), "high", "omitted production tier defaults to high")
}

// Script scanning judges against the tier holding ScanBaseline. Moving the
// baseline moves the lens — it was hardcoded to PROD.
func TestScriptScan_FollowsTheScanBaselineTier(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	scan := func() (high int, risk string) {
		r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
			"filename": "t.sql", "content": "DROP TABLE orders;",
		})
		eq(app.t, r.Code, 0, "scan script")
		var out struct {
			High       int `json:"high"`
			Statements []struct {
				Risk string `json:"risk"`
			} `json:"statements"`
		}
		_ = json.Unmarshal(r.Data, &out)
		if len(out.Statements) == 0 {
			t.Fatal("scan returned no statements")
		}
		return out.High, out.Statements[0].Risk
	}

	if _, risk := scan(); risk != "high" {
		t.Fatalf("DROP should scan high against the prod baseline, got %q", risk)
	}

	// Give dev its own harmless view of DROP, then make dev the baseline.
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/DROP", admin,
		map[string]any{"env": "dev", "level": "off"}).Code, 0, "set DROP off on dev")
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/dev", admin, map[string]any{
		"displayName": "测试 · DEV", "scanBaseline": true,
		"connLayer": "L4 沙盒", "defaultRole": "developer",
	}).Code, 0, "move the scan baseline to dev")

	high, risk := scan()
	eq(t, risk, "safe", "the scan now uses the dev dictionary")
	eq(t, high, 0, "no high-risk statements under the new baseline")
}

// The negative case, and the reason ScanScript returns an error at all.
//
// With no baseline tier the dictionary lookup is keyed by nothing, matches
// nothing, and reports every statement — DROP TABLE included — as safe. Nothing
// fails; the scanner just stops finding risk. Refusing is the only honest
// answer, matching how an unreadable rule layer is handled elsewhere (ED3).
func TestScriptScan_RefusesRatherThanReportingCleanWithoutABaseline(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// The API will not do this — it is the invariant the API protects. Break it
	// directly to prove the read path does not quietly tolerate the result.
	if err := app.repo.DB().Model(&model.EnvTier{}).
		Where("scan_baseline = ?", true).Update("scan_baseline", false).Error; err != nil {
		t.Fatalf("clear baseline: %v", err)
	}

	r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"filename": "t.sql", "content": "DROP TABLE orders;",
	})
	if r.Code == 0 {
		t.Fatal("a scan with no baseline tier must fail, not return a clean report")
	}

	// Executing the script must fail for the same reason: it scans first, and an
	// all-safe scan is what lets a script run without review.
	er := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": app.connIDByEnv(admin, "prod"),
		"filename":     "t.sql", "content": "DROP TABLE orders;",
	})
	if er.Code == 0 {
		t.Error("script execution must not proceed on an unscannable script")
	}
}

// The pending/hits stat aggregates by the CountsInPending flag. It used to
// filter `tbl_connection.env = 'prod'`, which counted only the environment that
// happens to share its tier's name — a second production environment was
// intercepted correctly but never showed up in the number.
func TestPendingStats_AggregateByAttributeNotByTheProdLiteral(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.newProdLikeTier(admin, "prod-hk", "hk-cluster-1", "hk-billing")

	before := gatewayIntercepts(app, admin)
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": conn, "sql": "DROP TABLE orders;", "reason": "cleanup",
	}).Code, resp.CodeIntercepted, "DROP on the new production tier is intercepted")

	if after := gatewayIntercepts(app, admin); after <= before {
		t.Errorf("an interception on a CountsInPending tier must be counted: before=%d after=%d", before, after)
	}
}

// The connection's display layer and default role are DERIVED from its tier but
// stored on the row, so they drift as soon as the tier moves out from under them
// — which the tier model made possible three new ways. They are recomputed on
// read; this pins that, because the stale value is not obviously wrong on screen,
// it just quietly describes a tier that no longer governs the instance.
func TestTierDefaults_FollowTheTierRatherThanTheStoredCopy(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	layerOf := func(id int64) (string, string) {
		app.t.Helper()
		r := app.do(http.MethodGet, "/api/v1/connections", admin, nil)
		eq(app.t, r.Code, 0, "list connections")
		var conns []struct {
			ID          int64  `json:"id"`
			Layer       string `json:"layer"`
			DefaultRole string `json:"defaultRole"`
		}
		_ = json.Unmarshal(r.Data, &conns)
		for _, c := range conns {
			if c.ID == id {
				return c.Layer, c.DefaultRole
			}
		}
		app.t.Fatalf("connection %d not listed", id)
		return "", ""
	}

	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "tierCode": "prod",
	}).Code, 0, "create prod-hk")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "hk-orders", "engine": "MySQL 8.0", "host": "10.0.0.51:3306",
		"env": "prod-hk", "policy": "strict",
	})
	eq(t, r.Code, 0, "create instance")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	layer, role := layerOf(c.ID)
	eq(t, layer, "L1 核心 · 写", "layer comes from the prod tier")
	eq(t, role, "dba_l2", "default role comes from the prod tier")

	// (1) Editing the tier's own values reaches instances already created.
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/prod", admin, map[string]any{
		"displayName": "生产环境 · PROD", "requireMfa": true, "dangerBanner": true,
		"countsInPending": true, "scanBaseline": true,
		"connLayer": "L1 核心 · 写 (改)", "defaultRole": "dba_l1",
	}).Code, 0, "edit the prod tier")
	layer, role = layerOf(c.ID)
	eq(t, layer, "L1 核心 · 写 (改)", "a tier edit reaches existing instances")
	eq(t, role, "dba_l1", "…including the default role")

	// (2) Rebinding the environment to another tier re-governs the instance.
	eq(t, app.do(http.MethodPut, "/api/v1/environments/prod-hk", admin, map[string]any{
		"displayName": "香港生产", "tierCode": "dev",
	}).Code, 0, "rebind prod-hk to dev")
	layer, role = layerOf(c.ID)
	eq(t, layer, "L4 沙盒", "rebinding the environment changes the instance layer")
	eq(t, role, "developer", "…and its default role")

	// (3) Deleting the environment moves the instance to a tier of its own.
	eq(t, app.do(http.MethodDelete, "/api/v1/environments/prod-hk", admin,
		map[string]any{"moveTo": "staging"}).Code, 0, "delete prod-hk, move to staging")
	layer, _ = layerOf(c.ID)
	eq(t, layer, "L3 预发布", "a moved instance reports the layer of where it landed")
}

// Asking the planner how a write WOULD run is a read. Judging it as the write it
// wraps meant a DBA could not see the plan for a DELETE without first getting the
// approval that the DELETE itself needs — and the plan is what decides whether to
// propose the DELETE at all.
//
// EXPLAIN ANALYZE is the opposite case and must stay gated: in PostgreSQL it
// really performs the statement.
func TestExplain_PlanIsAReadButAnalyzeStillExecutes(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// The statement this came up with: an upsert that is gated on prod.
	upsert := `INSERT INTO t_stats (activity_id, enroll_count) ` +
		`SELECT r.activity_id, COUNT(*) FROM t_record r WHERE r.rebate_time = '2026-08-06' ` +
		`GROUP BY r.activity_id ON DUPLICATE KEY UPDATE enroll_count = VALUES(enroll_count)`

	eq(t, app.riskCheck(token, prod, upsert).Action, "approve", "the write itself is still gated")
	eq(t, app.riskCheck(token, prod, "EXPLAIN "+upsert).Action, "allow", "planning that write is a read")

	// The dictionary layer: DELETE is a dictionary command, but a plan for one
	// deletes nothing.
	eq(t, app.riskCheck(token, prod, "DELETE FROM orders WHERE id = 1").Action, "approve", "a real DELETE is gated")
	eq(t, app.riskCheck(token, prod, "EXPLAIN DELETE FROM orders WHERE id = 1").Action, "allow", "planning a DELETE is a read")

	// Strict mode: a full-table DELETE is the case it exists for; a plan for one
	// still mutates nothing.
	eq(t, app.riskCheck(token, prod, "EXPLAIN DELETE FROM orders").Action, "allow", "planning a full-table DELETE is a read")

	// …and every executing form stays exactly as gated as before.
	for _, sql := range []string{
		"EXPLAIN ANALYZE " + upsert,
		"EXPLAIN ANALYZE DELETE FROM orders",
		"EXPLAIN (ANALYZE) DELETE FROM orders",
		"EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders",
	} {
		if got := app.riskCheck(token, prod, sql).Action; got == "allow" {
			t.Errorf("%q EXECUTES the statement and must not be waved through (got %q)", sql, got)
		}
	}
}

// A role denied SELECT must not be able to read a plan either: a plan exposes
// schema and row-count statistics.
func TestExplain_StillObeysTheReadGate(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")

	eq(t, app.riskCheck(admin, prod, "EXPLAIN SELECT * FROM orders").Action, "allow", "allowed before the change")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(app.roleIDByCode(admin, "admin"))+"/capabilities", admin,
		map[string]any{"matrix": map[string]map[string]string{"select": {"prod": "deny"}}}).Code, 0,
		"deny select on prod")
	eq(t, app.riskCheck(admin, prod, "EXPLAIN SELECT * FROM orders").Action, "deny",
		"a role denied SELECT must not read a plan either")
}

// Reading a plan is its own permission. A plan discloses schema and row-count
// statistics for a statement the role may be forbidden to run, so an estate that
// must control that has to be able to — without also revoking SELECT, which is
// what folding EXPLAIN into `select` forced.
func TestExplain_HasItsOwnCapabilityDimension(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	roleID := itoa(app.roleIDByCode(admin, "admin"))

	// Seeded allow: nothing changes until someone tightens it.
	eq(t, app.riskCheck(admin, prod, "EXPLAIN SELECT * FROM orders").Action, "allow", "plans readable by default")

	// Deny only the plan dimension.
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+roleID+"/capabilities", admin,
		map[string]any{"matrix": map[string]map[string]string{"explain": {"prod": "deny"}}}).Code, 0,
		"deny explain on prod")

	eq(t, app.riskCheck(admin, prod, "EXPLAIN SELECT * FROM orders").Action, "deny", "the plan is now refused")
	eq(t, app.riskCheck(admin, prod, "EXPLAIN DELETE FROM orders").Action, "deny", "…for any statement")
	// …and reading data is untouched, which is the whole point of the split.
	eq(t, app.riskCheck(admin, prod, "SELECT * FROM orders").Action, "allow", "SELECT is unaffected")

	// It is per tier, like every other capability.
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.riskCheck(admin, dev, "EXPLAIN SELECT 1").Action, "allow", "other tiers are unaffected")
}

// Splitting a permission must not hand out what the original one refused.
// `explain` seeds allow everywhere, so if the plan were judged on that dimension
// ALONE, introducing it would have given a role denied SELECT the ability to read
// plans of the tables it may not read. Both gates apply; the stricter wins.
func TestExplain_DenyingSelectStillDeniesThePlan(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	roleID := itoa(app.roleIDByCode(admin, "admin"))

	// explain stays at its seeded allow; only the read gate is closed.
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+roleID+"/capabilities", admin,
		map[string]any{"matrix": map[string]map[string]string{"select": {"prod": "deny"}}}).Code, 0,
		"deny select on prod")

	eq(t, app.riskCheck(admin, prod, "EXPLAIN SELECT * FROM orders").Action, "deny",
		"a role that may not read the data may not read its plan either")
}
