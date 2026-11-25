package bootstrap

import (
	"path/filepath"
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// Tiers and environments got a menu of their own ("envtier"). A menu key with no
// RoleMenu row reads as DENIED (middleware.MenuGuard), so on a database that
// predates the key nobody would hold it — including administrators — and the only
// place to grant it is a page that requires it. The upgrade has to grant it, and
// it has to grant it to the right people.

func openSeededDB(t *testing.T) *repository.Repo {
	t.Helper()
	cfg := &Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "envtier-menu.db")
	cfg.Database.AutoMigrate = true
	cfg.Database.Seed = true

	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)
	if err := Seed(repo, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return repo
}

// menuByRole reports which roles have a menu key enabled.
func menuByRole(t *testing.T, repo *repository.Repo, key string) map[int64]bool {
	t.Helper()
	var rows []model.RoleMenu
	if err := repo.DB().Where("menu_key = ?", key).Find(&rows).Error; err != nil {
		t.Fatalf("read menu %q: %v", key, err)
	}
	out := map[int64]bool{}
	for _, r := range rows {
		out[r.RoleID] = r.Enabled
	}
	return out
}

// The upgrade grants envtier to exactly the roles that already hold rules —
// tiers were managed from that menu before they had a page — so nobody gains a
// permission they did not have, and the people already in charge keep working.
func TestEnvTierMenu_BackfilledFromTheRulesMenuOnUpgrade(t *testing.T) {
	repo := openSeededDB(t)

	// Simulate a database written before the key existed.
	if err := repo.DB().Where("menu_key = ?", "envtier").Delete(&model.RoleMenu{}).Error; err != nil {
		t.Fatalf("clear envtier menu: %v", err)
	}
	if len(menuByRole(t, repo, "envtier")) != 0 {
		t.Fatal("fixture: envtier rows should be gone")
	}
	rules := menuByRole(t, repo, "rules")
	if len(rules) == 0 {
		t.Fatal("fixture: expected seeded rules menu rows")
	}

	if err := backfillEnvTiers(repo.DB()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	got := menuByRole(t, repo, "envtier")
	if len(got) != len(rules) {
		t.Fatalf("envtier rows = %d, want one per rules row (%d)", len(got), len(rules))
	}
	for roleID, enabled := range rules {
		if got[roleID] != enabled {
			t.Errorf("role %d: envtier=%v, want it to mirror rules=%v", roleID, got[roleID], enabled)
		}
	}
	// And it is actually granted to somebody — a backfill that mirrors an
	// all-denied state would satisfy the comparison above while still leaving the
	// page unreachable forever.
	granted := false
	for _, enabled := range got {
		granted = granted || enabled
	}
	if !granted {
		t.Error("no role can reach the tiers page after the upgrade")
	}
}

// A deliberate revocation must survive a restart. Re-granting it every boot would
// make the permission impossible to take away.
func TestEnvTierMenu_RevocationIsNotUndoneOnRestart(t *testing.T) {
	repo := openSeededDB(t)

	if err := repo.DB().Model(&model.RoleMenu{}).
		Where("menu_key = ?", "envtier").Update("enabled", false).Error; err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := backfillEnvTiers(repo.DB()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for roleID, enabled := range menuByRole(t, repo, "envtier") {
		if enabled {
			t.Errorf("role %d regained the revoked envtier menu on restart", roleID)
		}
	}
}

// A fresh install seeds the key directly, and it lands where the other
// admin-only menus do rather than being handed to everyone.
func TestEnvTierMenu_SeededAdminOnlyOnAFreshInstall(t *testing.T) {
	repo := openSeededDB(t)

	envtier := menuByRole(t, repo, "envtier")
	if len(envtier) == 0 {
		t.Fatal("fresh install seeded no envtier menu rows")
	}
	// It tracks the other platform-admin menus exactly — same roles, same answers.
	for roleID, enabled := range menuByRole(t, repo, "perms") {
		if envtier[roleID] != enabled {
			t.Errorf("role %d: envtier=%v but perms=%v — tier management must stay platform-admin only",
				roleID, envtier[roleID], enabled)
		}
	}
}
