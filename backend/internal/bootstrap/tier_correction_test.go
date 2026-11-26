package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// The five built-in tiers, and what each one MEANS. Two were mislabelled until
// 2026-08-06 — gli was seeded as 灰度 (grey release) when it is 法务 (legal), and
// staging as 演练UAT when it is 预发布 — and 演练 had no tier at all.
//
// The distinction these tests protect: an environment's NAME never says which
// kind of environment it is. The tier does, and the rules hang off the tier. So
// the correction is a relabelling plus one new tier; not a single rule row or
// connection moves, and no verdict changes.

func TestBuiltinTiers_MeanWhatTheySay(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	byCode := map[string]tierRow{}
	for _, tr := range app.tiers(token) {
		byCode[tr.Code] = tr
	}
	eq(t, len(byCode), 5, "seeded tier count")

	for code, want := range map[string]string{
		"prod": "生产环境", "uat": "演练环境", "gli": "法务环境",
		"dev": "开发环境", "staging": "预发布环境",
	} {
		if got := byCode[code].DisplayName; !strings.Contains(got, want) {
			t.Errorf("tier %q display name = %q, want it to say %q", code, got, want)
		}
	}
}

// uat is regulated from the moment it exists. A tier without rule rows is not a
// blank slate: both lookups read a missing row as permission granted.
func TestBuiltinTiers_UatIsGovernedLikeStaging(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	wantCaps, wantCmds := app.countRuleRows("staging")
	gotCaps, gotCmds := app.countRuleRows("uat")
	eq(t, gotCaps, wantCaps, "uat capability rows mirror staging")
	eq(t, gotCmds, wantCmds, "uat dictionary rows mirror staging")

	// End to end: an instance in the uat environment is gated, not waved through.
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "uat-orders", "engine": "MySQL 8.0", "host": "10.60.0.1:3306",
		"env": "uat", "policy": "approve-1",
	})
	eq(t, cr.Code, 0, "create a uat instance")
	var conn struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(cr.Data, &conn); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	v := app.riskCheck(token, conn.ID, "DROP TABLE orders;")
	if !v.RequiresApproval {
		t.Error("a DROP on a uat instance must be gated — an ungoverned tier is the failure this guards")
	}
}

// The seeded tiers are a starting point, not fixtures: once an operator deletes
// one it stays deleted. Both boot paths used to bring it back — the tier row
// (re-created as a "missing built-in") and its rules (re-mirrored from staging,
// leaving rule rows that belong to no tier).
func TestBuiltinTiers_DeletedTierStaysDeleted(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	for _, code := range []string{"gli", "uat"} {
		db.Where("env = ?", code).Delete(&model.Connection{})
		db.Where("code = ?", code).Delete(&model.Environment{})
		if err := app.repo.DeleteEnvTier(code); err != nil {
			t.Fatalf("delete tier %s: %v", code, err)
		}
	}

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := Seed(app.repo, app.cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, code := range []string{"gli", "uat"} {
		var n int64
		db.Model(&model.EnvTier{}).Where("code = ?", code).Count(&n)
		eq(t, n, int64(0), code+" tier after restart")
		db.Model(&model.Environment{}).Where("code = ?", code).Count(&n)
		eq(t, n, int64(0), code+" environment after restart")
		caps, cmds := app.countRuleRows(code)
		if caps != 0 || cmds != 0 {
			t.Errorf("%s left orphan rule rows: capability=%d dictionary=%d", code, caps, cmds)
		}
	}
	// The tiers that were not touched keep their rules.
	if caps, cmds := app.countRuleRows("staging"); caps == 0 || cmds == 0 {
		t.Errorf("staging lost its rules: capability=%d dictionary=%d", caps, cmds)
	}
}

// A tier an operator has renamed keeps their name. Fixing our own mislabelling is
// not a licence to overwrite a deliberate edit.
func TestBuiltinTiers_UpgradeKeepsAnOperatorsOwnName(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	db.Model(&model.EnvTier{}).Where("code = ?", "gli").
		Update("display_name", "法务合规 · 华东")

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var gli model.EnvTier
	db.First(&gli, "code = ?", "gli")
	eq(t, gli.DisplayName, "法务合规 · 华东", "an operator's own tier name survives the correction")
}
