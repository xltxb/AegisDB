package bootstrap

// 推送出去的事件里要带**推送时间**与**事件类型**。
//
// 这两样此前都不在请求体里:
//
//   - 事件类型只活在 X-AegisDB-Event 这个头上,而接收端入库的是 body —— 头不会跟着进去。
//     顶层的 resource 一开始填的是事件类型,但审计事件会把它覆盖成实例名;summary 里那个
//     [exec] 前缀是给人读的一句话,不是字段。于是"这条是什么事件"入了库就查不出来了。
//   - 推送时间根本没有。occurred_at 是事件**发生**的时间,ingested_at 是对方**收到**的时间,
//     中间这一段(排队、重试)没有任何东西记录。
//
// 两个字段都放在 raw_payload 里,不是顶层:事件中心的 platform_event 表只有那几列,顶层多
// 出来的字段没有列可落 —— 好一点是被原样丢掉,差一点是撞上字段校验让每一次投递都失败。
// raw_payload 是文档明说会原样入库的那一格。
//
// 这里同时钉住一条最容易悄悄退化的:**重试要带自己那一次的推送时间**。整份 body 在循环外
// 只组装一次的话,第四次重试(可能是 30 秒之后)带的仍然是第一次的时间 —— 那个字段就成了
// 一句谎话,而它存在的全部意义就是让人看出投递延迟。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// capture 收下投递,返回解析好的 body 与头。
type capturedPush struct {
	body    map[string]any
	event   string
	pushHdr string
}

func rawPayloadOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	rp, ok := body["raw_payload"].(map[string]any)
	if !ok {
		t.Fatalf("raw_payload 应是一个对象,实际 %#v", body["raw_payload"])
	}
	return rp
}

// 连通性测试这一条也要带:它要能回答"真事件长什么样"。
func TestWebhook_TestEventCarriesEventTypeAndPushTime(t *testing.T) {
	var mu sync.Mutex
	var got capturedPush
	received := make(chan struct{}, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &got.body)
		got.event = r.Header.Get("X-AegisDB-Event")
		got.pushHdr = r.Header.Get("X-AegisDB-Pushed-At")
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", 3)
	eq(t, app.do(http.MethodPost, "/api/v1/settings/webhook/test", token, nil).Code, 0, "test send")

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("桩没收到测试事件")
	}
	mu.Lock()
	defer mu.Unlock()

	rp := rawPayloadOf(t, got.body)
	if rp["event_type"] != "test" {
		t.Errorf("raw_payload.event_type 应为 test,实际 %#v", rp["event_type"])
	}
	assertPushTime(t, rp["pushed_at"])
	if got.event != "test" || got.pushHdr == "" {
		t.Errorf("两个头也要带上:X-AegisDB-Event=%q X-AegisDB-Pushed-At=%q", got.event, got.pushHdr)
	}
	// 原来就有的那份内容不能被挤掉 —— 新字段是加在旁边的兄弟键。
	if rp["ping"] != "aegisdb" {
		t.Errorf("raw_payload 里原有的内容应原样还在,实际 %#v", rp)
	}
}

// 真事件:事件类型是真实的那个(exec/intercept…),而审计行的字段仍在 raw_payload 里。
func TestWebhook_RealEventKeepsAuditFieldsBesideTheNewOnes(t *testing.T) {
	var mu sync.Mutex
	var got capturedPush
	received := make(chan struct{}, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &got.body)
		got.event = r.Header.Get("X-AegisDB-Event")
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	// 订阅按**增删改查**匹配(见 service.Dispatch):这条 DROP 是一次「删」。
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", 3)

	prodConn := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "推送元信息回归",
	})
	eq(t, r.Code, 42200, "PROD DROP 应被拦成审批单并触发 webhook")

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("桩没收到 intercept 事件")
	}
	mu.Lock()
	defer mu.Unlock()

	rp := rawPayloadOf(t, got.body)
	if rp["event_type"] != "intercept" {
		t.Errorf("raw_payload.event_type 应为 intercept,实际 %#v", rp["event_type"])
	}
	assertPushTime(t, rp["pushed_at"])
	// 审计行原来就在 raw_payload 顶层,新字段没有把它挤到别处去 —— 已经在读
	// raw_payload.command 的接收端不该因为这次改动失效。
	if _, ok := rp["command"]; !ok {
		t.Errorf("审计行的字段应原样还在 raw_payload 里,实际键集合 %v", keysOf(rp))
	}
	// 顶层仍然是事件中心那七个字段,一个不多 —— 多出来的顶层字段没有列可落,还可能被
	// 字段校验拒掉(见 docs/事件中心平台接入文档.md「与表结构映射关系」)。
	want := map[string]bool{
		"source_system": true, "occurred_at": true, "action": true, "resource": true,
		"operator": true, "summary": true, "raw_payload": true,
	}
	for k := range got.body {
		if !want[k] {
			t.Errorf("顶层多了一个字段 %q —— 事件中心的表里没有它的列", k)
		}
	}
	// 事件类型确实在顶层找不回来(resource 被实例名覆盖了),这正是要把它放进
	// raw_payload 的原因。这条断言是那句理由的证据,不是多余的重复。
	if got.body["resource"] == "intercept" {
		t.Skip("resource 仍是事件类型,这条用例的前提已经变了 —— 请重新想清楚放在哪里")
	}
}

// 重试带的是**这一次**的推送时间,不是第一次的。
func TestWebhook_RetryCarriesItsOwnPushTime(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if rp, ok := body["raw_payload"].(map[string]any); ok {
			mu.Lock()
			seen = append(seen, toStr(rp["pushed_at"]))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusInternalServerError) // 逼它重试
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	// 订阅按**增删改查**匹配(见 service.Dispatch):这条 DROP 是一次「删」。
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", 2)

	prodConn := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "重试推送时间",
	}).Code, 42200, "应被拦截并触发 webhook")

	// 退避是 1s,两次尝试足够拉开一秒 —— 时间戳精确到秒,所以两次必须不同。
	deadline := time.Now().Add(8 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("应当至少投递两次(第一次失败后重试),实际 %d 次", len(seen))
	}
	for i, v := range seen {
		assertPushTime(t, v)
		if v == "" {
			t.Fatalf("第 %d 次投递没带推送时间", i+1)
		}
	}
	if seen[0] == seen[1] {
		t.Errorf("重试沿用了第一次的推送时间(%s)—— 整份 body 在循环外只组装一次时就是这个样子,"+
			"而这个字段的意义正是让人看出投递延迟", seen[0])
	}
}

func assertPushTime(t *testing.T, v any) {
	t.Helper()
	s := toStr(v)
	if s == "" {
		t.Fatalf("pushed_at 缺失,实际 %#v", v)
	}
	// 格式必须和 occurred_at 一致(接入文档的 "2026-05-06 14:42:00"),否则接收端要为同一个
	// 载荷里的两个时间写两套解析。
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		t.Fatalf("pushed_at 应与 occurred_at 同一种格式,实际 %q: %v", s, err)
	}
	if d := time.Since(ts); d < -time.Minute || d > 10*time.Minute {
		t.Errorf("pushed_at 应是刚刚:%q(距今 %s)", s, d)
	}
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
