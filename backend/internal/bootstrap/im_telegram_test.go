package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

// telegramIM 起一个假 Bot API,把网关的 Telegram 渠道指过去并打开交互审批。
// polling 模式下等到第一次 getUpdates,webhook 模式下等到 setWebhook。
func (a *testApp) telegramIM(token string, extra map[string]any) *tgtest.Server {
	a.t.Helper()
	srv := tgtest.New(a.t)
	kv := map[string]any{
		"im.telegram.approval": true, "im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL,
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	if kv["im.telegram.mode"] == "webhook" {
		srv.WaitCall(a.t, "setWebhook", nil)
	} else {
		srv.WaitCall(a.t, "getUpdates", nil)
	}
	return srv
}

func TestTelegram_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的 Telegram 私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/telegram/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotUsername, "the bind dialog names the bot")
	srv.PushText(777, "private", issued.Code)
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("text"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("reply_markup"), "ap:"+ap.ApNo+":a")
	})
	if card.Str("parse_mode") != "HTML" || !strings.Contains(card.Str("text"), "DROP TABLE orders") {
		t.Fatalf("card = %+v", card.Params)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 应答 → 卡片收起
	q := srv.PushCallback(777, "ap:"+ap.ApNo+":a")
	ans := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == q })
	eq(t, ans.Str("text"), "已批准,请发起人回控制台执行", "toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Telegram")
	rows, _ := app.repo.AuditByOperatorLike("%(Telegram)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "editMessageReplyMarkup", func(c tgtest.Call) bool {
		return strings.Contains(c.Str("reply_markup"), "通过") && strings.Contains(c.Str("reply_markup"), `"done"`)
	})
}

func TestTelegram_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	q := srv.PushCallback(999, "ap:"+ap.ApNo+":a")
	ans := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == q })
	if !strings.Contains(ans.Str("text"), "未绑定") {
		t.Fatalf("toast = %q", ans.Str("text"))
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "telegram:999" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestTelegram_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, map[string]any{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3cret",
		"im.callbackBaseURL": "https://gw.example",
	})
	hook := srv.Calls("setWebhook")[0]
	eq(t, hook.Str("url"), "https://gw.example"+telegram.WebhookPath, "registered URL")
	eq(t, hook.Str("secret_token"), "s3cret", "registered secret")

	app.bindIM("zhangwei@vela.io", "telegram", "777")
	ap := app.submitProdHighRisk(admin)
	body := `{"update_id":1,"callback_query":{"id":"w1","from":{"id":777},"data":"ap:` + ap.ApNo + `:a"}}`

	eq(t, app.postIMWebhook("telegram", nil, body), http.StatusForbidden, "no secret header")
	eq(t, app.postIMWebhook("telegram", map[string]string{"X-Telegram-Bot-Api-Secret-Token": "nope"}, body), http.StatusForbidden, "wrong secret")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	eq(t, app.postIMWebhook("telegram", map[string]string{"X-Telegram-Bot-Api-Secret-Token": "s3cret"}, body), http.StatusOK, "accepted")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == "w1" })
	if len(srv.Calls("getUpdates")) != 0 {
		t.Fatal("webhook mode must not poll")
	}
}

func TestTelegram_SwitchingModesMovesTheWebhook(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)
	eq(t, len(srv.Calls("deleteWebhook")), 1, "polling clears any webhook first")

	app.setSettings(admin, map[string]any{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3", "im.callbackBaseURL": "https://gw.example",
	})
	srv.WaitCall(t, "setWebhook", nil)
	before := len(srv.Calls("getUpdates"))
	time.Sleep(400 * time.Millisecond) // 一个轮询周期:webhook 模式下不该再有 getUpdates
	eq(t, len(srv.Calls("getUpdates")), before, "polling stopped")

	app.setSettings(admin, map[string]any{"im.telegram.mode": "polling"})
	srv.WaitCall(t, "deleteWebhook", func(tgtest.Call) bool { return len(srv.Calls("deleteWebhook")) >= 2 })
	srv.WaitCall(t, "getUpdates", func(tgtest.Call) bool { return len(srv.Calls("getUpdates")) > before })
}

func TestTelegram_BadTokenShowsInStatusWithoutTheToken(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := tgtest.New(t)
	srv.SetFail("getMe", tgtest.Failure{Code: 401, Desc: "Unauthorized"})
	app.setSettings(admin, map[string]any{
		"im.telegram.approval": true, "im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
		if strings.Contains(string(r.Data), srv.Token) {
			t.Fatalf("status leaked the bot token: %s", r.Data)
		}
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "telegram" && !row.Running && strings.Contains(row.LastError, "401") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a rejected token must surface as a stopped channel with a 401 error")
}
