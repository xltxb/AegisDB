package bootstrap

// Webhook 按环境过滤:只推选定环境里实例的事件。
//
// 钉四条:
//   1. 存得下、读得回;不存在的环境代码保存时就去掉,省略字段保留原值,空串 = 全部环境;
//   2. 选了环境,别的环境的事件不推,选定环境的照推;
//   3. 没有环境的事件(登录这类「非数据」)不受环境约束 —— 否则勾了「非数据」也收不到;
//   4. 删掉一个环境、实例并到别处时,推送环境里的代码跟着换,不会从此静默停推。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"velagateway/internal/model"
)

func (a *testApp) webhookEnvs(token string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/settings", token, nil)
	var v struct {
		Webhook struct {
			Envs string `json:"envs"`
		} `json:"webhook"`
	}
	_ = json.Unmarshal(r.Data, &v)
	return v.Webhook.Envs
}

func (a *testApp) saveWebhookEnvs(token, endpoint string, envs any) {
	a.t.Helper()
	body := map[string]any{"endpoint": endpoint, "events": "create,delete,update,read,other", "retryMax": 1, "enabled": true}
	if envs != nil {
		body["envs"] = envs
	}
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings/webhook", token, body).Code, 0, "save webhook")
}

func TestWebhookEnvs_RoundTripAndNormalise(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	app.saveWebhookEnvs(token, "http://example.invalid/ingest", " prod, nope,prod,dev")
	if got := app.webhookEnvs(token); got != "prod,dev" {
		t.Fatalf("应去空白、去重、去掉不存在的环境,实际 %q", got)
	}
	app.saveWebhookEnvs(token, "http://example.invalid/ingest", nil) // 省略:保留
	if got := app.webhookEnvs(token); got != "prod,dev" {
		t.Errorf("省略 envs 应保留原值,实际 %q", got)
	}
	app.saveWebhookEnvs(token, "http://example.invalid/ingest", "") // 空串:全部环境
	if got := app.webhookEnvs(token); got != "" {
		t.Errorf("空串 = 全部环境,实际 %q", got)
	}
}

func TestWebhookEnvs_DeliversOnlySelectedEnvironments(t *testing.T) {
	var mu sync.Mutex
	type hit struct{ env, action, eventType string }
	var seen []hit
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if rp, ok := body["raw_payload"].(map[string]any); ok {
			env, _ := rp["env"].(string)
			act, _ := rp["dataAction"].(string)
			et, _ := rp["event_type"].(string)
			mu.Lock()
			seen = append(seen, hit{env, act, et})
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.saveWebhookEnvs(token, stub.URL, "dev") // 只推 dev
	prodConn := app.connIDByEnv(token, "prod")
	devConn := app.connIDByEnv(token, "dev")

	snapshot := func() []hit {
		mu.Lock()
		defer mu.Unlock()
		return append([]hit(nil), seen...)
	}
	waitFor := func(ok func([]hit) bool) []hit {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if s := snapshot(); ok(s) {
				return s
			}
			time.Sleep(50 * time.Millisecond)
		}
		return snapshot()
	}

	// prod 的事件:不推
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "环境过滤",
	}).Code, 42200, "PROD DROP 应被拦截")
	// dev 的事件:推
	app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{"connectionId": devConn, "sql": "SELECT 1;"})
	got := waitFor(func(s []hit) bool {
		for _, h := range s {
			if h.env == "dev" {
				return true
			}
		}
		return false
	})
	time.Sleep(500 * time.Millisecond) // 给 prod 那条万一推出来留点时间
	got = snapshot()
	sawDev := false
	for _, h := range got {
		if h.env == "prod" {
			t.Errorf("只选了 dev,prod 的事件不该推: %+v", h)
		}
		if h.env == "dev" {
			sawDev = true
		}
	}
	if !sawDev {
		t.Fatalf("选了 dev,dev 的事件应当推出来: %+v", got)
	}

	// 登录没有环境:不受环境约束,订阅了「非数据」就推
	before := len(got)
	app.login("linwei@vela.io", "vela123")
	got = waitFor(func(s []hit) bool { return len(s) > before })
	if len(got) == before {
		t.Fatal("没有环境的事件(登录)不该被环境过滤挡掉")
	}
	for _, h := range got[before:] {
		if h.env != "" {
			t.Errorf("这里只该多出没有环境的事件: %+v", h)
		}
	}
}

func TestWebhookEnvs_FollowEnvironmentDeletion(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	if err := app.repo.CreateEnvironment(&model.Environment{Code: "hk-prod", DisplayName: "香港生产", TierCode: "prod"}); err != nil {
		t.Fatal(err)
	}

	app.saveWebhookEnvs(token, "http://example.invalid/ingest", "hk-prod,dev")
	if err := app.repo.DeleteEnvironmentMoving("hk-prod", "prod"); err != nil {
		t.Fatal(err)
	}
	if got := app.webhookEnvs(token); got != "prod,dev" {
		t.Errorf("hk-prod 并入 prod 后,推送环境应换成 prod,实际 %q", got)
	}

	// 接手的环境本来就在清单里:只去掉被删的那个
	if err := app.repo.CreateEnvironment(&model.Environment{Code: "hk-prod", DisplayName: "香港生产", TierCode: "prod"}); err != nil {
		t.Fatal(err)
	}
	app.saveWebhookEnvs(token, "http://example.invalid/ingest", "hk-prod,prod")
	if err := app.repo.DeleteEnvironmentMoving("hk-prod", "prod"); err != nil {
		t.Fatal(err)
	}
	if got := app.webhookEnvs(token); got != "prod" {
		t.Errorf("接手的环境已在清单里,应只剩 prod,实际 %q", got)
	}
}
