package sqlmigrate_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/sqlmigrate"
	"aegisdb/internal/testpg"
)

func TestMain(m *testing.M) { testpg.Main(m) }

// openDB gives a gorm handle on a fresh, empty database of the throwaway
// PostgreSQL instance, for runner mechanics tests.
func openDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	_, dsn := testpg.NewDB(t)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	return db, dsn
}

func twoMigrations() fstest.MapFS {
	return fstest.MapFS{
		"0001_a.sql": {Data: []byte(`-- comment
CREATE DATABASE IF NOT EXISTS ignored;
USE ignored;
CREATE TABLE IF NOT EXISTS t_alpha (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_beta (id INTEGER PRIMARY KEY);`)},
	}
}

func TestRun_AppliesAndIsIdempotent(t *testing.T) {
	fsys := twoMigrations()
	db, _ := openDB(t)
	if err := sqlmigrate.Run(db, fsys); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Both migrations recorded.
	var count int64
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", count)
	}
	// Tables exist (CREATE DATABASE/USE were skipped, so this ran against the
	// connected database, not a phantom "ignored" schema).
	if !db.Migrator().HasTable("t_alpha") || !db.Migrator().HasTable("t_beta") {
		t.Fatal("expected t_alpha and t_beta to exist")
	}

	// Second run is a no-op (idempotent) — no error, still exactly 2 rows.
	if err := sqlmigrate.Run(db, fsys); err != nil {
		t.Fatalf("second run: %v", err)
	}
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected still 2 applied migrations after re-run, got %d", count)
	}
}

// On PostgreSQL the runner holds a session advisory lock for the whole run, so
// a second migrator waits instead of applying the same versions concurrently.
func TestRun_WaitsForThePGMigrationLock(t *testing.T) {
	db, dsn := openDB(t)
	ctx := context.Background()

	holder, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", sqlmigrate.PGMigrationLockKey); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- sqlmigrate.Run(db, twoMigrations()) }()
	select {
	case err := <-done:
		t.Fatalf("Run finished while another session held the migration lock (err=%v)", err)
	case <-time.After(1500 * time.Millisecond):
	}
	if db.Migrator().HasTable("t_alpha") {
		t.Fatal("Run applied a migration while another session held the migration lock")
	}

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", sqlmigrate.PGMigrationLockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after the lock was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not proceed after the lock was released")
	}
	if !db.Migrator().HasTable("t_alpha") || !db.Migrator().HasTable("t_beta") {
		t.Fatal("expected t_alpha and t_beta to exist")
	}
}

func TestSplitSQLStatements_SkipsCommentsAndScoping(t *testing.T) {
	stmts := sqlmigrate.SplitSQLStatements(`-- header comment
CREATE DATABASE IF NOT EXISTS aegisdb;
USE aegisdb;
CREATE TABLE a (id INT);
CREATE TABLE b (id INT);`)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements (DATABASE/USE/comments skipped), got %d: %v", len(stmts), stmts)
	}
}
