package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// A batch of high-risk statements pasted as ONE command is judged statement by
// statement, but the verdict used to collapse identical rule texts into a single
// line and name only the winner's verb: three DROPs came back as one
// "高危命令字典 · PROD 禁止直接执行" with Command = "DROP". Operators read that as
// "only the first statement hit the rule; the rest were skipped" — the gate had
// caught all three, the report showed one. The rule must name every gated
// statement by position and verb, and the pre-check must say the same thing as
// the exec path so the approval-reason modal and the ticket agree.
func TestExec_BatchRuleNamesEveryHighRiskStatement(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	sql := "DROP TABLE a;\r\nTRUNCATE TABLE b;\r\nDELETE FROM c WHERE id = 1;\r\n"

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": sql, "reason": "batch of three",
	})
	eq(t, r.Code, resp.CodeIntercepted, "three-statement high-risk batch intercepted")
	var d struct {
		Risk string `json:"risk"`
		Rule string `json:"rule"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		t.Fatalf("decode exec data: %v", err)
	}
	eq(t, d.Risk, "high", "batch risk")
	for _, want := range []string{"第1条 DROP", "第2条 TRUNCATE", "第3条 DELETE"} {
		if !strings.Contains(d.Rule, want) {
			t.Errorf("rule does not name %q: rule=%q", want, d.Rule)
		}
	}
}

// Whatever shape a pasted batch arrives in — one line, CRLF lines, trailing
// line comments, a benign leading SELECT, a BEGIN…COMMIT wrapper, a PL/SQL block
// followed by DDL — every statement is judged and the batch is intercepted on
// PROD. This pins the shapes an operator actually pastes, so a splitter change
// that merges or drops a statement fails here rather than in production.
func TestExec_BatchShapesAllIntercepted(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	shapes := map[string]string{
		"oneline":   "DROP TABLE a; DROP TABLE b; DROP TABLE c;",
		"crlf":      "DROP TABLE a;\r\nTRUNCATE TABLE b;\r\nDELETE FROM c;\r\n",
		"comments":  "DROP TABLE a; -- one\nDROP TABLE b; -- two\nDROP TABLE c;",
		"safeFirst": "SELECT 1;\nDROP TABLE b;\nTRUNCATE TABLE c;",
		"txn":       "BEGIN;\nDELETE FROM a WHERE id=1;\nDELETE FROM b WHERE id=2;\nCOMMIT;",
		"plsql":     "BEGIN\n DELETE FROM a;\n DELETE FROM b;\nEND;\n/\nDROP TABLE c;",
	}
	for name, sql := range shapes {
		pre := app.riskCheck(token, prod, sql)
		eq(t, pre.RequiresApproval, true, name+": pre-check demands approval")
		eq(t, pre.Risk, "high", name+": pre-check risk")
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": prod, "sql": sql, "reason": name,
		})
		eq(t, r.Code, resp.CodeIntercepted, name+": exec intercepted")
	}
}
