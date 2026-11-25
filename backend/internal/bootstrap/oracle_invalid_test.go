package bootstrap

// Oracle 无效对象的清点与批量重编译端点。
//
// 真正的编译要一个活的 Oracle,这里跑不了(逻辑本身在 gateway 包里用注入的编译函数
// 测过)。这里断言的是它周围那圈约束 —— 批量是不是一条比单个编译更松的通道:
// 引擎不对要拒、越权要拒,以及**没有真实凭据时要明说,而不是回一份空清单**。
//
// 最后那条是这个功能最坏的失败方式:一份空清单读起来是"这个 schema 很干净",
// 而真相是"我根本没看"。

import (
	"net/http"
	"strings"
	"testing"
)

func (a *testApp) invalidObjects(token string, connID int64) apiResp {
	a.t.Helper()
	return a.do(http.MethodGet, "/api/v1/connections/"+itoa(connID)+"/objects/invalid?scope=APP", token, nil)
}

func (a *testApp) recompile(token string, connID int64, body map[string]any) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/connections/"+itoa(connID)+"/objects/recompile", token, body)
}

func TestInvalidObjectsRefusesNonOracleEngine(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.invalidObjects(admin, app.mysqlConnID(admin))
	if r.Code == 0 {
		t.Fatal("listing INVALID objects on a non-Oracle engine must be refused")
	}
	if !strings.Contains(r.Msg, "Oracle") {
		t.Errorf("the refusal should say why, got %q", r.Msg)
	}
}

func TestRecompileRefusesNonOracleEngine(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.recompile(admin, app.mysqlConnID(admin), map[string]any{"scope": "APP"})
	if r.Code == 0 {
		t.Fatal("batch recompile on a non-Oracle engine must be refused")
	}
	if !strings.Contains(r.Msg, "Oracle") {
		t.Errorf("the refusal should say why, got %q", r.Msg)
	}
}

// 模拟连接(无凭据):必须明说,不能回一份"很干净"的空清单。
func TestInvalidObjectsOnSimulatedConnectionSaysSoInsteadOfLookingClean(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-invalid-sim")

	r := app.invalidObjects(admin, oracle)
	if r.Code == 0 {
		t.Fatal("一个没有凭据的实例不该回一份空清单 —— 那读起来就是「没有无效对象」")
	}
	if !strings.Contains(r.Msg, "凭据") {
		t.Errorf("the refusal should point at the missing credentials, got %q", r.Msg)
	}
}

func TestRecompileOnSimulatedConnectionSaysSoInsteadOfPretending(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-recompile-sim")

	r := app.recompile(admin, oracle, map[string]any{"scope": "APP"})
	if r.Code == 0 {
		t.Fatal("a simulated connection must not report a successful recompile")
	}
	if !strings.Contains(r.Msg, "凭据") {
		t.Errorf("the refusal should point at the missing credentials, got %q", r.Msg)
	}
}

// 批量重编译是一批 DDL:和单个编译一样受实例标签范围约束 —— 看不见的实例不能编译。
// 这一条要单独钉住,因为"批量"最容易变成一条绕过原有约束的新通道。
func TestRecompileRespectsInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "ora-recompile-scope")
	outsider := app.login("zhaolei@vela.io", "vela123") // 研发只读,标签范围之外

	if r := app.recompile(outsider, oracle, map[string]any{"scope": "APP"}); r.Code == 0 {
		t.Fatal("a user outside the instance's scope must not batch-recompile on it")
	}
	if r := app.invalidObjects(outsider, oracle); r.Code == 0 {
		t.Fatal("a user outside the instance's scope must not list its INVALID objects")
	}
}
