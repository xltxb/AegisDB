package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/lark/larktest"
)

// larkIM 起一个假飞书开放平台,把网关的飞书渠道指过去并打开交互审批。长连接模式下等到
// SDK 连上;webhook 模式自动带上假平台的 Encrypt Key 与 Verification Token,等到 Run 查过
// 应用信息。
func (a *testApp) larkIM(token string, extra map[string]any) *larktest.Server {
	a.t.Helper()
	srv := larktest.New(a.t)
	kv := map[string]any{
		"im.lark.approval": true, "im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL,
	}
	if extra["im.lark.mode"] == "webhook" {
		kv["im.lark.encryptKey"] = srv.EncryptKey
		kv["im.lark.verificationToken"] = srv.VerificationToken
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	if kv["im.lark.mode"] == "webhook" {
		srv.WaitCall(a.t, "botInfo", nil)
	} else {
		srv.WaitConnected(a.t)
	}
	return srv
}

func TestLark_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.larkIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的飞书私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/lark/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotName, "the bind dialog names the bot")
	srv.SendDM(t, "ou_owner", "p2p", issued.Code)
	srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_owner" && c.Str("msg_type") == "text" && strings.Contains(c.Str("content"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_owner" && c.Str("msg_type") == "interactive" && strings.Contains(c.Str("content"), ap.ApNo)
	})
	if !strings.Contains(card.Str("content"), "DROP TABLE orders") {
		t.Fatalf("card = %s", card.Str("content"))
	}

	// 3. 点「通过」→ 走控制台同一条判定 → toast → 卡片收起
	toast := srv.Click(t, "ou_owner", map[string]any{"ap": ap.ApNo, "act": "a"})
	eq(t, toast, "已批准,请发起人回控制台执行", "toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Lark")
	rows, _ := app.repo.AuditByOperatorLike("%(飞书)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "patch", func(c larktest.Call) bool {
		return strings.Contains(c.Str("content"), "工单 "+ap.ApNo) && strings.Contains(c.Str("content"), "通过") &&
			!strings.Contains(c.Str("content"), `"act"`)
	})
}

func TestLark_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.larkIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	if toast := srv.Click(t, "ou_stranger", map[string]any{"ap": ap.ApNo, "act": "a"}); !strings.Contains(toast, "未绑定") {
		t.Fatalf("toast = %q", toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "lark:ou_stranger" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestLark_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.larkIM(admin, map[string]any{"im.lark.mode": "webhook"})

	// 开发者后台保存请求地址时的 URL 校验(不带签名头)
	challenge, _ := json.Marshal(map[string]string{"encrypt": larktest.Encrypt(srv.EncryptKey, srv.Challenge(srv.VerificationToken))})
	code, body := app.postIMWebhookFull("lark", nil, string(challenge))
	if code != http.StatusOK || !strings.Contains(body, "c-123") {
		t.Fatalf("challenge → %d %s", code, body)
	}

	app.bindIM("zhangwei@vela.io", "lark", "ou_owner")
	ap := app.submitProdHighRisk(admin)
	h, click := srv.Signed(srv.CardAction("ou_owner", map[string]any{"ap": ap.ApNo, "act": "a"}))
	headers := map[string]string{}
	for k := range h {
		headers[k] = h.Get(k)
	}

	eq(t, app.postIMWebhook("lark", nil, string(click)), http.StatusForbidden, "unsigned")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	code, body = app.postIMWebhookFull("lark", headers, string(click))
	if code != http.StatusOK || !strings.Contains(body, "已批准") {
		t.Fatalf("signed click → %d %s", code, body)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	if len(srv.Calls("wsEndpoint")) != 0 {
		t.Fatal("webhook mode must not open the long connection")
	}
}

func TestLark_BadCredentialsShowInStatusWithoutTheSecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := larktest.New(t)
	const wrong = "the-wrong-app-secret-value"
	app.setSettings(admin, map[string]any{
		"im.lark.approval": true, "im.lark.appID": srv.AppID, "im.lark.appSecret": wrong, "im.lark.apiBase": srv.URL,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
		if strings.Contains(string(r.Data), wrong) {
			t.Fatalf("status leaked the app secret: %s", r.Data)
		}
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "lark" && !row.Running && strings.Contains(row.LastError, "10014") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("rejected credentials must surface as a stopped channel with Lark's error")
}

func TestLark_EncryptKeyIsTreatedAsASecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	const key = "lark-encrypt-key-123456"
	app.setSettings(admin, map[string]any{"im.lark.encryptKey": key})
	if raw := app.repo.SettingString("im.lark.encryptKey", ""); raw == "" || strings.Contains(raw, key) {
		t.Errorf("encrypt key must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", admin, nil); strings.Contains(string(get.Data), key) {
		t.Error("GET /settings leaked the encrypt key")
	}
}

func TestIMSettings_LarkSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.lark.domain":  "feishu.cn",
		"im.lark.mode":    "polling",
		"im.lark.apiBase": "ftp://open.feishu.cn",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be rejected at save time", k, bad)
		}
	}
	app.setSettings(admin, map[string]any{"im.lark.domain": "lark", "im.lark.mode": "webhook", "im.lark.apiBase": "https://open.larksuite.com"})
}
