package gateway

import (
	"context"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// RealSchema must introspect a live target's tables. We build a throwaway PostgreSQL
// database with two tables and assert the introspection groups them under one database.
func TestRealSchema_PostgreSQLIntrospectsTables(t *testing.T) {
	conn := pgTarget(t)
	conn.Name = "target"
	pgExec(t, conn,
		"CREATE TABLE customers(id serial PRIMARY KEY, name text)",
		"CREATE TABLE orders(id serial PRIMARY KEY, amount real)",
	)
	groups, err := RealSchema(conn)
	if err != nil {
		t.Fatalf("RealSchema: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 database group, got %d", len(groups))
	}
	tables := map[string]bool{}
	// PostgreSQL's tree is database -> schemas -> tables; the seeded tables live in public.
	for _, sc := range groups[0].Schemas {
		if sc.Name != "public" {
			continue
		}
		for _, tb := range sc.Tables {
			tables[tb] = true
		}
	}
	if groups[0].Database != conn.Database || !tables["customers"] || !tables["orders"] {
		t.Errorf("expected customers+orders under public of %s, got %+v", conn.Database, groups[0])
	}
}

// RealRun must return the target's actual result set (columns + rows), not just a
// count — that is what lets the terminal show real data instead of a synthesis.
func TestRealRun_PostgreSQLReturnsResultSet(t *testing.T) {
	conn := pgTarget(t)
	conn.Name = "t"
	pgExec(t, conn,
		"CREATE TABLE t(id integer, name text)",
		"INSERT INTO t VALUES (1,'alice'),(2,'bob')",
	)
	res, err := RealRun(context.Background(), conn, "SELECT id, name FROM t ORDER BY id", 30*time.Second)
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
