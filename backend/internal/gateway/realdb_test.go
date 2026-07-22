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

	// lib/pq has no sslmode=prefer, so we start with require (TLS) and dialPool
	// falls back to disable when the server has no SSL.
	pgConn := &model.Connection{Engine: "postgres", Host: "pg.internal", Port: 5432, Username: "u", Password: "p", Database: "app"}
	if driver, dsn, ok := engineDriver(pgConn); !ok || driver != "postgres" || !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("postgres DSN should use sslmode=require, got driver=%q dsn=%q ok=%v", driver, dsn, ok)
	}
}

// isPgNoSSL must recognize the "server has no SSL" error so the plaintext fallback
// only triggers for that case (not for auth/network failures).
func TestIsPgNoSSL(t *testing.T) {
	if !isPgNoSSL(errString("pq: SSL is not enabled on the server")) {
		t.Error("expected the no-SSL error to be detected")
	}
	if isPgNoSSL(errString("pq: password authentication failed for user \"u\"")) {
		t.Error("an auth error must not be treated as a no-SSL error")
	}
	if isPgNoSSL(nil) {
		t.Error("nil error is not a no-SSL error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// A PostgreSQL connection with no configured database must default to "postgres",
// not fall through to libpq's user-name default ("database <user> does not exist").
func TestEngineDriver_PostgresDefaultsDatabase(t *testing.T) {
	conn := &model.Connection{Engine: "PostgreSQL 15", Host: "pg.internal", Port: 5432, Username: "dbadmin", Password: "p"} // no Database
	_, dsn, ok := engineDriver(conn)
	if !ok {
		t.Fatal("expected real-exec support with credentials")
	}
	if !strings.Contains(dsn, "dbname='postgres'") {
		t.Errorf("empty database should default to postgres, got dsn=%q", dsn)
	}
	if strings.Contains(dsn, "dbname='dbadmin'") {
		t.Errorf("must not fall back to the username as the database, got dsn=%q", dsn)
	}
}
