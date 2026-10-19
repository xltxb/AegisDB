package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/dingtalk/dttest"
)

const dtTemplate = "tmpl-test.schema"

// dingtalkIM 起一个假钉钉开放平台,把网关的钉钉渠道指过去、打开交互审批,等到 Stream 连上。
func (a *testApp) dingtalkIM(token string, extra map[string]any) *dttest.Server {
	a.t.Helper()
	srv := dttest.New(a.t)
	kv := map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": dtTemplate, "im.dingtalk.apiBase": srv.URL,
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	srv.WaitConnected(a.t)
	return srv
}

// dingtalkStatus 轮询设置页状态,直到钉钉那一行满足 match。
func (a *testApp) dingtalkStatus(token string, match func(running bool, lastError string) bool) (bool, string) {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := a.do(http.MethodGet, "/api/v1/settings/im/status", token, nil)
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "dingtalk" && match(row.Running, row.LastError) {
				return row.Running, row.LastError
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatal("the DingTalk status never reached the expected state")
	return false, ""
}

func TestDingTalk_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.dingtalkIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的钉钉私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/dingtalk/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	srv.SendDM(t, "staff_owner", "1", issued.Code)
	srv.WaitCall(t, "oto", func(c dttest.Call) bool {
		return c.Str("userIds") == `["staff_owner"]` && strings.Contains(c.Str("msgParam"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片投放到他和机器人的单聊
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "deliver", func(c dttest.Call) bool {
		return c.Str("openSpaceId") == "dtv1.card//IM_ROBOT.staff_owner" && strings.Contains(c.Str("outTrackId"), ap.ApNo)
	})
	if card.Str("cardTemplateId") != dtTemplate || card.Str("callbackType") != "STREAM" ||
		!strings.Contains(card.At("cardData", "cardParamMap", "command"), "DROP TABLE orders") {
		t.Fatalf("card = %+v", card.Params)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 卡片上的提示 → 卡片收起
	tip := srv.Click(t, "staff_owner", card.Str("outTrackId"), "a")
	eq(t, tip, "已批准,请发起人回控制台执行", "tip")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via DingTalk")
	rows, _ := app.repo.AuditByOperatorLike("%(钉钉)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "update", func(c dttest.Call) bool {
		return c.Str("outTrackId") == card.Str("outTrackId") && c.At("cardData", "cardParamMap", "status") == "done" &&
			strings.Contains(c.At("cardData", "cardParamMap", "result"), "通过") && c.At("cardUpdateOptions", "updateCardDataByKey") == "true"
	})
}

func TestDingTalk_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.dingtalkIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	if tip := srv.Click(t, "staff_stranger", "aegis-"+ap.ApNo+"-0123456789abcdef", "a"); !strings.Contains(tip, "未绑定") {
		t.Fatalf("tip = %q", tip)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "dingtalk:staff_stranger" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestDingTalk_WebhookIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.dingtalkIM(admin, nil)
	// 钉钉本期只有 Stream:这扇门没有渠道来验真伪,一律关着。
	eq(t, app.postIMWebhook("dingtalk", nil, `{}`), http.StatusForbidden, "webhook")
}

func TestDingTalk_BadCredentialsShowInStatusWithoutTheSecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := dttest.New(t)
	const wrong = "the-wrong-client-secret-value"
	app.setSettings(admin, map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": wrong,
		"im.dingtalk.cardTemplateId": dtTemplate, "im.dingtalk.apiBase": srv.URL,
	})
	_, lastErr := app.dingtalkStatus(admin, func(running bool, e string) bool {
		return !running && strings.Contains(e, "invalidClientIdOrSecret")
	})
	// 假开放平台把收到的 secret 回显进了错误 message:LastError 里只能剩 scrub 的占位符
	if !strings.Contains(lastErr, "<client-secret>") || strings.Contains(lastErr, wrong) {
		t.Fatalf("lastError = %q", lastErr)
	}
	if r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil); strings.Contains(string(r.Data), wrong) {
		t.Fatalf("status leaked the client secret: %s", r.Data)
	}
}

func TestDingTalk_AMissingCardTemplateShowsInStatus(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := dttest.New(t)
	app.setSettings(admin, map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.apiBase": srv.URL,
	})
	app.dingtalkStatus(admin, func(running bool, e string) bool { return !running && strings.Contains(e, "卡片模板") })
	if n := len(srv.Calls("token")) + len(srv.Calls("open")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestDingTalk_ClientSecretIsStoredEncrypted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	const secret = "dingtalk-client-secret-123456"
	app.setSettings(admin, map[string]any{"im.dingtalk.clientSecret": secret})
	if raw := app.repo.SettingString("im.dingtalk.clientSecret", ""); raw == "" || strings.Contains(raw, secret) {
		t.Errorf("client secret must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", admin, nil); strings.Contains(string(get.Data), secret) {
		t.Error("GET /settings leaked the client secret")
	}
}

func TestIMSettings_DingTalkSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.dingtalk.apiBase":        "ftp://api.dingtalk.com",
		"im.dingtalk.cardTemplateId": "tmpl id with spaces",
		"im.dingtalk.robotCode":      "ding/robot",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be rejected at save time", k, bad)
		}
	}
	app.setSettings(admin, map[string]any{
		"im.dingtalk.apiBase":        "https://api.dingtalk.com",
		"im.dingtalk.cardTemplateId": "dac1dbec-55af-40c3-be91-6d6882ef3b66.schema",
		"im.dingtalk.robotCode":      "dingabc123",
	})
}
