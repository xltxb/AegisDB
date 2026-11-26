package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// \i 脚本逐条输出执行结果(2026-10 用户要求):与 mysql 客户端 source 一样,每条都看得到
// 影响了几行、查出了什么,不只是一句"执行了 N 条"。

type scriptStmtResult struct {
	Pos     int        `json:"pos"`
	SQL     string     `json:"sql"`
	Rows    int        `json:"rows"`
	Output  string     `json:"output"`
	Columns []string   `json:"columns"`
	Data    [][]string `json:"data"`
	Failed  bool       `json:"failed"`
}

func scriptResults(t *testing.T, raw json.RawMessage) []scriptStmtResult {
	t.Helper()
	var body struct {
		Results []scriptStmtResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode results: %v (%s)", err, raw)
	}
	return body.Results
}

func TestScript_ReturnsEachStatementsResult(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.realPGConnIn(admin, "dev", "script-results")
	app.setSettings(admin, map[string]any{"script.savePath": t.TempDir()})

	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": dev, "filename": "four.sql",
		"content": "CREATE TABLE r1 (id INTEGER);\nINSERT INTO r1 VALUES (1), (2), (3);\n-- 查一下\nSELECT id FROM r1 ORDER BY id;\nUPDATE r1 SET id = id WHERE id > 1;\n",
	})
	eq(t, r.Code, 0, "脚本执行: "+r.Msg)
	res := scriptResults(t, r.Data)
	if len(res) != 4 {
		t.Fatalf("应逐条返回 4 条结果,实际 %d: %s", len(res), r.Data)
	}
	for i, x := range res {
		eq(t, x.Pos, i+1, "序号")
		if x.Failed {
			t.Errorf("第 %d 条不该失败: %+v", i+1, x)
		}
	}
	eq(t, res[1].SQL, "INSERT INTO r1 VALUES (1), (2), (3)", "带着语句开头,让人认出是哪一条")
	eq(t, res[2].SQL, "SELECT id FROM r1 ORDER BY id", "语句前的说明注释不当开头")
	eq(t, res[1].Rows, 3, "INSERT 影响 3 行")
	if len(res[2].Columns) != 1 || res[2].Columns[0] != "id" || len(res[2].Data) != 3 || res[2].Data[2][0] != "3" {
		t.Errorf("SELECT 应带回列与 3 行数据: %+v", res[2])
	}
	eq(t, res[3].Rows, 2, "UPDATE 影响 2 行")
}

// 停在某一条时:已执行的那几条和失败的那一条的结果也带回,终端照样逐条打出来。
func TestScript_FailureStillReturnsResultsSoFar(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.realPGConnIn(admin, "dev", "script-results-fail")
	app.setSettings(admin, map[string]any{"script.savePath": t.TempDir()})

	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": dev, "filename": "mixed.sql",
		"content": "CREATE TABLE f_ok (id INTEGER);\nINSERT INTO f_no_such_table VALUES (1);\nCREATE TABLE f_after (id INTEGER);\n",
	})
	if r.Code == 0 {
		t.Fatalf("第 2 条失败,脚本不该报成功")
	}
	res := scriptResults(t, r.Data)
	if len(res) != 2 || res[0].Failed || !res[1].Failed || res[1].Pos != 2 {
		t.Fatalf("应带回第 1 条(成功)与第 2 条(失败),实际 %+v", res)
	}
}
