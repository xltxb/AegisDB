package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
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
	token := app.login("linwei@vela.io", "vela123")

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
	token := app.login("linwei@vela.io", "vela123")

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

// The upgrade path: a database carrying the OLD labels and no uat tier.
func TestBuiltinTiers_UpgradeRelabelsAndAddsUat(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// Rewind to the pre-correction state.
	db.Model(&model.EnvTier{}).Where("code = ?", "gli").
		Updates(map[string]any{"display_name": "灰度 · GLI", "conn_layer": "L2 灰度"})
	db.Model(&model.EnvTier{}).Where("code = ?", "staging").
		Updates(map[string]any{"display_name": "演练UAT · STAGING", "conn_layer": "L3 演练UAT"})
	db.Model(&model.Environment{}).Where("code = ?", "gli").Update("display_name", "灰度 · GLI")
	db.Where("code = ?", "uat").Delete(&model.EnvTier{})
	db.Where("code = ?", "uat").Delete(&model.Environment{})
	db.Where("tier_code = ?", "uat").Delete(&model.RoleCapability{})
	db.Where("tier_code = ?", "uat").Delete(&model.RiskCommand{})

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var gli, staging, uat model.EnvTier
	db.First(&gli, "code = ?", "gli")
	db.First(&staging, "code = ?", "staging")
	if err := db.First(&uat, "code = ?", "uat").Error; err != nil {
		t.Fatalf("uat tier missing after upgrade: %v", err)
	}
	if !strings.Contains(gli.DisplayName, "法务") {
		t.Errorf("gli still labelled %q", gli.DisplayName)
	}
	if !strings.Contains(staging.DisplayName, "预发布") {
		t.Errorf("staging still labelled %q", staging.DisplayName)
	}
	// The new tier arrives WITH its rules, never bare.
	caps, cmds := app.countRuleRows("uat")
	if caps == 0 || cmds == 0 {
		t.Errorf("uat arrived unregulated: capability rows=%d dictionary rows=%d", caps, cmds)
	}
	// And its environment exists, so instances can actually be filed under it.
	var env model.Environment
	if err := db.First(&env, "code = ?", "uat").Error; err != nil {
		t.Errorf("uat environment missing: %v", err)
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
