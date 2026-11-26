package gateway

import (
	"database/sql"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/internal/testpg"
)

func TestMain(m *testing.M) { testpg.Main(m) }

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
	c := testpg.Server(t)
	db, err := sql.Open("pgx", c.DSN(conn.Database))
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
