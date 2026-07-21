package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// configureWebhook points the gateway's webhook at the given stub endpoint.
func (a *testApp) configureWebhook(token, endpoint, secret, events string, retryMax int) {
	a.t.Helper()
	r := a.do(http.MethodPut, "/api/v1/settings/webhook", token, map[string]any{
		"endpoint": endpoint, "secret": secret, "events": events,
		"retryMax": retryMax, "enabled": true,
	})
	eq(a.t, r.Code, 0, "save webhook response code")
}

// US#46: every delivery authenticates with Authorization: Bearer <token>, where
// the token is the configured webhook secret. We point the webhook at a capturing
// stub, fire the synchronous test event, and verify the header the receiver saw.
func TestWebhook_TestSendCarriesBearerToken(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	received := make(chan struct{}, 1)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const secret = "vela-shared-secret"
	app.configureWebhook(token, stub.URL, secret, "intercept,approve,exec", 3)

	r := app.do(http.MethodPost, "/api/v1/settings/webhook/test", token, nil)
	eq(t, r.Code, 0, "webhook test-send response code")

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("stub receiver never got the test event")
	}

	mu.Lock()
	defer mu.Unlock()
	eq(t, gotAuth, "Bearer "+secret, "delivery should carry Authorization: Bearer <token>")
}

// US#47: a delivery that keeps failing is retried, but no more than retry_max
// times. A stub that always 500s lets us count attempts for a single event.
func TestWebhook_FailedDeliveryRetriesAreBoundedByRetryMax(t *testing.T) {
	var attempts int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const retryMax = 2
	app.configureWebhook(token, stub.URL, "s3cr3t", "intercept", retryMax)

	// Trigger exactly one "intercept" event: a PROD high-risk command.
	prodConn := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "retry test",
	})
	eq(t, r.Code, 42200, "PROD DROP should intercept (and fire the webhook)")

	// Wait until the retries reach the bound (backoff is 1s, 2s between tries).
	deadline := time.Now().Add(8 * time.Second)
	for atomic.LoadInt32(&attempts) < retryMax && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	// Give any (erroneous) extra attempt a chance to land, then assert the bound.
	time.Sleep(1500 * time.Millisecond)
	if got := atomic.LoadInt32(&attempts); got != retryMax {
		t.Errorf("expected exactly retry_max=%d delivery attempts, got %d", retryMax, got)
	}
}
