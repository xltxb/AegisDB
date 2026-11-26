package bootstrap

import (
	"database/sql"
	"testing"

	"aegisdb/internal/testpg"
)

func TestMain(m *testing.M) { testpg.Main(m) }

// pgExecDB runs setup statements directly on a target database (by name),
// bypassing the gateway.
func pgExecDB(t testing.TB, database string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("pgx", testpg.Server(t).DSN(database))
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

// newTargetDB creates a fresh, empty database on the throwaway PostgreSQL
// instance (dropped when the test ends), optionally seeded with stmts, and
// returns its name.
func newTargetDB(t testing.TB, stmts ...string) string {
	t.Helper()
	name, _ := testpg.NewDB(t)
	if len(stmts) > 0 {
		pgExecDB(t, name, stmts...)
	}
	return name
}

// targetConnBody is the POST /api/v1/connections body for a PostgreSQL target
// living in the database `database` of the throwaway instance.
func targetConnBody(t testing.TB, name, env, database string) map[string]any {
	t.Helper()
	c := testpg.Server(t)
	return map[string]any{
		"name": name, "engine": "postgresql", "host": c.Host, "port": c.Port,
		"username": c.User, "password": c.Password, "database": database,
		"env": env, "policy": "audit-only", "defaultRole": "dba_l2",
	}
}

// newPGConn creates a fresh target database (seeded with stmts) and registers
// it through POST /api/v1/connections; it returns the connection id and the
// database name.
func (a *testApp) newPGConn(token, name, env string, stmts ...string) (int64, string) {
	a.t.Helper()
	db := newTargetDB(a.t, stmts...)
	return a.pgConnAt(token, env, name, db), db
}
