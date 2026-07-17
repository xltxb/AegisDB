package bootstrap

import (
	"path/filepath"
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// Seeding must be idempotent for additive data: a database created before the
// schema tree existed should get it backfilled on the next boot, without
// deleting the DB. This guards against the "all-or-nothing" seed guard silently
// skipping new seed data on existing installations.
func TestSeed_BackfillsSchemaOnExistingDB(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "seed-test.db")
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
	if repo.Count(&model.SchemaObject{}) == 0 {
		t.Fatal("fresh seed should create schema objects")
	}

	// Simulate a DB created BEFORE schema seeding existed: drop schema rows while
	// keeping roles/connections intact.
	if err := repo.DB().Where("1 = 1").Delete(&model.SchemaObject{}).Error; err != nil {
		t.Fatalf("wipe schema: %v", err)
	}
	if repo.Count(&model.SchemaObject{}) != 0 {
		t.Fatal("precondition: schema should be empty after wipe")
	}

	// Re-seed: roles already exist (fresh path skipped), but the schema must be
	// backfilled idempotently.
	if err := Seed(repo, cfg); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if repo.Count(&model.SchemaObject{}) == 0 {
		t.Error("expected schema to be backfilled on an existing DB after re-seed")
	}
}
