package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// 回看一扇门:「它开着的那几个小时里,到底跑了什么」。
//
// 这是免审批窗口事后唯一的凭据 —— 那段时间里没有人签过字,只剩审计行。所以这一组
// 测的不是"列表能不能打开",而是它**会不会少给或多给**:少给一条被放行的高危语句,
// 等于把责任藏起来;多给一条别的库、别的时段的语句,等于栽赃给这扇门。

type windowStmt struct {
	AuditID    int64  `json:"auditId"`
	Actor      string `json:"actor"`
	Command    string `json:"command"`
	Risk       string `json:"risk"`
	Result     string `json:"result"`
	ApprovalNo string `json:"approvalNo"`
	ByWindow   bool   `json:"byWindow"`
}

type windowStmts struct {
	WindowID  int64        `json:"windowId"`
	Items     []windowStmt `json:"items"`
	ByWindow  int          `json:"byWindow"`
	Truncated bool         `json:"truncated"`
}

func (a *testApp) windowStatements(token string, id int64) windowStmts {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(id)+"/statements", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("窗口回看: code=%d msg=%s", r.Code, r.Msg)
	}
	var out windowStmts
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode statements: %v", err)
	}
	return out
}

func hasCommand(items []windowStmt, sql string) *windowStmt {
	for i := range items {
		if items[i].Command == sql {
			return &items[i]
		}
	}
	return nil
}

// 窗口期里跑过的语句都在回看里,而被这扇门放行的那条要认得出来。
func TestWindowStatements_ListsWhatRanAndMarksWhatTheWindowRelaxed(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const targetDB = "orders_db"
	const risky = "DROP TABLE orders_2024_q3"        // 本来要审批,窗口放行
	const plain = "SELECT id FROM orders LIMIT 1"    // 本来就不用审批

	id := app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	for _, sql := range []string{risky, plain} {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": prod, "sql": sql, "reason": "窗口内执行", "database": targetDB,
		})
		eq(t, r.Code, 0, "窗口内执行 "+sql)
	}

	got := app.windowStatements(token, id)
	eq(t, got.WindowID, id, "回看的是这扇门")
	if len(got.Items) < 2 {
		t.Fatalf("窗口期里跑过两条,回看只有 %d 条:%+v", len(got.Items), got.Items)
	}
	dropped := hasCommand(got.Items, risky)
	if dropped == nil {
		t.Fatal("被窗口放行的那条 DROP 不在回看里 —— 这一页就是为它存在的")
	}
	// 放行的判据是执行当时写进哈希链的 Operator,不是这里按时间反推的。
	eq(t, dropped.ByWindow, true, "DROP 应标成「这扇门放行的」")
	eq(t, dropped.Risk, "high", "风险等级照实记,不因窗口而降")
	eq(t, got.ByWindow, 1, "被这扇门放行的条数")

	// 普通 SELECT 也在列表里(它确实在窗口期跑的),但不算这扇门的功劳 ——
	// 两者混为一谈会让人把一扇门的责任看得比实际大。
	sel := hasCommand(got.Items, plain)
	if sel == nil {
		t.Fatal("窗口期里跑过的 SELECT 也该列出来")
	}
	eq(t, sel.ByWindow, false, "低风险语句不是窗口放行的")
}

// 窗口期之外跑的,不算在这扇门头上。
func TestWindowStatements_ExcludesWhatRanOutsideTheWindow(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const targetDB = "orders_db"
	const plain = "SELECT id FROM orders LIMIT 1"

	// 一扇昨天就关上的门。现在跑的语句与它无关。
	// 建窗不再接受落在过去的时段(工单 01 第 5 条):先建,再挪到昨天。
	id := app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	app.backdateWindow(id, time.Now().Add(-25*time.Hour), time.Now().Add(-24*time.Hour))
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": plain, "reason": "窗口之外", "database": targetDB,
	})
	eq(t, r.Code, 0, "窗口外的低风险语句照常执行")

	got := app.windowStatements(token, id)
	if hasCommand(got.Items, plain) != nil {
		t.Fatal("窗口期之外跑的语句被算进了这扇门 —— 那是栽赃")
	}
	eq(t, got.ByWindow, 0, "这扇门没放行过任何东西")
}

// 窗口按库开,回看也按库:同一台实例上别的库跑的语句不该串进来。
func TestWindowStatements_ScopedToTheWindowsDatabase(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const plain = "SELECT id FROM orders LIMIT 1"

	id := app.openWindow(token, prod, "orders_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": plain, "reason": "别的库", "database": "billing_db",
	})
	eq(t, r.Code, 0, "别的库上的低风险语句照常执行")

	if hasCommand(app.windowStatements(token, id).Items, plain) != nil {
		t.Fatal("别的库跑的语句串进了这扇门的回看")
	}
}

// 回看**不按人收窄**:没有监督角色的人也看得见别人在这扇门下跑过的语句原文。
//
// 这扇门免掉的是审批,也就免掉了签字的人 —— 一份只剩"我自己跑的那两条"的回看,读起来
// 和"这扇门没出什么事"一模一样。同时守住另一头:审计页那条规则**没有**被一起放开,
// 同一个人在 /audit 上仍然看不见这条命令。两页分道是刻意的,不是漏改。
func TestWindowStatements_NonOverseerSeesEveryonesCommands(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	const targetDB = "orders_db"
	const risky = "DROP TABLE orders_2024_q3"

	id := app.openWindow(admin, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": risky, "reason": "窗口内执行", "database": targetDB,
	}).Code, 0, "管理员在窗口内执行")

	// L2:进得了班车菜单,但**不是**监督角色(admin/owner/audit)。放开这一页,要服务
	// 的正是他 —— 改动之前他打开回看只看得见自己跑的那几条。
	l2 := app.login("chenhao@vela.io", "vela123")
	got := app.windowStatements(l2, id)
	hit := hasCommand(got.Items, risky)
	if hit == nil {
		t.Fatal("没有监督角色的人看不见别人跑的语句 —— 这扇门放行过什么应当是共同可见的")
	}
	if !hit.ByWindow {
		t.Fatal("那条 DROP 是被这扇门放行的,回看里却没标出来")
	}
	if hit.Actor == "" {
		t.Fatal("看得见语句却看不出是谁跑的 —— 回看要回答的正是这一句")
	}
	eq(t, got.ByWindow, 1, "被放行的条数要把别人跑的那条算进去")

	// 另一头:审计页没有被一起放开。
	for _, row := range app.auditByAction(l2, "").Items {
		if row.Command == risky {
			t.Fatal("审计页的可见性规则被一起放开了 —— 放开的只该是窗口回看这一页")
		}
	}
}

// 谁打得开这一页。内容不再按人收窄之后,门槛就全在这道判断上:
//
//	审计员进得来 —— 查旧账是本职,而默认矩阵里审计角色**没有** execwindow 菜单键,
//	               只认菜单会把唯一一个以查旧账为业的角色挡在门外;
//	只读进不来 —— 既不在班车菜单里,也不是监督角色。他在界面上本来就打不开班车页,
//	             挡的是直接打接口这条路。
//
// 只读角色带着 audit **菜单**键(他看得见审计页,只是被收窄到自己那几行),所以这道
// 门槛必须按角色判、不能按菜单键判 —— 这个测试就是钉这一点的。
func TestWindowStatements_AuditorMayReviewButPlainReadOnlyMayNot(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	const targetDB = "orders_db"
	const risky = "DROP TABLE orders_2024_q3"

	id := app.openWindow(admin, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": risky, "reason": "窗口内执行", "database": targetDB,
	}).Code, 0, "管理员在窗口内执行")

	auditor := app.login("hujun@vela.io", "vela123") // 审计角色:无 execwindow 菜单
	if hasCommand(app.windowStatements(auditor, id).Items, risky) == nil {
		t.Fatal("审计员打不开这扇门的回看 —— 查旧账正是他的本职")
	}

	ro := app.login("zhaolei@vela.io", "vela123") // 只读:无 execwindow,非监督角色
	eq(t, app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(id)+"/statements", ro, nil).Code,
		40300, "只读角色应当被挡在回看之外")
}
