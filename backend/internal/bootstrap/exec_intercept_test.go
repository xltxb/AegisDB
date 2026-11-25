package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// Tracer bullet: a high-risk command on a PROD instance must be intercepted by
// the gateway (code 42200) and produce an approval ticket (AP-xxxx) rather than
// executing. This proves the whole path: auth -> menu guard -> three-layer
// engine (capability matrix + risk dictionary) -> approval creation.
func TestExec_ProdHighRiskCommandIsIntercepted(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "DROP TABLE orders;",
		"reason":       "drop legacy table",
	})

	eq(t, r.Code, resp.CodeIntercepted, "exec PROD DROP response code")

	var data struct {
		Intercepted bool   `json:"intercepted"`
		ApprovalNo  string `json:"approvalNo"`
		Risk        string `json:"risk"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("decode exec data: %v", err)
	}
	if !data.Intercepted {
		t.Error("expected intercepted=true on PROD DROP")
	}
	if !strings.HasPrefix(data.ApprovalNo, "AP-") {
		t.Errorf("expected approvalNo like AP-xxxx, got %q", data.ApprovalNo)
	}
	eq(t, data.Risk, "high", "intercept risk level")
}
