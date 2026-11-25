package bootstrap

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
)

// chainRow is the audit projection needed to verify hash-chain linkage.
type chainRow struct {
	ID       int64  `json:"id"`
	Command  string `json:"command"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

// auditChain fetches GET /audit and returns rows in insertion order (id asc).
func (a *testApp) auditChain(token string) []chainRow {
	a.t.Helper()
	var rows []chainRow
	if err := json.Unmarshal(a.auditItemsRaw(token, ""), &rows); err != nil {
		a.t.Fatalf("audit decode: %v", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// assertLinked checks the append-only invariant: every row's prevHash equals the
// previous row's hash, the first row links to the empty seed, and no hash is blank.
func assertLinked(t *testing.T, rows []chainRow) {
	t.Helper()
	prev := ""
	for _, row := range rows {
		if row.Hash == "" {
			t.Errorf("audit row %d has an empty hash", row.ID)
		}
		eq(t, row.PrevHash, prev, "prevHash linkage for audit row "+itoa(row.ID))
		prev = row.Hash
	}
}

// US#44 / FR-AUDIT: the audit log is an append-only hash chain. Each row links to
// the previous via prevHash==hash, and appending new commands extends the chain
// without breaking earlier links.
func TestAuditChain_AppendOnlyLinkageHolds(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	seeded := app.auditChain(token)
	if len(seeded) < 2 {
		t.Fatalf("expected a seeded multi-row audit chain, got %d rows", len(seeded))
	}
	assertLinked(t, seeded)

	// Append two new commands through the gateway (allow path → executed + audited).
	devConn := app.connIDByEnv(token, "dev")
	for _, sql := range []string{"SELECT 1", "SELECT 2"} {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": devConn, "sql": sql,
		})
		eq(t, r.Code, 0, "exec "+sql+" response code")
	}

	grown := app.auditChain(token)
	if len(grown) != len(seeded)+2 {
		t.Errorf("expected chain to grow by 2, got %d -> %d", len(seeded), len(grown))
	}
	assertLinked(t, grown) // whole chain, including the seed prefix, still links
}
