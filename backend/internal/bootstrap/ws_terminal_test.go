package bootstrap

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS opens the terminal websocket carrying the JWT in the
// Sec-WebSocket-Protocol header (the production transport).
func (a *testApp) dialWS(token string) *websocket.Conn {
	a.t.Helper()
	wsURL := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/terminal/ws"
	d := websocket.Dialer{Subprotocols: []string{"vela-token", token}}
	c, _, err := d.Dial(wsURL, nil)
	if err != nil {
		a.t.Fatalf("dial ws: %v", err)
	}
	return c
}

func wsExecType(t *testing.T, c *websocket.Conn, connID int64, sql string) string {
	t.Helper()
	if err := c.WriteJSON(map[string]any{"type": "exec", "connectionId": connID, "sql": sql}); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_ = c.SetReadDeadline(timeNowPlus(3))
	var resp map[string]any
	if err := c.ReadJSON(&resp); err != nil {
		t.Fatalf("ws read: %v", err)
	}
	s, _ := resp["type"].(string)
	return s
}

func timeNowPlus(sec int) (deadline time.Time) {
	return time.Now().Add(time.Duration(sec) * time.Second)
}

// A logout mid-connection revokes the token, and the very next command on an
// already-open terminal socket must be rejected — the session check runs per
// command, not only at handshake (R5).
func TestTerminalWS_RevokedSessionRejectedMidConnection(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	c := app.dialWS(token)
	defer c.Close()

	// a safe command works while the session is valid
	if typ := wsExecType(t, c, dev, "SELECT 1"); typ != "output" {
		t.Fatalf("first exec: got type %q, want output", typ)
	}

	// logout bumps the token version, revoking this session
	eq(t, app.do(http.MethodPost, "/api/v1/auth/logout", token, nil).Code, 0, "logout")

	// the next command on the same open socket must be refused
	if typ := wsExecType(t, c, dev, "SELECT 1"); typ != "session_revoked" {
		t.Errorf("post-logout exec: got type %q, want session_revoked", typ)
	}
}
