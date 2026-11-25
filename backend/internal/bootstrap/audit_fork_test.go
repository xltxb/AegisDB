package bootstrap

import (
	"path/filepath"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// A4: the hash chain's integrity must not depend solely on an in-process mutex.
// A UNIQUE index on prev_hash makes a fork impossible at the database layer — two
// rows can never chain onto the same predecessor, even from separate connections
// or processes. Here we drive the repository directly: a second row reusing an
// existing prev_hash must be rejected.
func TestAudit_PrevHashUniquePreventsFork(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "audit.db")
	cfg.Database.AutoMigrate = true

	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)

	mk := func(cmd, prev, hash string) *model.AuditLog {
		return &model.AuditLog{
			OccurredAt: time.Now(), ActorID: 1, ActorName: "t",
			Command: cmd, Risk: "low", Result: "executed", PrevHash: prev, Hash: hash,
		}
	}

	if err := repo.InsertAudit(mk("c1", "", "h1")); err != nil { // genesis
		t.Fatalf("insert genesis: %v", err)
	}
	if err := repo.InsertAudit(mk("c2", "h1", "h2")); err != nil { // chains onto h1
		t.Fatalf("insert second: %v", err)
	}
	// A forking row reusing prev_hash "h1" must be rejected by the unique index.
	if err := repo.InsertAudit(mk("evil", "h1", "h3")); err == nil {
		t.Fatal("expected unique(prev_hash) to reject a fork chaining onto h1, got nil")
	}
}
