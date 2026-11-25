package bootstrap

// Oracle 的 SET SERVEROUTPUT ON 与其它 SQL*Plus SET(端到端,经 /terminal/exec)。
//
// 判定、执行、批量拆分、DBMS_OUTPUT 读回这一整条链要真实的 Oracle(AEGISDB_TEST_ORACLE=
// host:port/user/password/service 时运行);SET 不下发、判定放行这两条不需要库,每次都跑。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

type execOut struct {
	Output       string   `json:"output"`
	Intercepted  bool     `json:"intercepted"`
	ServerOutput []string `json:"serverOutput"`
	Columns      []string `json:"columns"`
}

func execTerm(t *testing.T, app *testApp, token string, connID int64, sql string, serverOutput bool) (apiResp, execOut) {
	t.Helper()
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": connID, "sql": sql, "serverOutput": serverOutput,
	})
	var o execOut
	_ = json.Unmarshal(r.Data, &o)
	return r, o
}

// SET 不下发,也不因为"未知动词按写判"被拦 —— 连库都不需要。
func TestOracleSQLPlusSet_NotSentAndNotGated(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "ora-set")
	r, o := execTerm(t, app, admin, cid, "SET SERVEROUTPUT ON", false)
	if r.Code != 0 || o.Intercepted || !strings.Contains(o.Output, "SQL*Plus") {
		t.Fatalf("SET 应直接回一句已忽略,实际 code=%d %+v", r.Code, o)
	}
	rc := app.riskCheck(admin, cid, "SET LINESIZE 200")
	if rc.Action != "allow" {
		t.Errorf("SQL*Plus 的 SET 应判为放行,实际 %+v", rc)
	}
	// Oracle 自己的 SET 语句照常判定
	if rc := app.riskCheck(admin, cid, "SET ROLE ALL"); rc.Action == "" {
		t.Errorf("SET ROLE 应当有正常的判定结果,实际 %+v", rc)
	}
}

func TestOracleServerOutput_EndToEnd(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "ora-real", "engine": "Oracle 23ai", "host": p[0], "env": "dev", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	// 单条:终端带着开关
	_, o := execTerm(t, app, admin, c.ID, "BEGIN DBMS_OUTPUT.PUT_LINE('单条输出'); END;", true)
	if strings.Join(o.ServerOutput, "|") != "单条输出" {
		t.Fatalf("开关开着应读回输出,实际 %+v", o)
	}
	_, o = execTerm(t, app, admin, c.ID, "BEGIN DBMS_OUTPUT.PUT_LINE('x'); END;", false)
	if len(o.ServerOutput) != 0 {
		t.Errorf("开关关着不该有输出,实际 %q", o.ServerOutput)
	}

	// 粘贴的整段:批里的 SET SERVEROUTPUT ON / OFF 对后面的语句生效
	batch := "SET SERVEROUTPUT ON\n" +
		"BEGIN DBMS_OUTPUT.PUT_LINE('a'); END;\n/\n" +
		"SET LINESIZE 200\n" +
		"BEGIN DBMS_OUTPUT.PUT_LINE('b'); END;\n/\n" +
		"SET SERVEROUTPUT OFF\n" +
		"BEGIN DBMS_OUTPUT.PUT_LINE('c'); END;\n/\n"
	r, o = execTerm(t, app, admin, c.ID, batch, false)
	if r.Code != 0 || strings.Join(o.ServerOutput, "|") != "a|b" {
		t.Fatalf("批里开 / 关 SERVEROUTPUT 应只读回 a、b,实际 code=%d msg=%q %+v", r.Code, r.Msg, o)
	}

	// 批中途失败:已经读到的输出照样带回
	r, o = execTerm(t, app, admin, c.ID, "SET SERVEROUTPUT ON\nBEGIN DBMS_OUTPUT.PUT_LINE('before'); RAISE_APPLICATION_ERROR(-20001, 'boom'); END;\n/\n", false)
	if !strings.Contains(o.Output, "boom") || strings.Join(o.ServerOutput, "|") != "before" {
		t.Errorf("中途失败时应带回之前的输出,实际 %+v", o)
	}
}
