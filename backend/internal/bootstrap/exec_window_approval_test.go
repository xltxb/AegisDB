package bootstrap

// 执行窗口(「班车」)改为申请 → 审批 → 生效之后,唯一真正要守住的事实是:
//
//	**一张还没被批准的窗口,一行都不放行。**
//
// 这组测试就守这一条,以及它的两个近邻 —— 批准之后确实开始放行;驳回之后仍然不放行。
// 窗口是全系统唯一一处主动放宽闸门的地方,而这次改动把"谁能打开它"从一个人变成了
// 两个人;如果 pending 在判定层被漏掉,这次改动就等于什么都没做,而界面上还写着
// "待审批"。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// applyWindow 提交一个覆盖此刻的一次性窗口申请,返回窗口 id 与它的审批单号。
// 它**不批准** —— 那正是这一组要测的东西。
func (a *testApp) applyWindow(token string, connID int64, database, name string) (int64, string) {
	a.t.Helper()
	now := time.Now()
	r := a.do(http.MethodPost, "/api/v1/exec-windows", token, map[string]any{
		"name": name, "enabled": true, "connectionId": connID, "database": database,
		"kind":     "once",
		"startsAt": now.Add(-time.Minute).Format(time.RFC3339),
		"endsAt":   now.Add(time.Hour).Format(time.RFC3339),
		"reason":   "发版车次",
	})
	if r.Code != 0 {
		a.t.Fatalf("申请执行窗口: code=%d msg=%s", r.Code, r.Msg)
	}
	var w model.ExecWindow
	if err := json.Unmarshal(r.Data, &w); err != nil {
		a.t.Fatalf("decode window: %v", err)
	}
	if w.ID == 0 {
		a.t.Fatalf("窗口没有 id: %s", string(r.Data))
	}
	return w.ID, w.ApNo
}

// windowStatus 读回一个窗口此刻的审批状态。
func (a *testApp) windowStatus(token string, id int64) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/exec-windows", token, nil)
	var ws []model.ExecWindow
	if err := json.Unmarshal(r.Data, &ws); err != nil {
		a.t.Fatalf("decode windows: %v", err)
	}
	for _, w := range ws {
		if w.ID == id {
			return w.Status
		}
	}
	a.t.Fatalf("列表里没有窗口 #%d", id)
	return ""
}

// decideWindowTicket 由**另一个人**(DBA 负责人)决定这张单。
//
// 不打开自审批开关来图省事:这次改动的全部意义就是开门的人和签字的人不是同一个,
// 用自审批跑通的测试证明不了它。
func (a *testApp) decideWindowTicket(apNo string, approve bool) {
	a.t.Helper()
	approver := a.login("zhangwei@vela.io", "vela123")
	id := a.approvalIDByNo(apNo)
	verb := "reject"
	if approve {
		verb = "approve"
	}
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/"+verb, approver, nil); r.Code != 0 {
		a.t.Fatalf("%s %s: code=%d msg=%s", verb, apNo, r.Code, r.Msg)
	}
}

// 待审批的窗口不放行;批准之后放行。这是这次改动的**全部意义**。
func TestExecWindow_PendingDoesNotRelax_ApprovedDoes(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	// 基线不成立的话,后面两步测的就不是窗口。
	eq(t, app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval, true, "开窗前应当需要审批")

	winID, apNo := app.applyWindow(token, prod, targetDB, "发版班车")
	if apNo == "" {
		t.Fatal("申请窗口没有生成审批单 —— 那这扇门仍然是一个人开的")
	}
	eq(t, app.windowStatus(token, winID), model.WindowPending, "刚提交的窗口应当待审批")

	// 关键断言:等审批期间,窗口一行都不放行。
	if !app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Fatal("待审批的窗口就已经在放行了 —— 提交申请的那一刻门就开着,审批形同虚设")
	}

	app.decideWindowTicket(apNo, true)

	eq(t, app.windowStatus(token, winID), model.WindowApproved, "批准之后窗口应当生效")
	if app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Error("批准之后窗口仍未生效 —— 这个功能整个没有用")
	}
}

// 被驳回的窗口永远不放行。
func TestExecWindow_RejectedNeverRelaxes(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	winID, apNo := app.applyWindow(token, prod, targetDB, "被驳回的班车")
	app.decideWindowTicket(apNo, false)

	eq(t, app.windowStatus(token, winID), model.WindowRejected, "驳回之后窗口应当是 rejected")
	if !app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Error("被驳回的窗口仍在放行 —— 驳回没有落到判定层上")
	}
}

// 改动会让一张已批准的窗口回到待审批。
//
// 因为"把 02:00-04:00 改成 02:00-06:00"和"新开一扇 04:00-06:00 的门"是同一件事。
// 不重审的话,审批就只拦得住第一版:批准之后任何人都能把时间段拉长、把库换掉,而
// 那扇门上签的字已经不对应它现在的样子了。
func TestExecWindow_EditReturnsToPending(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	winID, apNo := app.applyWindow(token, prod, targetDB, "先批后改")
	app.decideWindowTicket(apNo, true)
	eq(t, app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval, false, "批准后窗口生效")

	now := time.Now()
	r := app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), token, map[string]any{
		"name": "先批后改", "enabled": true, "connectionId": prod, "database": targetDB,
		"kind":     "once",
		"startsAt": now.Add(-time.Minute).Format(time.RFC3339),
		"endsAt":   now.Add(6 * time.Hour).Format(time.RFC3339), // 把窗口拉长
		"reason":   "多留几个小时",
	})
	eq(t, r.Code, 0, "改窗口")

	eq(t, app.windowStatus(token, winID), model.WindowPending, "改完应当回到待审批")
	if !app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Error("改完还在放行 —— 那审批只拦得住第一版")
	}
}

// 窗口单不能走"手动执行"那条路。
//
// 它的 Command 是一句描述("开启执行窗口「…」"),不是可执行语句;放它过去,网关会
// 把那句中文当 SQL 发给数据库。这和升级单、导出单挡在同一处,理由也是同一个。
func TestExecWindow_ApprovalIsNotManuallyExecutable(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	_, apNo := app.applyWindow(token, prod, "orders_db", "不可执行的班车")
	app.decideWindowTicket(apNo, true)

	id := app.approvalIDByNo(apNo)
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", token, nil); r.Code == 0 {
		t.Fatal("窗口申请单被当成命令执行了 —— 那句「开启执行窗口…」会被发给数据库")
	}
}
