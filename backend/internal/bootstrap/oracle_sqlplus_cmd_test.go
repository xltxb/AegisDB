package bootstrap

// SQL*Plus 客户端命令走完整条 HTTP 链(sqlplus-compat 第一批):判定 → 执行器 → 回话。
// 拒绝 / 忽略 / PROMPT 不需要库;SHOW ERRORS 要真实的 Oracle(AEGISDB_TEST_ORACLE)。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestOracleSQLPlusClientCommands_EndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "ora-sqlplus")

	if rc := app.riskCheck(admin, cid, "conn scott/tiger@orcl"); rc.Action != "deny" {
		t.Errorf("CONNECT 应判拒绝,实际 %+v", rc)
	}
	r, o := execTerm(t, app, admin, cid, "conn scott/tiger@orcl", false)
	if r.Code == 0 && !strings.Contains(o.Output, "不支持") {
		t.Errorf("CONNECT 不该被执行,实际 code=%d %+v", r.Code, o)
	}

	for _, sql := range []string{"PROMPT 开始建表", "REM 注释", "define owner = APP", "col x format a10", "SHOW ERRORS"} {
		if rc := app.riskCheck(admin, cid, sql); rc.Action != "allow" {
			t.Errorf("%q 应放行,实际 %+v", sql, rc)
		}
	}
	if r, o := execTerm(t, app, admin, cid, "PROMPT 开始建表", false); r.Code != 0 || o.Output != "开始建表" {
		t.Errorf("PROMPT 应原样回话,实际 code=%d %+v", r.Code, o)
	}

	// 一段脚本:行命令按行切开,连同里面的 CONNECT,整段判拒绝 —— 不会有一部分先跑掉
	if rc := app.riskCheck(admin, cid, "PROMPT 开始\nREM x\nconn a/b\nSELECT 1 FROM DUAL;"); rc.Action != "deny" {
		t.Errorf("含 CONNECT 的脚本应整段拒绝,实际 %+v", rc)
	}
}

// SHOW ERRORS <类型> <名字> 在真实 Oracle 上读回编译错误;修好之后查出零行。
func TestOracleShowErrors_RealOracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ora-showerr", "engine": "Oracle 23ai", "host": p[0], "env": "dev", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	// 建一个编译失败的过程(建完回读 all_errors,这条本身报失败 —— 见 oracle_create_errors.go)
	execTerm(t, app, admin, c.ID, "CREATE OR REPLACE PROCEDURE aegisdb_showerr_p IS BEGIN no_such_thing; END;", false)
	defer execTerm(t, app, admin, c.ID, "DROP PROCEDURE aegisdb_showerr_p", false)

	type rows struct {
		Columns []string   `json:"columns"`
		Data    [][]string `json:"data"`
	}
	read := func() rows {
		r, _ := execTerm(t, app, admin, c.ID, "SHOW ERRORS PROCEDURE aegisdb_showerr_p", false)
		if r.Code != 0 {
			t.Fatalf("SHOW ERRORS: code=%d msg=%s", r.Code, r.Msg)
		}
		var o rows
		_ = json.Unmarshal(r.Data, &o)
		return o
	}
	got := read()
	if strings.Join(got.Columns, ",") != "LINE/COL,ERROR" || len(got.Data) == 0 {
		t.Fatalf("应读回编译错误,实际 %+v", got)
	}
	if !strings.Contains(strings.Join(got.Data[0], " "), "NO_SUCH_THING") && !strings.Contains(strings.Join(got.Data[0], " "), "PLS-") {
		t.Errorf("错误内容不对: %+v", got.Data)
	}

	execTerm(t, app, admin, c.ID, "CREATE OR REPLACE PROCEDURE aegisdb_showerr_p IS BEGIN NULL; END;", false)
	if got := read(); len(got.Data) != 0 {
		t.Errorf("修好之后应查出零行,实际 %+v", got.Data)
	}
}

// WHENEVER SQLERROR / EXIT 在终端批量执行里(真实 Oracle,sqlplus-compat 07)。
func TestOracleWheneverExit_RealOracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ora-whenever", "engine": "Oracle 23ai", "host": p[0], "env": "dev", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	execTerm(t, app, admin, c.ID, "BEGIN EXECUTE IMMEDIATE 'DROP TABLE aegisdb_whenever_t'; EXCEPTION WHEN OTHERS THEN NULL; END;", false)
	execTerm(t, app, admin, c.ID, "CREATE TABLE aegisdb_whenever_t (id NUMBER)", false)
	defer execTerm(t, app, admin, c.ID, "DROP TABLE aegisdb_whenever_t", false)
	count := func() string {
		rr, _ := execTerm(t, app, admin, c.ID, "SELECT COUNT(*) AS N FROM aegisdb_whenever_t", false)
		var d struct {
			Data [][]string `json:"data"`
		}
		_ = json.Unmarshal(rr.Data, &d)
		if len(d.Data) == 0 {
			return "?"
		}
		return d.Data[0][0]
	}

	// 默认遇错即停:第 2 条失败,第 3 条不执行
	_, o := execTerm(t, app, admin, c.ID, "INSERT INTO aegisdb_whenever_t VALUES (1);\nINSERT INTO no_such_table VALUES (1);\nINSERT INTO aegisdb_whenever_t VALUES (2);", false)
	if !strings.Contains(o.Output, "失败") || count() != "1" {
		t.Fatalf("默认应停在失败处,实际 output=%q count=%s", o.Output, count())
	}

	// WHENEVER SQLERROR CONTINUE:失败的跳过,后面的照跑;EXIT 之后的不执行
	_, o = execTerm(t, app, admin, c.ID, "WHENEVER SQLERROR CONTINUE\nINSERT INTO no_such_table VALUES (1);\nINSERT INTO aegisdb_whenever_t VALUES (3);\nEXIT\nINSERT INTO aegisdb_whenever_t VALUES (4);", false)
	if !strings.Contains(o.Output, "第 4 条 EXIT 处结束") || !strings.Contains(o.Output, "1 条失败") {
		t.Errorf("结尾说明不对: %q", o.Output)
	}
	if got := count(); got != "2" {
		t.Errorf("应插入 1 和 3 两行(4 在 EXIT 之后),实际 %s", got)
	}
}

// 绑定变量:标签页带着变量来,PL/SQL 改了带回去;查询 / DML 按值绑定;值绝不进 SQL 文本(sqlplus-compat 06)。
func TestOracleBindVariables_RealOracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ora-binds", "engine": "Oracle 23ai", "host": p[0], "env": "dev", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	type bind struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
		Null  bool   `json:"null"`
	}
	type out struct {
		Output  string     `json:"output"`
		Columns []string   `json:"columns"`
		Data    [][]string `json:"data"`
		Binds   []bind     `json:"binds"`
	}
	exec := func(sql string, binds []bind) out {
		t.Helper()
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": c.ID, "sql": sql, "binds": binds})
		if r.Code != 0 {
			t.Fatalf("%q: code=%d msg=%s", sql, r.Code, r.Msg)
		}
		var o out
		_ = json.Unmarshal(r.Data, &o)
		return o
	}
	vars := []bind{{Name: "N", Type: "NUMBER", Value: "5"}, {Name: "S", Type: "VARCHAR2(40)", Null: true}}

	// EXEC :n := … (翻成 PL/SQL,IN OUT 读回)
	o := exec("EXEC :n := :n * 2 + 1", vars)
	if len(o.Binds) != 2 || o.Binds[0].Value != "11" {
		t.Fatalf("PL/SQL 应把 n 改成 11,实际 %+v", o)
	}
	vars = []bind{{Name: "N", Type: "NUMBER", Value: o.Binds[0].Value}, {Name: "S", Type: "VARCHAR2(40)", Null: true}}
	o = exec("BEGIN :s := 'hello ' || :n; END;", vars)
	if o.Binds[1].Value != "hello 11" || o.Binds[1].Null {
		t.Fatalf("VARCHAR2 OUT: %+v", o.Binds)
	}

	// 查询:按值绑定
	o = exec("SELECT :n + 1 AS X FROM dual", vars)
	if len(o.Data) != 1 || o.Data[0][0] != "12" {
		t.Fatalf("SELECT :n + 1: %+v", o)
	}
	// 值里写着 SQL 也只是一个字符串
	evil := []bind{{Name: "S", Type: "VARCHAR2(60)", Value: "x'); DROP TABLE dual; --"}}
	o = exec("SELECT LENGTH(:s) AS L FROM dual", evil)
	if len(o.Data) != 1 || o.Data[0][0] != "24" {
		t.Fatalf("注入的值应当按字符串处理: %+v", o)
	}
	// 字符串字面量里的 :n 不是绑定
	o = exec("SELECT ':n' AS V FROM dual", vars)
	if len(o.Data) != 1 || o.Data[0][0] != ":n" {
		t.Fatalf("字面量里的 :n: %+v", o)
	}

	// DML:按值写入
	exec("BEGIN EXECUTE IMMEDIATE 'DROP TABLE aegisdb_bind_t'; EXCEPTION WHEN OTHERS THEN NULL; END;", nil)
	exec("CREATE TABLE aegisdb_bind_t (id NUMBER, name VARCHAR2(40))", nil)
	defer exec("DROP TABLE aegisdb_bind_t", nil)
	exec("INSERT INTO aegisdb_bind_t VALUES (:n, :s)", []bind{{Name: "N", Type: "NUMBER", Value: "7"}, {Name: "S", Type: "VARCHAR2(40)", Value: "seven"}})
	o = exec("SELECT id || '-' || name AS V FROM aegisdb_bind_t", nil)
	if len(o.Data) != 1 || o.Data[0][0] != "7-seven" {
		t.Fatalf("DML 绑定: %+v", o)
	}

	// 批量(粘贴的一段脚本):VARIABLE 声明 → EXEC 赋值 → 后面的语句看到新值
	o = exec("VARIABLE r VARCHAR2(20)\nEXEC :r := 'from batch';\nINSERT INTO aegisdb_bind_t VALUES (8, :r);", nil)
	var r8 []bind
	for _, b := range o.Binds {
		if b.Name == "R" {
			r8 = append(r8, b)
		}
	}
	if len(r8) != 1 || r8[0].Value != "from batch" {
		t.Fatalf("批量里声明的变量应带回来: %+v", o)
	}
	o = exec("SELECT name AS V FROM aegisdb_bind_t WHERE id = 8", nil)
	if len(o.Data) != 1 || o.Data[0][0] != "from batch" {
		t.Fatalf("批量里后一条应看到 EXEC 赋的值: %+v", o)
	}
	// DML 的 RETURNING … INTO :n:读回
	o = exec("INSERT INTO aegisdb_bind_t VALUES (9, 'r') RETURNING id INTO :n", []bind{{Name: "N", Type: "NUMBER", Null: true}})
	if len(o.Binds) != 1 || o.Binds[0].Value != "9" {
		t.Fatalf("RETURNING INTO 应读回 9: %+v", o)
	}
}

// 2026-10-28:脚本里不带分号的 SET HEAD OFF 曾把下一条语句吞掉(既不执行也不报错)。
func TestOracleSetAbbrevDoesNotSwallowNext_RealOracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ora-sethead", "engine": "Oracle 23ai", "host": p[0], "env": "dev", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	execTerm(t, app, admin, c.ID, "BEGIN EXECUTE IMMEDIATE 'DROP TABLE aegisdb_sethead_t'; EXCEPTION WHEN OTHERS THEN NULL; END;", false)
	execTerm(t, app, admin, c.ID, "CREATE TABLE aegisdb_sethead_t (id NUMBER)", false)
	defer execTerm(t, app, admin, c.ID, "DROP TABLE aegisdb_sethead_t", false)

	_, o := execTerm(t, app, admin, c.ID, "SET HEAD OFF\nINSERT INTO aegisdb_sethead_t VALUES (1);\nset lin 120\nINSERT INTO aegisdb_sethead_t VALUES (2);", false)
	if !strings.Contains(o.Output, "共 4 条语句") {
		t.Fatalf("应按 4 条执行,实际 %q", o.Output)
	}
	rr, _ := execTerm(t, app, admin, c.ID, "SELECT COUNT(*) AS N FROM aegisdb_sethead_t", false)
	var d struct {
		Data [][]string `json:"data"`
	}
	_ = json.Unmarshal(rr.Data, &d)
	if len(d.Data) != 1 || d.Data[0][0] != "2" {
		t.Fatalf("两条 INSERT 都应执行,实际 %+v", d.Data)
	}
}
