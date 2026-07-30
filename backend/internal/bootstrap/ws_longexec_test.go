package bootstrap

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// slowSQL is a recursive CTE that keeps SQLite busy for a couple of seconds —
// standing in for the DDL a DBA actually runs. It only has to outlast the
// browser's pong budget.
const slowSQL = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 20000000) SELECT count(*) FROM c;`

// sqliteConnID creates a connection pointing at a throwaway SQLite file, which
// gives the suite a target that really executes (the seeded demo instances are
// simulated and return instantly, so they cannot reproduce a long-running
// command).
func (a *testApp) sqliteConnID(token string) int64 {
	a.t.Helper()
	dbPath := filepath.Join(a.t.TempDir(), "target.db")
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "slow-target", "engine": "sqlite", "host": "127.0.0.1:0",
		"env": "dev", "policy": "audit-only", "username": "u", "password": "p",
		"database": dbPath,
	})
	eq(a.t, r.Code, 0, "create sqlite target connection")
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Data, &created); err != nil || created.ID == 0 {
		a.t.Fatalf("decode created connection: %v (%s)", err, string(r.Data))
	}
	return created.ID
}

// The terminal socket must stay answerable while a command runs.
//
// Production symptom: a DDL that runs for a while ends with the socket dropping
// mid-statement, the terminal showing "已断开", and the result never arriving.
// The browser cannot send native WS ping frames, so wsTerminal.ts sends an
// app-level {"type":"ping"} every 20s and closes the socket if no pong comes
// back within 5s. The server handles messages in one sequential loop, so while
// it is blocked executing a statement it never reads the ping — the client's
// watchdog then correctly concludes the socket is half-open and kills it. Any
// statement outlasting the pong budget therefore severs its own connection.
func TestTerminalWS_AnswersHeartbeatWhileCommandRuns(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	connID := app.sqliteConnID(token)

	c := app.dialWS(token)
	defer c.Close()

	start := time.Now()
	if err := c.WriteJSON(map[string]any{"type": "exec", "connectionId": connID, "sql": slowSQL}); err != nil {
		t.Fatalf("ws write exec: %v", err)
	}
	// The heartbeat the browser would send while the statement is still running.
	if err := c.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatalf("ws write ping: %v", err)
	}

	// The browser allows 5s for the pong; be stricter so the test stays fast.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	sawPong := false
	var execElapsed time.Duration
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			break // deadline hit: nothing came back in time
		}
		switch m["type"] {
		case "pong":
			sawPong = true
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("pong took %v — the browser would already have closed the socket", d)
			}
		case "output", "error":
			execElapsed = time.Since(start)
		}
		if sawPong {
			break
		}
	}

	if !sawPong {
		t.Errorf("no pong while a command was running (exec finished after %v) — "+
			"the client's heartbeat watchdog closes the socket mid-statement", execElapsed)
	}
}
