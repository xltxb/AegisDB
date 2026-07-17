package gateway

import (
	"path/filepath"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// SQLite target connections must NOT be pooled/cached — a cached handle would
// keep the file open and break temp-dir cleanup. Each openConn returns a fresh
// handle and release() closes it (L3 safety property).
func TestOpenConn_SqliteNotCached(t *testing.T) {
	conn := &model.Connection{Engine: "sqlite", Database: filepath.Join(t.TempDir(), "c.db")}
	db1, rel1, err := openConn(conn)
	if err != nil {
		t.Fatalf("open #1: %v", err)
	}
	defer rel1()
	db2, rel2, err := openConn(conn)
	if err != nil {
		t.Fatalf("open #2: %v", err)
	}
	defer rel2()
	if db1 == db2 {
		t.Error("sqlite handles must be distinct (not cached)")
	}
}

// Networked target connections must prefer TLS so credentials/results aren't
// sent in the clear when the server supports encryption (low-risk hardening).
func TestEngineDriver_PrefersTLS(t *testing.T) {
	mysqlConn := &model.Connection{Engine: "mysql", Host: "db.internal", Port: 3306, Username: "u", Password: "p", Database: "app"}
	if driver, dsn, ok := engineDriver(mysqlConn); !ok || driver != "mysql" || !strings.Contains(dsn, "tls=preferred") {
		t.Errorf("mysql DSN should request tls=preferred, got driver=%q dsn=%q ok=%v", driver, dsn, ok)
	}

	pgConn := &model.Connection{Engine: "postgres", Host: "pg.internal", Port: 5432, Username: "u", Password: "p", Database: "app"}
	if driver, dsn, ok := engineDriver(pgConn); !ok || driver != "postgres" || !strings.Contains(dsn, "sslmode=prefer") {
		t.Errorf("postgres DSN should use sslmode=prefer, got driver=%q dsn=%q ok=%v", driver, dsn, ok)
	}
}
