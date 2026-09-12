package bootstrap

// Webhook 密钥:不回显,不明文落库。
//
// 同一个文件里的 GetSettings 早就写着「never expose the secret」并把它抹成空串,而
// 保存那一条走的是 `resp.OK(c, wh)` —— 整行返回,secret 在里面。于是**保存一次就能把
// 已存的密钥读出来**:请求体里 secret 留空表示"保持原值",响应却把原值原样送了回来。
//
// 存储那一头同样对不齐:同类的 approval.external.token 是加密落库的,webhook 的密钥
// 却是明文。一份库备份、一次误配的只读账号,拿到的就是能冒充这个网关往事件中心推数据
// 的凭据。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

func TestWebhookSecret_IsNeverEchoedBack(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const secret = "wh-s3cr3t-value"
	r := app.do(http.MethodPut, "/api/v1/settings/webhook", token, map[string]any{
		"endpoint": "https://events.example.com/ingest", "secret": secret,
		"events": "exec", "retryMax": 3, "enabled": true,
	})
	eq(t, r.Code, 0, "保存 webhook")
	if strings.Contains(string(r.Data), secret) {
		t.Errorf("保存响应把密钥原样送了回来:%s", string(r.Data))
	}

	// 再保存一次、secret 留空(= 保持原值)—— 这正是「读出已存密钥」的那条路。
	r2 := app.do(http.MethodPut, "/api/v1/settings/webhook", token, map[string]any{
		"endpoint": "https://events.example.com/ingest", "secret": "",
		"events": "exec", "retryMax": 3, "enabled": true,
	})
	eq(t, r2.Code, 0, "再次保存")
	if strings.Contains(string(r2.Data), secret) {
		t.Errorf("留空保存把已存的密钥读了出来:%s", string(r2.Data))
	}

	// 界面需要知道的只是「配没配」,给一个布尔位就够 —— 与 GetSettings 的
	// webhookHasSecret 同一套做法。
	var out struct {
		HasSecret bool `json:"hasSecret"`
	}
	_ = json.Unmarshal(r2.Data, &out)
	if !out.HasSecret {
		t.Error("响应要告诉界面密钥已配置(hasSecret),否则界面只能靠回显密钥来判断")
	}
}

func TestWebhookSecret_IsEncryptedAtRest(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const secret = "wh-at-rest-s3cr3t"
	eq(t, app.do(http.MethodPut, "/api/v1/settings/webhook", token, map[string]any{
		"endpoint": "https://events.example.com/ingest", "secret": secret,
		"events": "exec", "retryMax": 3, "enabled": true,
	}).Code, 0, "保存 webhook")

	var row model.WebhookConfig
	if err := app.repo.DB().Table("tbl_webhook_config").First(&row).Error; err != nil {
		t.Fatalf("读 webhook 行: %v", err)
	}
	if row.Secret == secret {
		t.Error("密钥是明文落库的 —— 一份库备份就等于一枚能冒充本网关推数据的凭据")
	}
	if row.Secret == "" {
		t.Fatal("密钥根本没存下来")
	}
}
