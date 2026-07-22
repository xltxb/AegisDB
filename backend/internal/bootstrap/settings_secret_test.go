package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A configured Lark signing secret must survive an ordinary settings save that
// echoes back the masked/blank secret field. Regression for the secret-masking
// mechanism: GET masked the value, the frontend round-tripped an empty string,
// and the save wiped the real secret. Observable: postLark only adds a "sign"
// field when a secret is set, so a wiped secret makes "sign" disappear.
func TestSettings_SavingDoesNotWipeLarkSecret(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const secret = "lark-signing-secret-123"
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"notify.lark": true, "notify.larkWebhook": lark.URL,
		"notify.larkSecret": secret, "notify.consoleURL": "https://gw.corp.io",
	}).Code, 0, "configure lark with secret")

	// sanity: with a secret set, the delivered card carries a signature
	eq(t, app.do(http.MethodPost, "/api/v1/settings/lark/test", token, nil).Code, 0, "lark test #1")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Fatalf("expected a signed card while secret is set, got: %s", body)
	}

	// GET must not leak the real secret back to the client
	get := app.do(http.MethodGet, "/api/v1/settings", token, nil)
	eq(t, get.Code, 0, "get settings")
	if strings.Contains(string(get.Data), secret) {
		t.Errorf("GET /settings leaked the raw lark secret")
	}

	// simulate the frontend round-trip that used to clobber it: save settings
	// echoing an empty secret field (plus an unrelated change)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"notify.larkSecret": "", "notify.consoleURL": "https://gw.corp.io/changed",
	}).Code, 0, "save settings with blank secret echoed back")

	// the secret must still be in force → card is still signed
	eq(t, app.do(http.MethodPost, "/api/v1/settings/lark/test", token, nil).Code, 0, "lark test #2")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Errorf("lark secret was wiped by the settings save — card no longer signed: %s", body)
	}
}

func waitCard(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no card delivered to the Lark webhook")
		return ""
	}
}
