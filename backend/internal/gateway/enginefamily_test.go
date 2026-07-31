package gateway

import (
	"testing"

	"velagateway/internal/model"
)

// The engine string is free text chosen in the console (and importable from a
// CSV), while the driver that can actually talk to the target is a much smaller
// set. Resolving that mapping in one place keeps it out of an order-sensitive
// chain of substring checks, and makes "which engines can this gateway actually
// drive" a question with a testable answer.
func TestEngineFamily(t *testing.T) {
	cases := map[string]string{
		// MySQL wire protocol. PolarDB is MySQL-compatible, so it belongs here.
		"MySQL 8.0": familyMySQL, "mysql": familyMySQL, "MariaDB": familyMySQL,
		"TiDB": familyMySQL, "tidb 7.1": familyMySQL,
		"PolarDB": familyMySQL, "polardb": familyMySQL, "PolarDB-X": familyMySQL,
		// PostgreSQL wire protocol
		"PostgreSQL 15": familyPostgres, "postgres": familyPostgres,
		"GaussDB (DWS)": familyPostgres, "dws": familyPostgres, "gaussdb": familyPostgres,
		// others
		"Oracle": familyOracle, "oracle 19c": familyOracle,
		"SQLite": familySQLite, "sqlite3": familySQLite,
		// Engines this gateway cannot execute against — they must resolve to no
		// family rather than being quietly attached to a driver that cannot speak
		// their protocol.
		"MongoDB": "", "Redis 7": "", "ClickHouse": "", "": "",
	}
	for engine, want := range cases {
		if got := engineFamily(engine); got != want {
			t.Errorf("engineFamily(%q) = %q, want %q", engine, got, want)
		}
	}
}

// PolarDB must reach the MySQL driver end-to-end, not merely resolve a family.
func TestEngineDriver_PolarDBUsesTheMySQLProtocol(t *testing.T) {
	c := &model.Connection{
		Engine: "PolarDB", Host: "10.0.0.1", Port: 3306,
		Username: "u", Password: "p", Database: "appdb",
	}
	drv, dsn, ok := engineDriver(c)
	if !ok || drv != "mysql" {
		t.Fatalf("PolarDB → driver=%q ok=%v, want mysql", drv, ok)
	}
	if dsn == "" {
		t.Error("expected a MySQL DSN for PolarDB")
	}
}
