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

// 飞书群通知:配置好 webhook 后,测试接口发一张样例卡片;真实的高危工单推一张带单号的卡片。
func TestLark_NotifyCardOnNewApproval(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"im.lark.notify": true, "im.lark.notifyWebhook": lark.URL, "notify.consoleURL": "https://gw.corp.io",
	})

	tr := app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil)
	eq(t, tr.Code, 0, "test-notify code")
	var tres struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(tr.Data, &tres)
	if !tres.Ok {
		t.Fatalf("test notify should succeed, got %q", tres.Message)
	}
	if body := waitCard(t, received); !strings.Contains(body, `"interactive"`) || !strings.Contains(body, "AP-TEST") {
		t.Errorf("test card is not an interactive approval card: %s", body)
	}

	prod := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders_2024_q3;", "reason": "q3 cleanup",
	})
	eq(t, r.Code, 42200, "high-risk intercepted")
	var er struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &er)
	body := waitCard(t, received)
	if !strings.Contains(body, er.ApprovalNo) || !strings.Contains(body, "https://gw.corp.io/approvals") {
		t.Errorf("approval card missing apNo %s or console link: %s", er.ApprovalNo, body)
	}
}

func TestLark_NotifySwitchOffSendsNothing(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "hit"
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"im.lark.notify": false, "im.lark.notifyWebhook": lark.URL})
	app.submitProdHighRisk(token)
	app.flushIM()
	select {
	case <-received:
		t.Fatal("switch is off, nothing should be posted")
	case <-time.After(200 * time.Millisecond):
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
