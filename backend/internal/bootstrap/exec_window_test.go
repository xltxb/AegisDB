package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/pkg/resp"
)

// openWindow 建一个此刻正生效的一次性窗口 —— **并批准它**,返回它的 id。
//
// 窗口现在要走审批才生效(见 exec_window_approval_test.go)。这一组测的是判定层
// 拿到一个生效窗口之后怎么算(放宽什么、不放宽什么、按库、过期),所以这里把申请
// 到批准这一段一次走完,让每个用例的开头就是"一扇确实开着的门"。
// 审批由**另一个人**签字,不打开自审批开关 —— 那条路径在这里不该被顺带绕过。
func (a *testApp) openWindow(token string, connID int64, db string, from, to time.Time) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/exec-windows", token, map[string]any{
		"name": "凌晨发车", "enabled": true, "connectionId": connID, "database": db,
		"kind": "once", "startsAt": from.Format(time.RFC3339), "endsAt": to.Format(time.RFC3339),
		"reason": "月度维护",
	})
	if r.Code != 0 {
		a.t.Fatalf("建窗口: code=%d msg=%s", r.Code, r.Msg)
	}
	var w model.ExecWindow
	if err := json.Unmarshal(r.Data, &w); err != nil {
		a.t.Fatalf("decode window: %v", err)
	}
	if w.ApNo == "" {
		a.t.Fatalf("窗口没有生成审批单 —— 那它就是一个人开的")
	}
	a.decideWindowTicket(w.ApNo, true)
	return w.ID
}

// 窗口生效时,本来要审批的中/高风险语句直接执行。
func TestExecWindow_RelaxesApprovalWhileOpen(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db" // 终端里选中的那个库

	// 窗口之前:PROD 上的 DROP 命中高危字典,要审批。
	before := app.riskCheckIn(token, prod, sql, targetDB)
	eq(t, before.RequiresApproval, true, "开窗前应当需要审批")

	id := app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if id == 0 {
		t.Fatal("窗口 id 不该为 0")
	}

	// 窗口之内:同一条语句直接放行,而且预检与执行的口径一致 —— 终端不会先弹审批框
	// 再发现不用审批。
	during := app.riskCheckIn(token, prod, sql, targetDB)
	if during.RequiresApproval {
		t.Errorf("窗口内不该再要审批,实际 action=%s rule=%s", during.Action, during.Command)
	}
	eq(t, during.Action, "allow", "窗口内应放行")
	// 风险等级不降:窗口改的是流程,不是"这条语句有多危险"这个事实。
	eq(t, during.Risk, "high", "风险等级不因窗口而降低")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": sql, "reason": "窗口内执行", "database": targetDB,
	})
	eq(t, r.Code, 0, "窗口内执行不应被拦截")
}

// 关掉窗口,立刻恢复审批。
func TestExecWindow_ClosingRestoresApproval(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	id := app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	eq(t, app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval, false, "窗口内免审批")

	eq(t, app.do(http.MethodDelete, "/api/v1/exec-windows/"+itoa(id), token, nil).Code, 0, "删除窗口")
	eq(t, app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval, true, "窗口一撤,审批立刻回来")
}

// 窗口只免审批,不给权限:能力矩阵判 deny 的仍然 deny。
//
// 这是整个功能的安全边界 —— 否则一个只读角色会因为到了凌晨两点就能写生产库。
func TestExecWindow_NeverOverridesCapabilityDeny(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	const targetDB = "orders_db"
	app.openWindow(admin, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	// 只读角色(ro)在 PROD 上 write=deny。窗口开着也不该让它写。
	ro := app.login("zhaolei@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", ro, map[string]any{
		"connectionId": prod, "sql": "UPDATE orders SET status='x' WHERE id=1", "reason": "试探", "database": targetDB,
	})
	if r.Code == 0 {
		t.Fatal("窗口开着也不该让能力矩阵拒绝的角色执行写操作")
	}
	eq(t, r.Code, resp.CodeForbidden, "应当是能力矩阵拒绝")
}

// 窗口是按库开的:同一台实例上别的库不受影响。
func TestExecWindow_ScopedToOneDatabase(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	// 给一个**别的**库开窗。
	app.openWindow(token, prod, "some_other_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	if !app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Error("给别的库开的窗口不该放行这个库")
	}
}

// 已经过期的窗口不放行。
func TestExecWindow_ExpiredDoesNotOpen(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	const targetDB = "orders_db"
	app.openWindow(token, prod, targetDB, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if !app.riskCheckIn(token, prod, "DROP TABLE orders_2024_q3", targetDB).RequiresApproval {
		t.Error("过期的窗口不该放行")
	}
}

// 写不通的定义要当场拒绝,而不是存下一个永远不开、或开在没预料时间的窗口。
func TestExecWindow_RejectsUnsoundDefinitions(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	base := map[string]any{"name": "x", "enabled": true, "connectionId": prod, "database": "d"}

	bad := []map[string]any{
		{"kind": "recurring", "timezone": "Mars/Olympus", "startMin": 0, "endMin": 60}, // 认不出的时区
		{"kind": "recurring", "timezone": "Asia/Shanghai", "startMin": 120, "endMin": 120}, // 起止相同
		{"kind": "recurring", "timezone": "Asia/Shanghai", "startMin": 0, "endMin": 2000},  // 超出一天
		{"kind": "recurring", "timezone": "Asia/Shanghai", "startMin": 0, "endMin": 60, "weekdays": "8"}, // 没有第八天
		{"kind": "once"}, // 一次性但没给起止
		{"kind": "whenever", "timezone": "Asia/Shanghai"}, // 不认识的模型
	}
	for i, extra := range bad {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		for k, v := range extra {
			body[k] = v
		}
		if r := app.do(http.MethodPost, "/api/v1/exec-windows", token, body); r.Code == 0 {
			t.Errorf("第 %d 个无效定义被接受了: %v", i+1, extra)
		}
	}

	// 库名必填 —— 空库名会让一个窗口悄悄覆盖整台实例。
	if r := app.do(http.MethodPost, "/api/v1/exec-windows", token, map[string]any{
		"name": "x", "enabled": true, "connectionId": prod, "database": "",
		"kind": "recurring", "timezone": "Asia/Shanghai", "startMin": 0, "endMin": 60,
	}); r.Code == 0 {
		t.Error("空库名不该被接受")
	}
}
