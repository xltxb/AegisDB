package bootstrap

import (
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openSQLite gives a throwaway sqlite gorm handle for runner mechanics tests
// (dialect-agnostic DDL only).
func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mig-test.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	return db
}

func TestRunSQLMigrations_AppliesAndIsIdempotent(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`-- comment
CREATE DATABASE IF NOT EXISTS ignored;
USE ignored;
CREATE TABLE IF NOT EXISTS t_alpha (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_beta (id INTEGER PRIMARY KEY);`)},
	}

	db := openSQLite(t)
	if err := RunSQLMigrations(db, fsys); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Both migrations recorded.
	var count int64
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", count)
	}
	// Tables exist (CREATE DATABASE/USE were skipped, so this ran against the
	// connected sqlite db, not a phantom "ignored" schema).
	if !db.Migrator().HasTable("t_alpha") || !db.Migrator().HasTable("t_beta") {
		t.Fatal("expected t_alpha and t_beta to exist")
	}

	// Second run is a no-op (idempotent) — no error, still exactly 2 rows.
	if err := RunSQLMigrations(db, fsys); err != nil {
		t.Fatalf("second run: %v", err)
	}
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected still 2 applied migrations after re-run, got %d", count)
	}
}

func TestSplitSQLStatements_SkipsCommentsAndScoping(t *testing.T) {
	stmts := splitSQLStatements(`-- header comment
CREATE DATABASE IF NOT EXISTS vela_gateway;
USE vela_gateway;
CREATE TABLE a (id INT);
CREATE TABLE b (id INT);`)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements (DATABASE/USE/comments skipped), got %d: %v", len(stmts), stmts)
	}
}
