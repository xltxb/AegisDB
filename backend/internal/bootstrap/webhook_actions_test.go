package bootstrap

// 能**订阅**的,和审计行上「增删改查」那一列,必须是同一组。
//
// 这一栏原先按事件类型订阅(intercept/approve/exec/login)。换成增删改查是因为接 SIEM 的人
// 想说的通常是"所有删除都推给我" —— 而被拦下来的 DROP 和执行掉的 DROP 是两个事件类型、
// 同一件事,按类型订阅答不了那句话。
//
// 钉四条:
//   1. 服务端**下发**可订阅清单,就是 gateway.DataActions,界面照着画;
//   2. 存得下、读得回,认不出的取值仍被服务端归一掉;
//   3. 订阅了就真的推,没订阅就真的不推 —— 白名单与派发读的是同一组字符串;
//   4. **旧的按事件类型的订阅会被迁移**,不会变成一个静默停推。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

func (a *testApp) settingsActions(token string) []string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/settings", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("读设置: code=%d msg=%s", r.Code, r.Msg)
	}
	var v struct {
		WebhookActions []string `json:"webhookActions"`
	}
	if err := json.Unmarshal(r.Data, &v); err != nil {
		a.t.Fatalf("解码: %v", err)
	}
	return v.WebhookActions
}

func (a *testApp) webhookEvents(token string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/settings", token, nil)
	var v struct {
		Webhook struct {
			Events string `json:"events"`
		} `json:"webhook"`
	}
	_ = json.Unmarshal(r.Data, &v)
	return v.Webhook.Events
}

// 清单由服务端下发,且就是增删改查那一列的取值。
func TestWebhookActions_ComeFromTheAuditDataActions(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	got := app.settingsActions(token)
	if len(got) == 0 {
		t.Fatal("设置接口应下发可订阅清单 —— 界面自己列一份,迟早和后端对不上")
	}
	want := append([]string(nil), gateway.DataActions...)
	gotSorted := append([]string(nil), got...)
	sort.Strings(want)
	sort.Strings(gotSorted)
	if len(want) != len(gotSorted) {
		t.Fatalf("可订阅清单应与增删改查一致\n  下发 %v\n  分类 %v", got, gateway.DataActions)
	}
	for i := range want {
		if want[i] != gotSorted[i] {
			t.Fatalf("可订阅清单应与增删改查一致\n  下发 %v\n  分类 %v", got, gateway.DataActions)
		}
	}
}

// 存得下读得回;认不出的取值(包括旧的事件类型)被服务端归一掉。
func TestWebhookActions_RoundTripAndNormalise(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	app.configureWebhook(token, "http://example.invalid/ingest", "s3cr3t", "create,delete,update,read,other", 3)
	if got := app.webhookEvents(token); got != "create,delete,update,read,other" {
		t.Fatalf("五类都该存得下,实际 %q", got)
	}
	app.configureWebhook(token, "http://example.invalid/ingest2", "", "create,delete,update,read,other", 5)
	if got := app.webhookEvents(token); got != "create,delete,update,read,other" {
		t.Errorf("换个地址重存之后订阅少了东西:%q", got)
	}
	// 旧的事件类型现在是"认不出的取值",保存时被归一掉 —— 迁移是启动那一次的事,不是这里。
	app.configureWebhook(token, "http://example.invalid/ingest2", "", "delete,exec,nope", 5)
	if got := app.webhookEvents(token); got != "delete" {
		t.Errorf("认不出的取值应被服务端归一掉,实际 %q", got)
	}
}

// 订阅了就真的推,没订阅就真的不推。
func TestWebhookActions_DeliversExactlyWhatIsSubscribed(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		if rp, ok := body["raw_payload"].(map[string]any); ok {
			a, _ := rp["dataAction"].(string)
			seen = append(seen, a)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	// **只**订阅"删"。
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", 3)

	prodConn := app.connIDByEnv(token, "prod")
	// 一条删:应当推。
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "订阅删",
	}).Code, 42200, "PROD DROP 应被拦截")

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("订阅了「删」,而一条删除操作没推出来 —— 白名单与派发读的不是同一组字符串")
	}
	for _, a := range got {
		if a != "delete" {
			t.Errorf("只订阅了「删」,却推出来一条 %q", a)
		}
	}

	// 一次登录是「非数据」,没订阅,不该推。
	before := len(got)
	app.login("linwei@aegisdb.io", "aegisdb123")
	time.Sleep(1200 * time.Millisecond)
	mu.Lock()
	after := len(seen)
	mu.Unlock()
	if after != before {
		t.Errorf("没订阅「非数据」,登录不该推 —— 多出了 %d 条", after-before)
	}
}

// 旧的按事件类型的订阅要被迁走,否则是一个**静默停推**。
func TestWebhookActions_LegacySubscriptionIsMigratedNotSilentlyDropped(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// 装出一份升级前的订阅:里面全是事件类型,一个合法的增删改查都没有。
	if err := db.Model(&model.WebhookConfig{}).Where("1 = 1").
		Update("events", "exec,login").Error; err != nil {
		t.Fatalf("装旧值: %v", err)
	}
	if err := backfillWebhookActions(db); err != nil {
		t.Fatalf("迁移: %v", err)
	}

	var wh model.WebhookConfig
	if err := db.First(&wh).Error; err != nil {
		t.Fatalf("读回: %v", err)
	}
	if wh.Events == "exec,login" {
		t.Fatal("旧订阅没被迁移 —— 换维之后它一个都匹配不上,webhook 从升级那一刻起什么都不发")
	}
	for _, a := range gateway.DataActions {
		if !contains(wh.Events, a) {
			t.Errorf("旧订阅应放宽为全部(推多了是噪音,推少了是审计事件悄悄漏掉),缺 %q:%q", a, wh.Events)
		}
	}

	// 幂等:再跑一遍不动它。
	if err := backfillWebhookActions(db); err != nil {
		t.Fatalf("第二遍: %v", err)
	}
	var again model.WebhookConfig
	_ = db.First(&again).Error
	if again.Events != wh.Events {
		t.Errorf("第二遍不该再动:%q → %q", wh.Events, again.Events)
	}

	// 空列表是一个明确的"什么都不推",不能被当成旧值放宽掉。
	if err := db.Model(&model.WebhookConfig{}).Where("1 = 1").Update("events", "").Error; err != nil {
		t.Fatalf("清空: %v", err)
	}
	if err := backfillWebhookActions(db); err != nil {
		t.Fatalf("第三遍: %v", err)
	}
	var empty model.WebhookConfig
	_ = db.First(&empty).Error
	if empty.Events != "" {
		t.Errorf("空订阅是明确的「什么都不推」,不该被放宽成 %q", empty.Events)
	}
}

func contains(csv, want string) bool {
	for _, p := range splitCSV(csv) {
		if p == want {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}
