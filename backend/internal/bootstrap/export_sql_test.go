package bootstrap

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/testpg"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/sqlutil"
)

// 导出成 SQL 文件:导出的 INSERT 在一张空库里执行一遍,读回来的必须就是原来的数据 ——
// NULL 还是 NULL、空串还是空串、带引号的字符串一字不差。单表查询附带建表语句,所以空库里
// 不用先手工建表。
func TestExportSQL_RoundTripsIntoAnEmptyDatabase(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	connID, _ := app.newPGConn(token, "sqlexp", "dev",
		`CREATE TABLE t_member (id INTEGER PRIMARY KEY, name TEXT, note TEXT)`,
		`INSERT INTO t_member VALUES (1, 'O''Brien', NULL)`,
		`INSERT INTO t_member VALUES (2, '', 'line1
line2')`,
		`INSERT INTO t_member VALUES (3, '张三', 'a;b -- not a comment')`,
	)
	conn := struct{ ID int64 }{connID}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	// 表名不带 schema:PostgreSQL 按 search_path 找到它所在的 schema 去取建表语句(#150)
	r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": conn.ID, "sql": "SELECT id, name, note FROM t_member ORDER BY id", "name": "members", "format": "sql",
	})
	eq(t, r.Code, 0, "submit sql export")
	var j exportJob
	_ = json.Unmarshal(r.Data, &j)
	job := app.waitJob(token, j.ID)
	eq(t, job.Status, "done", "status: "+job.Error)

	var dump strings.Builder
	for _, f := range job.fileParts() {
		body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(f), token)
		b, derr := crypto.ZipDecrypt(body, job.Password)
		if derr != nil {
			t.Fatalf("decrypt %s: %v", f, derr)
		}
		dump.Write(b)
	}
	text := dump.String()
	if !strings.Contains(text, "CREATE TABLE") {
		t.Fatalf("单表导出应附建表语句:\n%s", text)
	}

	out, err := sql.Open("pgx", testpg.Server(t).DSN(newTargetDB(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	for _, st := range sqlutil.SplitStatements(text) {
		if _, err := out.Exec(st); err != nil {
			t.Fatalf("导出的语句执行失败: %v\n%s", err, st)
		}
	}
	rows, err := out.Query(`SELECT id, name, note FROM t_member ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type rec struct {
		id   int
		name string
		note sql.NullString
	}
	var got []rec
	for rows.Next() {
		var x rec
		if err := rows.Scan(&x.id, &x.name, &x.note); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	want := []rec{
		{1, "O'Brien", sql.NullString{}},
		{2, "", sql.NullString{String: "line1\nline2", Valid: true}},
		{3, "张三", sql.NullString{String: "a;b -- not a comment", Valid: true}},
	}
	if len(got) != len(want) {
		t.Fatalf("导回 %d 行,应为 %d 行:\n%s", len(got), len(want), text)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 行不一致:got %+v want %+v", i+1, got[i], want[i])
		}
	}
}

// 联表查询认不出目标表:不填就拒绝,填了就照填的写,但不附建表语句(那是哪张表的结构?)。
func TestExportSQL_JoinNeedsAnExplicitTargetTable(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")
	dev := app.connIDByEnv(token, "dev")
	q := "SELECT a.id, b.id FROM t_a a JOIN t_b b ON a.id = b.id"
	if r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": q, "name": "join", "format": "sql"}); r.Code == 0 || !strings.Contains(r.Msg, "目标表") {
		t.Errorf("联表且未填目标表应被拒: code=%d msg=%s", r.Code, r.Msg)
	}
	if r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": q, "name": "join", "format": "sql", "targetTable": "t_ab; DROP TABLE x"}); r.Code == 0 {
		t.Error("目标表名不是合法标识符,应被拒")
	}
	r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": q, "name": "join", "format": "sql", "targetTable": "t_ab"})
	eq(t, r.Code, 0, "填了目标表名就能提交: "+r.Msg)
}
