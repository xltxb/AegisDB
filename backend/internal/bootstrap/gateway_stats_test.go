package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// The gateway status pill uses real measured request latency, not a hard-coded
// number: after some requests the stats endpoint reports online + a p50 backed
// by actual samples.
func TestGatewayStats_RealLatency(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	for i := 0; i < 6; i++ {
		app.do(http.MethodGet, "/api/v1/connections", token, nil)
	}
	r := app.do(http.MethodGet, "/api/v1/gateway/stats", token, nil)
	eq(t, r.Code, 0, "stats code")

	var s struct {
		Online  bool    `json:"online"`
		P50Ms   float64 `json:"p50Ms"`
		Samples int     `json:"samples"`
	}
	if err := json.Unmarshal(r.Data, &s); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if !s.Online {
		t.Error("gateway should report online")
	}
	if s.Samples < 1 {
		t.Errorf("expected real latency samples, got %d", s.Samples)
	}
	if s.P50Ms < 0 {
		t.Errorf("p50 should be a real non-negative latency, got %v", s.P50Ms)
	}
}

func gatewayIntercepts(app *testApp, token string) int64 {
	app.t.Helper()
	r := app.do(http.MethodGet, "/api/v1/gateway/stats", token, nil)
	eq(app.t, r.Code, 0, "gateway stats code")
	var s struct {
		Intercepts int64 `json:"intercepts"`
	}
	_ = json.Unmarshal(r.Data, &s)
	return s.Intercepts
}

// The rules-page "hits" stat is backed by real PROD interceptions, not a demo
// number: intercepting a PROD high-risk command increases the reported count.
func TestGatewayStats_ReportsRealProdInterceptions(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	before := gatewayIntercepts(app, token)

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "cleanup",
	})
	eq(t, r.Code, resp.CodeIntercepted, "PROD DROP intercepted")

	after := gatewayIntercepts(app, token)
	if after <= before {
		t.Errorf("intercept count should increase after a PROD interception: before=%d after=%d", before, after)
	}
}
