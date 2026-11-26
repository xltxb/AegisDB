package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// slowSQL keeps the target busy for a few seconds — standing in for the DDL a
// DBA actually runs. It only has to outlast the browser's pong budget.
const slowSQL = `SELECT pg_sleep(3);`

// pgConnID creates a connection to a throwaway PostgreSQL database, which gives
// the suite a target that really executes (the seeded demo instances are
// simulated and return instantly, so they cannot reproduce a long-running
// command).
func (a *testApp) pgConnID(token string) int64 {
	a.t.Helper()
	body := targetConnBody(a.t, "slow-target", "dev", newTargetDB(a.t))
	r := a.do(http.MethodPost, "/api/v1/connections", token, body)
	eq(a.t, r.Code, 0, "create PostgreSQL target connection")
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
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	connID := app.pgConnID(token)

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

	// 等这条语句真正跑完再返回。
	//
	// 断言在看到 pong 的那一刻就成立了,但那时 slowSQL 还在跑,目标库上还有
	// 一条活着的会话。清理阶段要紧接着 DROP DATABASE,让语句先收尾,清理就不必去强杀
	// 它 —— 一张会无故变红的回归网,很快就没人再信它。
	drainExec(t, c, execElapsed)
}

// drainExec reads until the exec reports back (output or error), so the target
// database has no live session left when it is dropped. Bounded: a statement that
// never finishes must not hang the suite.
func drainExec(t *testing.T, c *websocket.Conn, already time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			t.Logf("exec did not report back within the drain window (pong seen at %v): %v", already, err)
			return
		}
		if m["type"] == "output" || m["type"] == "error" {
			return
		}
	}
}
