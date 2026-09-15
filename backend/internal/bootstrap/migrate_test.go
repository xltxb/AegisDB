package bootstrap

import (
	"testing"
	"testing/fstest"

	"velagateway/internal/testsupport"
)

func TestRunSQLMigrations_AppliesAndIsIdempotent(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`-- comment
CREATE DATABASE IF NOT EXISTS ignored;
USE ignored;
CREATE TABLE IF NOT EXISTS t_alpha (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_beta (id INTEGER PRIMARY KEY);`)},
	}

	// A dedicated schema, so the runner's own ledger table and the two toy
	// tables below cannot collide with another test's.
	db := testsupport.NewDB(t)
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
	// connected schema, not a phantom "ignored" database).
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
