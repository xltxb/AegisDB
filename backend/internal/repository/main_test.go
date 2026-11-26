package repository

import (
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/testpg"
)

func TestMain(m *testing.M) { testpg.Main(m) }

// openStore gives the test its own PostgreSQL store holding the production
// schema (migrations/pg), with no seed data. The pool is closed before the
// database is dropped (cleanups run LIFO).
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
