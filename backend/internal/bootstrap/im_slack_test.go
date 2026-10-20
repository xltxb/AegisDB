package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/slack/slacktest"
)

// slackIM 起一个假 Slack,把网关的 Slack 渠道指过去、打开交互审批。Socket Mode 等到连上;webhook 模式
// 带上假 Slack 的 Signing Secret,等到 Run 验过 Bot Token。
func (a *testApp) slackIM(token string, webhook bool) *slacktest.Server {
	a.t.Helper()
	srv := slacktest.New(a.t)
	kv := map[string]any{
		"im.slack.approval": true, "im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken,
		"im.slack.apiBase": srv.URL + "/api",
	}
	if webhook {
		kv["im.slack.mode"], kv["im.slack.signingSecret"] = "webhook", srv.SigningSecret
	}
	a.setSettings(token, kv)
	if webhook {
		srv.WaitCall(a.t, "auth.test", nil)
	} else {
		srv.WaitConnected(a.t)
	}
	return srv
}

func TestSlack_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.slackIM(admin, false)

	// 1. 自助绑定:控制台生成码(弹窗里告诉他私聊哪个机器人),用自己的 Slack 私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/slack/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotName, "the bind dialog names the bot")
	srv.SendDM(t, "U0OWNER", issued.Code)
	srv.WaitCall(t, "chat.postMessage", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("text"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "chat.postMessage", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("blocks"), ap.ApNo)
	})
	if !strings.Contains(card.Get("blocks"), "DROP TABLE orders") || !strings.Contains(card.Get("blocks"), "aegis_actions") {
		t.Fatalf("card = %s", card.Get("blocks"))
	}
	open := srv.Calls("conversations.open")
	if len(open) == 0 || open[0].Get("users") != "U0OWNER" {
		t.Fatalf("conversations.open = %+v", open)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 只有他看得见的结果 → 卡片收起
	eq(t, srv.Click(t, "U0OWNER", ap.ApNo, "a"), "已批准,请发起人回控制台执行", "ephemeral")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Slack")
	rows, _ := app.repo.AuditByOperatorLike("%(Slack)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "chat.update", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("blocks"), "通过") &&
			!strings.Contains(c.Get("blocks"), "aegis_actions") && strings.Contains(c.Get("blocks"), "DROP TABLE orders")
	})
}

func TestSlack_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.slackIM(admin, false)
	ap := app.submitProdHighRisk(admin)

	if got := srv.Click(t, "U0STRANGER", ap.ApNo, "a"); !strings.Contains(got, "未绑定") {
		t.Fatalf("ephemeral = %q", got)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "slack:U0STRANGER" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestSlack_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.slackIM(admin, true)

	// Slack 后台保存 Request URL 时的校验
	verify := `{"token":"legacy","challenge":"c-123","type":"url_verification"}`
	code, body := app.postIMWebhookFull("slack", srv.SignedHeaders(verify, ""), verify)
	if code != http.StatusOK || body != `{"challenge":"c-123"}` {
		t.Fatalf("challenge → %d %s", code, body)
	}

	app.bindIM("zhangwei@vela.io", "slack", "U0OWNER")
	ap := app.submitProdHighRisk(admin)
	click := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", "aegis_approve", "ap:"+ap.ApNo+":a"))

	eq(t, app.postIMWebhook("slack", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, click), http.StatusForbidden, "unsigned")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	eq(t, app.postIMWebhook("slack", srv.SignedHeaders(click, "application/x-www-form-urlencoded"), click), http.StatusOK, "signed click")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	srv.WaitCall(t, "chat.postEphemeral", func(c slacktest.Call) bool { return c.Get("user") == "U0OWNER" })
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("webhook mode opened %d socket mode connections", n)
	}
}

func TestSlack_ARejectedBotTokenShowsInStatusWithoutTheToken(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token-0000"
	app.setSettings(admin, map[string]any{
		"im.slack.approval": true, "im.slack.botToken": wrong, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api",
	})
	_, lastErr := app.channelStatus(admin, "slack", func(running bool, e string) bool {
		return !running && strings.Contains(e, "invalid_auth")
	})
	// 假 Slack 把收到的 token 回显进了错误:LastError 里只能剩 scrub 的占位符
	if !strings.Contains(lastErr, "<bot-token>") || strings.Contains(lastErr, wrong) {
		t.Fatalf("lastError = %q", lastErr)
	}
	if r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil); strings.Contains(string(r.Data), wrong) {
		t.Fatalf("status leaked the bot token: %s", r.Data)
	}
}

func TestSlack_WebhookModeWithoutASigningSecretShowsInStatus(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := slacktest.New(t)
	// webhook 模式缺 Signing Secret:纯配置错,在任何请求之前就报
	app.setSettings(admin, map[string]any{
		"im.slack.approval": true, "im.slack.botToken": srv.BotToken, "im.slack.mode": "webhook", "im.slack.apiBase": srv.URL + "/api",
	})
	app.channelStatus(admin, "slack", func(running bool, e string) bool { return !running && strings.Contains(e, "Signing Secret") })
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestSlack_SecretsAreStoredEncrypted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	secrets := map[string]string{
		"im.slack.botToken":      "xoxb-1234567890-secret-bot-token",
		"im.slack.appToken":      "xapp-1-A0TEST-secret-app-token",
		"im.slack.signingSecret": "0123456789abcdef0123456789abcdef",
	}
	kv := map[string]any{}
	for k, v := range secrets {
		kv[k] = v
	}
	app.setSettings(admin, kv)
	get := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	for k, v := range secrets {
		if raw := app.repo.SettingString(k, ""); raw == "" || strings.Contains(raw, v) {
			t.Errorf("%s must be stored encrypted, stored %q", k, raw)
		}
		if strings.Contains(string(get.Data), v) {
			t.Errorf("GET /settings leaked %s", k)
		}
	}
}

func TestIMSettings_SlackSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.slack.apiBase":       "ftp://slack.com/api",
		"im.slack.mode":          "rtm",
		"im.slack.botToken":      "xapp-1-A0TEST-this-is-the-app-token", // 两种 token 填反
		"im.slack.appToken":      "xoxb-1234567890-this-is-the-bot-token",
		"im.slack.signingSecret": "too-short",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be refused at save time", k, bad)
		}
	}
	// 用户 token 也不行:拿它发的卡片会以那个人的名义出现
	if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"im.slack.botToken": "xoxp-1234567890-a-user-token"}).Code == 0 {
		t.Error("a user token must be refused as the bot token")
	}
	app.setSettings(admin, map[string]any{
		"im.slack.apiBase": "https://slack.com/api", "im.slack.mode": "webhook",
		"im.slack.botToken": "xoxb-1234567890-0987654321-AbCdEfGhIjKl", "im.slack.appToken": "xapp-1-A0TEST001-1234567890-abcdef",
		"im.slack.signingSecret": "8f742231b10e8888abcd99edf5d7d85a",
	})
}
