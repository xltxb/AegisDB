package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// auditRow is the GET /audit projection we assert on.
type auditRow struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

// auditRows fetches GET /audit (all risk, default range).
func (a *testApp) auditRows(token string) []auditRow {
	a.t.Helper()
	var rows []auditRow
	if err := json.Unmarshal(a.auditItemsRaw(token, ""), &rows); err != nil {
		a.t.Fatalf("audit decode: %v", err)
	}
	return rows
}

// FR-AUDIT / US#32: an all-safe script executed directly must still leave a full
// audit trail — every statement is recorded as executed, just like a terminal
// command. Observable purely through GET /audit.
//
// 用**真实的 SQLite 连接**跑,而不是种子里那台没有凭据的演示实例。
//
// 从前用的是后者,于是这条用例其实一条语句都没执行过,却断言审计记着 executed ——
// 它自己就是那个谎的受害者。模拟执行现在如实记成 simulated(见 model.ResultSimulated),
// 这条用例才露出来:它想验的是"脚本通道逐条留下审计",而那件事只有在真的执行过的
// 时候才谈得上 executed。
func TestScriptExecute_SafeScriptLeavesAuditTrail(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	devConn, _ := app.sameFileConns(token, "script-audit")

	// 两张真表 —— 脚本里的 SELECT 要真能跑通,否则审计记的是 warn 而不是 executed。
	for _, ddl := range []string{
		`CREATE TABLE gapb_alpha (id INTEGER)`,
		`CREATE TABLE gapb_beta (name TEXT)`,
	} {
		if r := app.execSQL(token, devConn, ddl); r.Code != 0 {
			t.Fatalf("建表 %q: code=%d msg=%s", ddl, r.Code, r.Msg)
		}
	}

	// Upload now requires a configured save path.
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set script path")

	// Two distinctly-named safe SELECTs so we can find them in the audit log.
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": devConn,
		"filename":     "safe.sql",
		"content":      "SELECT id FROM gapb_alpha; SELECT name FROM gapb_beta;",
	})
	eq(t, r.Code, 0, "safe script execute response code")

	rows := app.auditRows(token)
	wantSeen := map[string]bool{"gapb_alpha": false, "gapb_beta": false}
	for _, row := range rows {
		// 建表那两条命令里也含表名,但它们不是 SELECT —— 只认脚本里那两条。
		if !strings.HasPrefix(strings.TrimSpace(row.Command), "SELECT") {
			continue
		}
		for marker := range wantSeen {
			if strings.Contains(row.Command, marker) {
				wantSeen[marker] = true
				eq(t, row.Result, "executed", "audit result for "+marker)
			}
		}
	}
	for marker, seen := range wantSeen {
		if !seen {
			t.Errorf("expected an audit row for safe statement %q, found none", marker)
		}
	}
}
