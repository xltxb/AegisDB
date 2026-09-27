package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 已配置的飞书签名密钥,不能被一次普通的设置保存(回传空密钥字段)抹掉;GET 不能回显它;
// 它在库里必须是密文。可观察的一面:配了密钥时卡片带 "sign",密钥被抹掉 "sign" 就消失。
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
	app.setSettings(token, map[string]any{
		"im.lark.notify": true, "im.lark.notifyWebhook": lark.URL,
		"im.lark.notifySecret": secret, "notify.consoleURL": "https://gw.corp.io",
	})
	eq(t, app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil).Code, 0, "test #1")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Fatalf("expected a signed card while secret is set, got: %s", body)
	}

	if raw := app.repo.SettingString("im.lark.notifySecret", ""); raw == "" || strings.Contains(raw, secret) {
		t.Errorf("secret must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", token, nil); strings.Contains(string(get.Data), secret) {
		t.Error("GET /settings leaked the raw lark secret")
	}

	app.setSettings(token, map[string]any{"im.lark.notifySecret": "", "notify.consoleURL": "https://gw.corp.io/changed"})
	eq(t, app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil).Code, 0, "test #2")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Errorf("lark secret was wiped by the settings save — card no longer signed: %s", body)
	}
}
