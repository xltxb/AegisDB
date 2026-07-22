package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// When a command is intercepted, the response must carry the matched rule text so
// the terminal can show WHY it was blocked (dictionary vs capability vs strict),
// instead of a hardcoded label. Observable on the /terminal/exec response.
func TestExec_InterceptResponseCarriesMatchedRule(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "DROP TABLE orders;",
		"reason":       "rule surfacing",
	})
	eq(t, r.Code, resp.CodeIntercepted, "exec PROD DROP response code")

	var data struct {
		Intercepted bool   `json:"intercepted"`
		Rule        string `json:"rule"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("decode exec data: %v", err)
	}
	if !data.Intercepted {
		t.Fatal("expected intercepted=true")
	}
	if strings.TrimSpace(data.Rule) == "" {
		t.Error("expected a non-empty matched rule on the intercept response")
	}
}
