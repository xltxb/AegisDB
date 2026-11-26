package service

import (
	"database/sql"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
	"aegisdb/internal/testpg"
)

func TestMain(m *testing.M) { testpg.Main(m) }

// openStore gives the test its own gateway store on the throwaway PostgreSQL
// instance: the production schema (migrations/pg), no seed data. The pool is
// closed before the database is dropped (cleanups run LIFO).
func openStore(t *testing.T) *gorm.DB {
	t.Helper()
	_, dsn := testpg.MigratedDB(t)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	return db
}

// pgTarget returns a connection to a fresh, empty database on the throwaway
// PostgreSQL instance; the database is dropped when the test ends. Every call
// yields a distinct database name, so the gateway's per-DSN pool cache never
// shares a handle between tests.
func pgTarget(t *testing.T) *model.Connection {
	t.Helper()
	c := testpg.Server(t)
	name, _ := testpg.NewDB(t)
	return &model.Connection{Engine: "postgresql", Host: c.Host, Port: c.Port,
		Username: c.User, Password: c.Password, Database: name}
}

// pgExec runs setup statements directly on the target database, bypassing the gateway.
func pgExec(t *testing.T, conn *model.Connection, stmts ...string) {
	t.Helper()
	db, err := sql.Open("pgx", testpg.Server(t).DSN(conn.Database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}
