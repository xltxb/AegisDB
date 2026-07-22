package bootstrap

import (
	"path/filepath"
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// Production init: migrate + reference data + admin only (no demo users/connections),
// and re-running is idempotent (resets the admin password, no duplicate).
func TestInitDatabase_CreatesAdminAndReferenceOnly(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "init.db")
	cfg.Database.AutoMigrate = true
	cfg.Gateway.DefaultPolicy = "strict"

	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close() // release the sqlite file so TempDir cleanup can delete it (Windows)
		}
	}()
	repo := repository.New(db)

	if err := InitDatabase(repo, cfg, "ops@corp.io", "S3cret!Passw0rd", "Ops Admin"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// reference data present, but NO demo users/connections
	if n := repo.Count(&model.Role{}); n != 5 {
		t.Errorf("expected 5 roles, got %d", n)
	}
	if repo.Count(&model.RiskCommand{}) == 0 {
		t.Error("risk dictionary not seeded")
	}
	if repo.Count(&model.RoleMenu{}) == 0 || repo.Count(&model.RoleCapability{}) == 0 {
		t.Error("menus/capabilities not seeded")
	}
	if repo.Count(&model.Setting{}) == 0 {
		t.Error("settings not seeded")
	}
	if n := repo.Count(&model.Connection{}); n != 0 {
		t.Errorf("prod init must not seed demo connections, got %d", n)
	}
	if n := repo.Count(&model.User{}); n != 1 {
		t.Fatalf("expected exactly 1 user (the admin), got %d", n)
	}

	// admin has the admin role + the given password
	adminRole, err := repo.GetRoleByCode("admin")
	if err != nil {
		t.Fatalf("admin role: %v", err)
	}
	u, err := repo.GetUserByEmail("ops@corp.io")
	if err != nil {
		t.Fatalf("admin user not found: %v", err)
	}
	if u.RoleID != adminRole.ID {
		t.Errorf("admin not assigned the admin role")
	}
	if !crypto.CheckPassword(u.PasswordHash, "S3cret!Passw0rd") {
		t.Error("admin password not set correctly")
	}

	// re-run: idempotent (no dup) and resets the password
	if err := InitDatabase(repo, cfg, "ops@corp.io", "N3w!Secret9xyz", ""); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	if n := repo.Count(&model.User{}); n != 1 {
		t.Errorf("re-init duplicated the admin, got %d users", n)
	}
	u2, _ := repo.GetUserByEmail("ops@corp.io")
	if !crypto.CheckPassword(u2.PasswordHash, "N3w!Secret9xyz") {
		t.Error("re-init did not reset the admin password")
	}
}
