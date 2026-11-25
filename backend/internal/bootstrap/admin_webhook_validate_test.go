package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// Webhook 保存的响应与 GET 一致:只回 hasSecret,不回明文(工单 02 第 3 条)。
func TestWebhookSave_DoesNotEchoTheSecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	const secret = "wh-echo-s3cr3t"
	r := app.do(http.MethodPut, "/api/v1/settings/webhook", admin, map[string]any{
		"endpoint": "https://siem.example.com/hook", "secret": secret, "enabled": true,
	})
	eq(t, r.Code, 0, "保存 webhook")
	if strings.Contains(string(r.Data), secret) {
		t.Errorf("保存响应回显了 secret 明文:%s", r.Data)
	}
	if !strings.Contains(string(r.Data), `"hasSecret":true`) {
		t.Errorf("保存响应应当带 hasSecret:true:%s", r.Data)
	}
	// 存下来的还是真的 secret(只是不回给客户端)。
	wh, _ := app.repo.GetWebhook()
	eq(t, wh.Secret, secret, "secret 照常落库")
}

// Webhook 地址只允许 http/https,拒绝 link-local 与云元数据地址;retryMax 上限 10(工单 02 第 4 条)。
func TestWebhookSave_ValidatesEndpointAndRetry(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	bad := []map[string]any{
		{"endpoint": "ftp://siem.example.com/hook"},
		{"endpoint": "javascript:alert(1)"},
		{"endpoint": "http://169.254.169.254/latest/meta-data/"},
		{"endpoint": "http://[fe80::1]/hook"},
		{"endpoint": "http://metadata.google.internal/computeMetadata/v1/"},
		{"endpoint": "http://100.100.100.200/latest/meta-data/"},
		{"endpoint": "https://siem.example.com/hook", "retryMax": 1000000},
		{"endpoint": "https://siem.example.com/hook", "retryMax": -1},
	}
	for _, b := range bad {
		b["enabled"] = true
		r := app.do(http.MethodPut, "/api/v1/settings/webhook", admin, b)
		if r.Code != 40001 {
			t.Errorf("%v:应当 40001,实际 code=%d msg=%s", b, r.Code, r.Msg)
		}
	}
	if wh, _ := app.repo.GetWebhook(); wh != nil && wh.Endpoint != "" && !strings.HasPrefix(wh.Endpoint, "https://siem") {
		t.Errorf("非法地址不该落库:%s", wh.Endpoint)
	}

	eq(t, app.do(http.MethodPut, "/api/v1/settings/webhook", admin, map[string]any{
		"endpoint": "https://siem.example.com/hook", "retryMax": 10, "enabled": true,
	}).Code, 0, "合法地址 + retryMax=10")
}
