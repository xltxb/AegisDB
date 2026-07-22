package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Lark (飞书) channel: once a webhook is configured, the test endpoint sends a
// sample approval card, and a real high-risk approval pushes an interactive card
// to the bot webhook.
func TestLark_ApprovalCardPushedToWebhook(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// configure the Lark channel
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"notify.lark": true, "notify.larkWebhook": lark.URL, "notify.consoleURL": "https://gw.corp.io",
	}).Code, 0, "configure lark")

	// the test endpoint sends a sample card
	tr := app.do(http.MethodPost, "/api/v1/settings/lark/test", token, nil)
	eq(t, tr.Code, 0, "lark test code")
	var tres struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(tr.Data, &tres)
	if !tres.Ok {
		t.Errorf("lark test card should succeed, got %q", tres.Message)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, `"interactive"`) || !strings.Contains(body, "AP-TEST") {
			t.Errorf("test card not an interactive approval card: %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no test card delivered to the Lark webhook")
	}

	// a real high-risk approval pushes an interactive card carrying its ap number
	prod := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders_2024_q3;", "reason": "q3 cleanup",
	})
	eq(t, r.Code, 42200, "high-risk intercepted")
	var er struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &er)
	if er.ApprovalNo == "" {
		t.Fatal("expected an approval number")
	}
	select {
	case body := <-received:
		if !strings.Contains(body, er.ApprovalNo) || !strings.Contains(body, `"interactive"`) {
			t.Errorf("approval card missing apNo %s or not interactive: %s", er.ApprovalNo, body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no approval card delivered to the Lark webhook")
	}
}
