package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	app.configureWebhook(token, stub.URL, "secret", "intercept,approve,exec", 3)

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
	app.configureWebhook(token, stub.URL, "secret", "intercept", 3)

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
	app.configureWebhook(token, stub.URL, "s3cr3t", "intercept", retryMax)

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
