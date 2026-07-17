package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
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
