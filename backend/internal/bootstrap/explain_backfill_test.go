package bootstrap

import (
	"testing"

	"velagateway/internal/model"
)

// `explain` became a capability dimension of its own after these databases were
// installed. A missing capability row reads as `allow`, so nothing behaves
// differently either way — what differs is what the permissions page can show.
// Without the rows it renders the default instead of a stored value, and an
// administrator cannot tell "nobody has decided this" from "somebody chose
// allow".

func explainRows(t *testing.T, app *testApp) map[string]string {
	t.Helper()
	var rows []model.RoleCapability
	if err := app.repo.DB().Where("capability = ?", "explain").Find(&rows).Error; err != nil {
		t.Fatalf("read explain rows: %v", err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[itoa(r.RoleID)+"/"+r.TierCode] = r.Level
	}
	return out
}

func TestExplainBackfill_FreshInstallHasARowForEveryRoleAndTier(t *testing.T) {
	app := newTestApp(t)

	var roles, tiers int64
	app.repo.DB().Model(&model.Role{}).Count(&roles)
	app.repo.DB().Model(&model.EnvTier{}).Count(&tiers)
	if roles == 0 || tiers == 0 {
		t.Fatalf("fixture: roles=%d tiers=%d", roles, tiers)
	}

	got := explainRows(t, app)
	if int64(len(got)) != roles*tiers {
		t.Errorf("explain rows = %d, want one per role×tier (%d)", len(got), roles*tiers)
	}
	for k, lvl := range got {
		if lvl != model.LevelAllow {
			t.Errorf("%s seeded %q — the dimension exists to be tightened, not to tighten anything by itself", k, lvl)
		}
	}
}

// The upgrade path: a database installed before the dimension existed.
func TestExplainBackfill_UpgradeWritesTheMissingRows(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	if err := db.Where("capability = ?", "explain").Delete(&model.RoleCapability{}).Error; err != nil {
		t.Fatalf("clear explain rows: %v", err)
	}
	if len(explainRows(t, app)) != 0 {
		t.Fatal("fixture: rows should be gone")
	}

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(explainRows(t, app)) == 0 {
		t.Error("after migrate the permissions page still has nothing to show")
	}
}

// A level an operator has set must survive every restart.
func TestExplainBackfill_DoesNotOverwriteAnOperatorsChoice(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	var role model.Role
	if err := db.First(&role, "code = ?", "ro").Error; err != nil {
		t.Fatalf("read role: %v", err)
	}
	if err := db.Model(&model.RoleCapability{}).
		Where("role_id = ? AND capability = ? AND tier_code = ?", role.ID, "explain", "prod").
		Update("level", model.LevelDeny).Error; err != nil {
		t.Fatalf("deny explain: %v", err)
	}

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var row model.RoleCapability
	if err := db.First(&row, "role_id = ? AND capability = ? AND tier_code = ?", role.ID, "explain", "prod").Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	eq(t, row.Level, model.LevelDeny, "a deliberate deny must survive the backfill")
}

// A tier created afterwards gets its rows from the template clone, not from a
// later boot — creating a tier must leave it governed immediately.
func TestExplainBackfill_ANewTierGetsExplainRowsFromItsTemplate(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do("POST", "/api/v1/env-tiers", admin, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "templateCode": "prod",
	}).Code, 0, "create tier")

	var n int64
	app.repo.DB().Model(&model.RoleCapability{}).
		Where("capability = ? AND tier_code = ?", "explain", "prod-hk").Count(&n)
	if n == 0 {
		t.Error("a new tier has no explain rows — the clone is what makes a tier governed from the start")
	}
}
