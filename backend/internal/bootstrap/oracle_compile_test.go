package bootstrap

// Oracle 包编译端点。
//
// 真正的编译要一个活的 Oracle,这里跑不了;能确定性验证的是它周围那圈约束 ——
// 这个按钮是不是一条没人看管的执行通道。所以本文件断言的是:引擎不对要拒、
// 对象名注入要拒、不可编译的类型要拒、越权要拒,以及没有真实凭据时要**明说
// 而不是假装成功**。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func (a *testApp) compile(token string, connID int64, body map[string]any) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/connections/"+itoa(connID)+"/objects/compile", token, body)
}

// newOracleConn registers a credential-less Oracle instance. 夹具里没有 Oracle,
// 各用例自建而不是动共享夹具 —— 改夹具会波及别处对实例数量的断言。
func (a *testApp) newOracleConn(adminToken, name string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", adminToken, map[string]any{
		"name": name, "engine": "Oracle 19c", "host": "10.40.0.9", "port": 1521,
		"env": "dev", "policy": "audit-only", "database": "ORCLPDB1", "tags": "sandbox,dev",
	})
	if r.Code != 0 {
		a.t.Fatalf("create oracle connection: code=%d msg=%s", r.Code, r.Msg)
	}
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &conn)
	return conn.ID
}

func (a *testApp) mysqlConnID(token string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	var rows []struct {
		ID     int64  `json:"id"`
		Engine string `json:"engine"`
	}
	_ = json.Unmarshal(r.Data, &rows)
	for _, c := range rows {
		if strings.Contains(strings.ToLower(c.Engine), "mysql") {
			return c.ID
		}
	}
	a.t.Fatal("no MySQL connection in the fixture set")
	return 0
}

func TestCompileRefusesNonOracleEngine(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.compile(admin, app.mysqlConnID(admin), map[string]any{"type": "package", "name": "APP_PKG"})
	if r.Code == 0 {
		t.Fatal("compiling a package on a non-Oracle engine must be refused")
	}
	if !strings.Contains(r.Msg, "Oracle") {
		t.Errorf("the refusal should say why, got %q", r.Msg)
	}
}

// 对象名是拼进 SQL 文本的(Oracle 不能把标识符做成绑定变量),注入必须挡在
// 执行之前 —— 而且要挡在"有没有配凭据"之前,否则一个模拟实例会用无关的理由
// 把注入尝试挡掉,真到了配好凭据的实例上就没人拦了。
func TestCompileRejectsInjectionInObjectName(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-inject")

	r := app.compile(admin, oracle, map[string]any{
		"type": "package", "name": `APP_PKG" COMPILE; DROP TABLE users --`,
	})
	if r.Code == 0 {
		t.Fatal("an injected object name must be refused")
	}
	if !strings.Contains(r.Msg, "标识符") {
		t.Errorf("the refusal should name the problem, got %q", r.Msg)
	}
}

// 不可编译的对象类型(表)要报错,而不是悄悄什么都不做。
func TestCompileRefusesUncompilableType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-type")

	r := app.compile(admin, oracle, map[string]any{"type": "table", "name": "TBL_ORDER"})
	if r.Code == 0 {
		t.Fatal("a table has no compile unit — that must be an error")
	}
	if !strings.Contains(r.Msg, "编译") {
		t.Errorf("the refusal should explain, got %q", r.Msg)
	}
}

// 模拟连接(无凭据)下必须明说编译不了 —— 假装成功是这个功能最坏的失败方式:
// DBA 会以为线上那个 INVALID 的包已经修好了。
func TestCompileOnSimulatedConnectionSaysSoInsteadOfPretending(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-sim")

	r := app.compile(admin, oracle, map[string]any{"type": "package", "name": "APP_PKG"})
	if r.Code == 0 {
		t.Fatal("a simulated connection must not report a successful compile")
	}
	if !strings.Contains(r.Msg, "凭据") {
		t.Errorf("the refusal should point at the missing credentials, got %q", r.Msg)
	}
}

// 编译是 DDL:菜单权限之外,还要受实例标签范围约束 —— 看不见的实例不能编译。
func TestCompileRespectsInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-scope")
	outsider := app.login("zhaolei@vela.io", "vela123") // 研发只读,标签范围之外

	r := app.compile(outsider, oracle, map[string]any{"type": "package", "name": "APP_PKG"})
	if r.Code == 0 {
		t.Fatal("a user outside the instance's scope must not compile on it")
	}
}
