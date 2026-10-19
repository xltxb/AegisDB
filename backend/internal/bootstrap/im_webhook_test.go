package bootstrap

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/imtest"
)

// postIMWebhookFull 直接打网关的 webhook 入口(不带 JWT),返回状态码与响应体。
func (a *testApp) postIMWebhookFull(channel string, header map[string]string, body string) (int, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/im/"+channel+"/webhook", bytes.NewReader([]byte(body)))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("webhook: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (a *testApp) postIMWebhook(channel string, header map[string]string, body string) int {
	a.t.Helper()
	code, _ := a.postIMWebhookFull(channel, header, body)
	return code
}

func (a *testApp) webhookFake(token string) *imtest.Fake {
	a.t.Helper()
	f := a.fakeIM("fake")
	f.WebhookSecret = "s3"
	a.enableIM(token, "fake")
	return f
}

func TestIMWebhook_ApprovesThroughTheSameDecisionPath(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.webhookFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin)

	code, respBody := app.postIMWebhookFull("fake", map[string]string{"X-Fake-Secret": "s3"},
		`{"pid":"p-zw","apNo":"`+ap.ApNo+`","approve":true}`)
	eq(t, code, http.StatusOK, "accepted")
	if !strings.Contains(respBody, `"toast"`) {
		t.Fatalf("the channel's reply body must reach the platform, got %q", respBody)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	if toasts := f.WebhookToasts(); len(toasts) != 1 || toasts[0] != "已批准,请发起人回控制台执行" {
		t.Fatalf("toasts = %q", toasts)
	}
}

func TestIMWebhook_FailsClosed(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake").WebhookSecret = "s3"
	good := map[string]string{"X-Fake-Secret": "s3"}
	body := `{"pid":"x","apNo":"AP-1","approve":true}`

	eq(t, app.postIMWebhook("nope", good, body), http.StatusNotFound, "unknown channel")
	eq(t, app.postIMWebhook("fake", good, body), http.StatusForbidden, "approval switch off")
	app.enableIM(admin, "fake")
	eq(t, app.postIMWebhook("fake", map[string]string{"X-Fake-Secret": "no"}, body), http.StatusForbidden, "bad secret")
	eq(t, app.postIMWebhook("fake", nil, body), http.StatusForbidden, "no secret")

	app.setSettings(admin, map[string]any{"im.callbackAllowIPs": "10.0.0.0/8"})
	good["X-Forwarded-For"] = "203.0.113.9" // 夹具信任回环代理,这个头就是客户端地址
	eq(t, app.postIMWebhook("fake", good, body), http.StatusForbidden, "source IP not allowed")
	good["X-Forwarded-For"] = "10.1.2.3"
	eq(t, app.postIMWebhook("fake", good, body), http.StatusOK, "allowed source")
}

func TestIMSettings_CallbackKeysAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, v := range map[string]any{
		"im.callbackBaseURL":  "://nope",
		"im.callbackAllowIPs": "10.0.0.300",
		"im.telegram.apiBase": "://nope",
		"im.telegram.mode":    "carrier-pigeon",
	} {
		if r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: v}); r.Code == 0 {
			t.Errorf("%s=%v must be rejected at save time", k, v)
		}
	}
	app.setSettings(admin, map[string]any{"im.telegram.mode": "webhook", "im.callbackAllowIPs": "10.0.0.0/8, 192.168.1.5"})
}

func TestIMSettings_TelegramWebhookSecretIsValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	save := func(v string) int {
		return app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"im.telegram.webhookSecret": v}).Code
	}
	for _, bad := range []string{"1", strings.Repeat("a", 31), strings.Repeat("a", 39) + "!", strings.Repeat("a", 257)} {
		if save(bad) == 0 {
			t.Errorf("webhook secret %q must be rejected at save time", bad)
		}
	}
	eq(t, save(strings.Repeat("a", 32)), 0, "32 valid chars")
	eq(t, save(strings.Repeat("aZ0_-", 8)), 0, "all allowed characters")
	eq(t, save(""), 0, "empty keeps the existing secret")
}
