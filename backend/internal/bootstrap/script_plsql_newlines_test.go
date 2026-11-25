package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 脚本通道执行的必须是脚本里写的那段文本 —— 包括换行。
//
// 事故:`\i PK_AGENT.pck` 显示「脚本执行成功 · 8 条语句」,库里的包却全是 INVALID,
// 编译报 PLS-00103 end-of-file,位置全在第 1 行。脚本通道把每条语句的空白压成一个
// 空格再下发,而整块取出的 PL/SQL 单元是**带着注释**的原文:换行一没,第一个 `--`
// 就把后面整个包体注释掉了。Oracle 照收不误(CREATE OR REPLACE 对编译错误只是留下
// INVALID 对象),于是脚本"成功",包是坏的。
//
// SQLite 上同一件事:CREATE TRIGGER … BEGIN … END; 被整块取出,body 里的 `--`
// 在压平后吞掉后面的 INSERT 和 END,触发器要么建不出来,要么是空的。
func TestScriptExecute_PLSQLBlockKeepsItsNewlines(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn, _ := app.sameFileConns(token, "script-plsql-nl")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set script path")

	for _, ddl := range []string{
		`CREATE TABLE nl_src (id INTEGER)`,
		`CREATE TABLE nl_dst (id INTEGER)`,
	} {
		if r := app.execSQL(token, conn, ddl); r.Code != 0 {
			t.Fatalf("建表 %q: code=%d msg=%s", ddl, r.Code, r.Msg)
		}
	}

	// CRLF,和从 Windows 上传的 .pck 一样。
	script := strings.Join([]string{
		"CREATE TRIGGER nl_sync AFTER INSERT ON nl_src",
		"BEGIN",
		"  -- 同步写一份到 nl_dst",
		"  INSERT INTO nl_dst (id) VALUES (NEW.id);",
		"END;",
	}, "\r\n")
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": conn, "filename": "nl_sync.sql", "content": script,
	})
	if r.Code != 0 {
		t.Fatalf("脚本执行失败: code=%d msg=%s", r.Code, r.Msg)
	}

	if r := app.execSQL(token, conn, `INSERT INTO nl_src (id) VALUES (4242)`); r.Code != 0 {
		t.Fatalf("写源表: code=%d msg=%s", r.Code, r.Msg)
	}
	got := app.execSQL(token, conn, `SELECT id FROM nl_dst`)
	if !strings.Contains(string(got.Data), "4242") {
		t.Fatalf("触发器没有生效 —— 注释吞掉了 body:nl_dst = %s", got.Data)
	}
}

// 扫描结果里的语句是给人看、也是给审批单摘录用的;它和执行的文本必须是同一份,
// 多行的 PL/SQL 块也要原样多行。
func TestScriptScan_PLSQLBlockIsReportedVerbatim(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn, _ := app.sameFileConns(token, "script-plsql-scan")

	body := "CREATE OR REPLACE PACKAGE pk_nl AS\n  -- 说明\n  FUNCTION f RETURN NUMBER;\nEND pk_nl;"
	script := body + "\n/\n"
	r := app.do(http.MethodPost, "/api/v1/scripts/scan", token, map[string]any{
		"connectionId": conn, "filename": "pk_nl.pck", "content": script,
	})
	if r.Code != 0 {
		t.Fatalf("scan: code=%d msg=%s", r.Code, r.Msg)
	}
	var scan struct {
		Total      int `json:"total"`
		Statements []struct {
			SQL string `json:"sql"`
		} `json:"statements"`
	}
	if err := json.Unmarshal(r.Data, &scan); err != nil {
		t.Fatalf("scan decode: %v", err)
	}
	if scan.Total != 1 || len(scan.Statements) != 1 {
		t.Fatalf("一个包规格应是 1 条语句,得到 %d", scan.Total)
	}
	eq(t, scan.Statements[0].SQL, body, "扫描到的语句应与脚本原文逐字相同")
}
