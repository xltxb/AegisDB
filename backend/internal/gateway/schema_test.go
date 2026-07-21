package gateway

import (
	"database/sql"
	"path/filepath"
	"testing"

	"velagateway/internal/model"
)

// RealSchema must introspect a live target's tables. We build a throwaway SQLite
// file with two tables and assert the introspection groups them under one database.
func TestRealSchema_SQLiteIntrospectsTables(t *testing.T) {
	dbfile := filepath.Join(t.TempDir(), "target.db")
	sdb, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open target sqlite: %v", err)
	}
	for _, ddl := range []string{
		"CREATE TABLE customers(id INTEGER PRIMARY KEY, name TEXT)",
		"CREATE TABLE orders(id INTEGER PRIMARY KEY, amount REAL)",
	} {
		if _, err := sdb.Exec(ddl); err != nil {
			t.Fatalf("seed target: %v", err)
		}
	}
	sdb.Close()

	conn := &model.Connection{Engine: "SQLite", Database: dbfile, Name: "target"}
	groups, err := RealSchema(conn)
	if err != nil {
		t.Fatalf("RealSchema: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 database group, got %d", len(groups))
	}
	tables := map[string]bool{}
	for _, tb := range groups[0].Tables {
		tables[tb] = true
	}
	if !tables["customers"] || !tables["orders"] {
		t.Errorf("expected customers+orders tables, got %v", groups[0].Tables)
	}
}

// RealRun must return the target's actual result set (columns + rows), not just a
// count — that is what lets the terminal show real data instead of a synthesis.
func TestRealRun_SQLiteReturnsResultSet(t *testing.T) {
	dbfile := filepath.Join(t.TempDir(), "target.db")
	sdb, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open target sqlite: %v", err)
	}
	if _, err := sdb.Exec("CREATE TABLE t(id INTEGER, name TEXT)"); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if _, err := sdb.Exec("INSERT INTO t VALUES (1,'alice'),(2,'bob')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sdb.Close()

	conn := &model.Connection{Engine: "SQLite", Database: dbfile, Name: "t"}
	res, err := RealRun(conn, "SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("RealRun: %v", err)
	}
	if len(res.Columns) != 2 || res.Columns[0] != "id" || res.Columns[1] != "name" {
		t.Errorf("columns = %v, want [id name]", res.Columns)
	}
	if len(res.Data) != 2 || res.Rows != 2 {
		t.Fatalf("expected 2 rows, got Data=%d Rows=%d", len(res.Data), res.Rows)
	}
	if res.Data[0][0] != "1" || res.Data[0][1] != "alice" || res.Data[1][1] != "bob" {
		t.Errorf("unexpected data: %v", res.Data)
	}
}

// A connection without credentials is not real-exec-capable, so RealSchema's caller
// must fall back to the seeded tree rather than attempt a live connect.
func TestRealSchema_NoCredentialsNotSupported(t *testing.T) {
	conn := &model.Connection{Engine: "MySQL 8.0", Host: "10.0.0.1", Port: 3306} // no username
	if RealExecSupported(conn) {
		t.Error("a MySQL connection without a username must not be real-exec-supported")
	}
}
