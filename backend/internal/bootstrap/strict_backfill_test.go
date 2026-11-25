package bootstrap

import (
	"testing"

	"velagateway/internal/model"
)

// The column default (TRUE) reproduces the shipped behaviour on upgrade, but it
// cannot express the deployment that deliberately set gateway.strict_mode to
// false: SQL cannot read a YAML file. That case is folded in by
// backfillStrictNoWhere, and it must run EXACTLY ONCE — re-applying it on every
// boot would wipe an operator's later decision to switch a tier back on, so the
// switch would look editable and silently refuse to stay edited.
func TestStrictBackfill_FoldsLegacyOffAndOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// The harness boots with StrictMode=false, so the fold has already run and
	// stamped its guard. Undo its effect the way an operator would, then prove a
	// second run does not undo the operator.
	if err := db.Model(&model.EnvTier{}).Where("code = ?", "prod").
		Update("strict_nowhere", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillStrictNoWhere(db, false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var prod model.EnvTier
	if err := db.First(&prod, "code = ?", "prod").Error; err != nil {
		t.Fatal(err)
	}
	if !prod.StrictNoWhere {
		t.Error("re-running the fold wiped a tier the operator had switched back on")
	}
}

// On a database that never carried the legacy switch as "off", the fold writes
// nothing: every tier keeps whatever the column default gave it.
func TestStrictBackfill_LeavesTiersAloneWhenLegacyWasOn(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// Clear the guard so the fold runs again, this time as a legacy-ON install.
	if err := db.Where("k = ?", strictBackfillGuard).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.EnvTier{}).Where("code = ?", "dev").
		Update("strict_nowhere", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillStrictNoWhere(db, true); err != nil {
		t.Fatalf("legacy-on fold: %v", err)
	}
	var dev model.EnvTier
	if err := db.First(&dev, "code = ?", "dev").Error; err != nil {
		t.Fatal(err)
	}
	if !dev.StrictNoWhere {
		t.Error("a legacy-ON install must not have any tier switched off by the fold")
	}
}
