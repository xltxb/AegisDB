package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// GLI (灰度) is a first-class connection environment whose risk/capability tier
// mirrors staging (演练UAT). A high-risk DDL on a GLI instance must therefore be
// gated at the *staging* level: intercepted for approval with risk "mid" — not
// the PROD "high" hard-block, and not DEV's free execution. This proves the
// seedGliEnv backfill wired GLI's capability-matrix + risk-dictionary rows.
func TestExec_GliHighRiskIsGatedLikeStaging(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// Create a GLI instance (approve-1 policy, like an ordinary grey-release DB).
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "grey-cluster", "engine": "MySQL 8.0", "host": "10.50.0.9:3306",
		"env": "gli", "policy": "approve-1",
	})
	eq(t, cr.Code, 0, "create GLI connection")
	var conn struct {
		ID    int64  `json:"id"`
		Env   string `json:"env"`
		Layer string `json:"layer"`
	}
	if err := json.Unmarshal(cr.Data, &conn); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	eq(t, conn.Env, "gli", "connection env")
	if conn.Layer != "L2 灰度" {
		t.Errorf("expected GLI layer label 'L2 灰度', got %q", conn.Layer)
	}

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": conn.ID, "sql": "DROP TABLE orders;", "reason": "grey cleanup",
	})
	eq(t, r.Code, resp.CodeIntercepted, "GLI DROP intercepted for approval")

	var data struct {
		Intercepted bool   `json:"intercepted"`
		ApprovalNo  string `json:"approvalNo"`
		Risk        string `json:"risk"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("decode exec data: %v", err)
	}
	if !data.Intercepted {
		t.Error("expected intercepted=true on GLI DROP")
	}
	if !strings.HasPrefix(data.ApprovalNo, "AP-") {
		t.Errorf("expected approvalNo like AP-xxxx, got %q", data.ApprovalNo)
	}
	// Staging tier: DROP is "mid", not PROD's "high".
	eq(t, data.Risk, "mid", "GLI intercept risk level (mirrors staging)")
}
