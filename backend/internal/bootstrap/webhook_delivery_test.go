package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deliveryRow is the GET /settings/webhook/deliveries projection.
type deliveryRow struct {
	Event    string `json:"event"`
	Success  bool   `json:"success"`
	Attempts int    `json:"attempts"`
}

// webhookDeliveries fetches the delivery log.
func (a *testApp) webhookDeliveries(token string) []deliveryRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/settings/webhook/deliveries", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list webhook deliveries: code=%d msg=%s", r.Code, r.Msg)
	}
	var rows []deliveryRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		a.t.Fatalf("deliveries decode: %v", err)
	}
	return rows
}

// US#48: sending a test event must be recorded so admins can review the delivery
// outcome. Tracer bullet: a synchronous test-send leaves one "test" delivery
// marked successful, observable through GET /settings/webhook/deliveries.
func TestWebhookDelivery_TestSendIsRecorded(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.configureWebhook(token, stub.URL, "secret", "create,delete,update", 3)

	r := app.do(http.MethodPost, "/api/v1/settings/webhook/test", token, nil)
	eq(t, r.Code, 0, "webhook test-send response code")

	rows := app.webhookDeliveries(token)
	var found *deliveryRow
	for i := range rows {
		if rows[i].Event == "test" {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a recorded 'test' delivery, found none")
	}
	if !found.Success {
		t.Errorf("test delivery to a 200 stub should be recorded as success=true")
	}
}

// A successful async delivery (real event, 200 stub) is recorded as success on
// the first attempt — the happy-path counterpart of the failure case.
func TestWebhookDelivery_AsyncSuccessRecorded(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	// 订阅按**增删改查**匹配(见 service.Dispatch):这条 DROP 是一次「删」。
	app.configureWebhook(token, stub.URL, "secret", "delete", 3)

	prodConn := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "async success test",
	})
	eq(t, r.Code, 42200, "PROD DROP should intercept (and fire the webhook)")

	deadline := time.Now().Add(5 * time.Second)
	var rec *deliveryRow
	for time.Now().Before(deadline) && rec == nil {
		for _, row := range app.webhookDeliveries(token) {
			if row.Event == "intercept" {
				r := row
				rec = &r
				break
			}
		}
		if rec == nil {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if rec == nil {
		t.Fatal("expected a recorded 'intercept' delivery, found none")
	}
	if !rec.Success {
		t.Error("delivery to a 200 stub should be recorded as success=true")
	}
	eq(t, rec.Attempts, 1, "successful delivery should record a single attempt")
}

// 投递按**增删改查**过滤:一条审计事件的 data_action 不在订阅列表里,就永远不会被推出去。
//
// 这里订阅「查」:一条 dev 上的 SELECT 是查,推;而一次登录是「非数据」,不推。登录量大、
// 对事件中心价值低,"别把登录推过去"正是这条过滤最常被用来做的事 —— 换维之后它仍然表达
// 得出来(不勾「非数据」),所以这条用例保留,只是换了订阅的那一维。
//
// 断言看的是 X-Vela-Event 请求头,那里仍然是**事件类型** —— 类型一路带着走,只是不再决定
// 推不推。
func TestWebhookDelivery_ActionFilterExcludesLogin(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.Header.Get("X-Vela-Event")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	// 只订阅「查」;登录是「非数据」,必须被过滤掉。
	app.configureWebhook(token, stub.URL, "secret", "read", 3)

	// A fresh login records a 'login' audit → dispatch, which the filter must drop.
	app.login("linwei@vela.io", "vela123")

	// A low-risk command on a dev connection records an 'exec' audit → must deliver.
	dev := app.connIDByEnv(token, "dev")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1;", "reason": "filter test",
	})
	eq(t, r.Code, 0, "dev SELECT executes")

	// Wait for the subscribed exec delivery to land.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := got["exec"]
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got["exec"] == 0 {
		t.Error("订阅了「查」,那条 SELECT(事件类型 exec)应当被推出去")
	}
	// login dispatch is filtered synchronously (before any network attempt), so a
	// non-zero count here is a hard filtering failure, not a timing artefact.
	if got["login"] != 0 {
		t.Errorf("没订阅「非数据」,登录不该被推,实际 %d 条", got["login"])
	}
}

// A failing delivery is recorded as unsuccessful with the attempt count it
// exhausted (bounded by retry_max).
func TestWebhookDelivery_FailedDeliveryRecordedWithAttempts(t *testing.T) {
	var hits int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const retryMax = 2
	// 订阅按**增删改查**匹配(见 service.Dispatch):这条 DROP 是一次「删」。
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", retryMax)

	// One intercept event → async dispatch against the always-failing stub.
	prodConn := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "delivery log test",
	})
	eq(t, r.Code, 42200, "PROD DROP should intercept (and fire the webhook)")

	// Poll the delivery log until the failed intercept delivery is recorded.
	deadline := time.Now().Add(10 * time.Second)
	var rec *deliveryRow
	for time.Now().Before(deadline) {
		for _, row := range app.webhookDeliveries(token) {
			if row.Event == "intercept" {
				r := row
				rec = &r
				break
			}
		}
		if rec != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rec == nil {
		t.Fatal("expected a recorded 'intercept' delivery after retries, found none")
	}
	if rec.Success {
		t.Error("delivery to an always-500 stub should be recorded as success=false")
	}
	eq(t, rec.Attempts, retryMax, "recorded attempts should equal retry_max")
}
