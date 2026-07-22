package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A command carrying a credential must be masked before it enters the audit log —
// the immutable log must never store a password in the clear.
func TestAudit_RedactsPasswordLiterals(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	const secret = "sup3rSecretPw!"
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": "CREATE USER 'bob'@'%' IDENTIFIED BY '" + secret + "'",
	})
	eq(t, r.Code, 0, "create user executes on dev")

	rows := app.do(http.MethodGet, "/api/v1/audit", token, nil)
	eq(t, rows.Code, 0, "audit list code")
	var audit []struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rows.Data, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}

	found := false
	for _, a := range audit {
		if strings.Contains(a.Command, "IDENTIFIED BY") {
			found = true
			if strings.Contains(a.Command, secret) {
				t.Errorf("audit log leaked the password: %q", a.Command)
			}
			if !strings.Contains(a.Command, "'***'") {
				t.Errorf("expected a masked password in the audit command, got %q", a.Command)
			}
		}
	}
	if !found {
		t.Fatal("CREATE USER audit row not found")
	}
}
