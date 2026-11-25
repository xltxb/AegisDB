package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 2026-11-12 发布单报障:DDL 单里夹着 UPDATE,外部平台先调 /open/sql-review 预检,只得到 ALGORITHM
// 的报错;DML/DDL 混单要等建单才说,而预检一失败就不建单 —— 用户以为没有这道检查。
// 预检现在也报 DML/DDL 同单,声明了 changeType 时核对内容;措辞与建单时一致。
func TestOpenReview_ReportsMixedDMLAndDDL(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	tok := app.issueClient(admin, "混单预检", "zhangwei@vela.io", []string{model.ScopeReviewCheck})

	type finding struct {
		Code    string `json:"code"`
		Level   string `json:"level"`
		Stmt    int    `json:"stmt"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	}
	check := func(body map[string]any) (passed bool, hit *finding) {
		t.Helper()
		r := app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, body)
		if r.Code != 0 {
			t.Fatalf("预检失败: %d %s", r.Code, r.Msg)
		}
		var out struct {
			Result struct {
				Passed   bool      `json:"passed"`
				Findings []finding `json:"findings"`
			} `json:"result"`
		}
		_ = json.Unmarshal(r.Data, &out)
		for i, f := range out.Result.Findings {
			if f.Code == "release.change_type" {
				return out.Result.Passed, &out.Result.Findings[i]
			}
		}
		return out.Result.Passed, nil
	}

	mixed := "-- 1. 补数据\nUPDATE t_agent_contract SET s = 2 WHERE s IS NULL;\n\n-- 2. 改列\nALTER TABLE t_agent_contract MODIFY COLUMN s tinyint NOT NULL DEFAULT 2 COMMENT 'a; b', ALGORITHM=INPLACE, LOCK=NONE;\n"
	if passed, f := check(map[string]any{"dialect": "mysql", "sql": mixed}); passed || f == nil || f.Level != "error" ||
		!strings.Contains(f.Message, "不允许同时包含 DML(第 1 条)和 DDL(第 2 条)") || f.Stmt != 2 || f.Line != 5 {
		t.Errorf("DML+DDL 同单应报 error 并指向第 2 条 / 第 5 行: passed=%v %+v", passed, f)
	}
	if passed, f := check(map[string]any{"dialect": "mysql", "sql": mixed, "changeType": "ddl"}); passed || f == nil ||
		!strings.Contains(f.Message, "变更类型为 DDL 的升级单不允许 DML 语句:第 1 条(UPDATE)") {
		t.Errorf("声明 DDL 却有 UPDATE 应报: passed=%v %+v", passed, f)
	}
	// 纯 DDL、声明一致:不报;COMMENT 里的分号也不再让 ALGORITHM 规则误报
	pureDDL := "ALTER TABLE t MODIFY COLUMN c varchar(50) NOT NULL DEFAULT '' COMMENT 'x; y', ALGORITHM=INPLACE, LOCK=NONE;"
	if passed, f := check(map[string]any{"dialect": "mysql", "sql": pureDDL, "changeType": "ddl"}); !passed || f != nil {
		t.Errorf("纯 DDL 应通过: passed=%v %+v", passed, f)
	}
	// 只读脚本(审查不是建单):不因"认不出类型"报错
	if _, f := check(map[string]any{"dialect": "mysql", "sql": "SHOW TABLES;"}); f != nil {
		t.Errorf("认不出类型的脚本不该报混单: %+v", f)
	}
	// changeType 写错:40001
	if r := app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{"dialect": "mysql", "sql": pureDDL, "changeType": "dll"}); r.Code == 0 {
		t.Error("非法 changeType 应拒绝")
	}
}

// 控制台的规范审查走同一套。
func TestReviewCheck_ConsoleReportsMixed(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/sql-review/check", tok, map[string]any{
		"dialect": "mysql", "sql": "UPDATE t SET a = 1 WHERE id = 1;\nDROP TABLE x;",
	})
	if r.Code != 0 || !strings.Contains(string(r.Data), `"release.change_type"`) {
		t.Errorf("控制台审查也应报混单: %d %s %s", r.Code, r.Msg, r.Data)
	}
}
