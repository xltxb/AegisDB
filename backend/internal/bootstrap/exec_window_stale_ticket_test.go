package bootstrap

// 改过的窗口,签在旧定义上的那个字不算数。
//
// 改一个窗口会把它打回待审批并另建一张单 —— 这一步早就有了。但旧的那张单还留在
// 待办里,而批准它的那条路只认窗口 ID,不认这张单是不是这个窗口**当前**那张:
// 审批人翻到旧单点了通过,改后的时间段和库就直接生效了。他看到的是 02:00-04:00,
// 打开的是 02:00-06:00。删掉窗口同样会留下这么一张指向已删定义的孤儿单。
//
// 所以两头都要堵:改/删窗口时把旧单作废,批准时校验单与窗口确实互相指着对方。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// approvalStatusByNo 读一张单此刻的状态。
func (a *testApp) approvalStatusByNo(token, apNo string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?ap="+apNo, token, nil)
	var page struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("approvals decode: %v", err)
	}
	if len(page.Items) != 1 {
		a.t.Fatalf("按单号查 %s 应恰好一行,实际 %d", apNo, len(page.Items))
	}
	return page.Items[0].Status
}

// editWindowHours 把窗口的结束时刻往后推,窗口因此回到待审批并另建一张单。
func (a *testApp) editWindowHours(token string, winID, connID int64, database, name string, hours int) {
	a.t.Helper()
	now := time.Now()
	r := a.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), token, map[string]any{
		"name": name, "enabled": true, "connectionId": connID, "database": database,
		"kind":     "once",
		"startsAt": now.Add(-time.Minute).Format(time.RFC3339),
		"endsAt":   now.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339),
		"reason":   "多留几个小时",
	})
	if r.Code != 0 {
		a.t.Fatalf("改窗口: code=%d msg=%s", r.Code, r.Msg)
	}
}

// tryDecideTicket 是 decideWindowTicket 的宽松版:允许这一下被拒 —— 对一张已经
// 作废的单来说,被拒正是对的。
func (a *testApp) tryDecideTicket(apNo string, approve bool) int {
	a.t.Helper()
	approver := a.login("zhangwei@vela.io", "vela123")
	verb := "reject"
	if approve {
		verb = "approve"
	}
	return a.do(http.MethodPost, "/api/v1/approvals/"+itoa(a.approvalIDByNo(apNo))+"/"+verb, approver, nil).Code
}

// 批准旧单,不能把改后的窗口打开。
func TestExecWindow_StaleTicketCannotOpenTheEditedWindow(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const sql = "DROP TABLE orders_2024_q3"
	const targetDB = "orders_db"

	winID, oldApNo := app.applyWindow(token, prod, targetDB, "改前改后")
	app.editWindowHours(token, winID, prod, targetDB, "改前改后", 6) // 窗口回到 pending,另建一张单

	app.tryDecideTicket(oldApNo, true) // 审批人翻到旧单点了通过

	eq(t, app.windowStatus(token, winID), model.WindowPending,
		"旧单上的签字把改后的窗口打开了 —— 他看到的是改前那一版")
	if !app.riskCheckIn(token, prod, sql, targetDB).RequiresApproval {
		t.Error("改后的窗口靠一张签在旧定义上的单生效了")
	}
}

// 改窗口要把旧单一起作废,否则待办里留着一张永远不该被批的单。
func TestExecWindow_EditRetiresTheSupersededTicket(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const targetDB = "orders_db"

	winID, oldApNo := app.applyWindow(token, prod, targetDB, "作废旧单")
	eq(t, app.approvalStatusByNo(token, oldApNo), model.StatusPending, "前置条件:刚提交的单在等审批")

	app.editWindowHours(token, winID, prod, targetDB, "作废旧单", 6)

	if got := app.approvalStatusByNo(token, oldApNo); got == model.StatusPending {
		t.Error("改完窗口,旧单还等在待办里 —— 审批人会去批一张已经不存在的定义")
	}
}

// 删窗口留下的孤儿单同样要作废。
func TestExecWindow_DeleteRetiresItsPendingTicket(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const targetDB = "orders_db"

	winID, apNo := app.applyWindow(token, prod, targetDB, "删掉再说")
	eq(t, app.do(http.MethodDelete, "/api/v1/exec-windows/"+itoa(winID), token, nil).Code, 0, "删窗口")

	if got := app.approvalStatusByNo(token, apNo); got == model.StatusPending {
		t.Error("窗口删了,它的单还挂在待办里 —— 批下去连要开的那扇门都不在了")
	}
}
