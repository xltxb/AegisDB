package bootstrap

// 2026-10 黑盒(工单 01 第 2~5 条):窗口的改 / 删要联动它的审批单;建窗按申请人对实例的
// 访问权校验;一次性窗口的时间要说得通。

import (
	"net/http"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// windowBody 是一个一次性窗口的请求体。
func windowBody(connID int64, db, name string, from, to time.Time) map[string]any {
	return map[string]any{
		"name": name, "enabled": true, "connectionId": connID, "database": db,
		"kind": "once", "startsAt": from.Format(time.RFC3339), "endsAt": to.Format(time.RFC3339),
		"reason": "黑盒用例",
	}
}

// backdateWindow 把一个已建好的窗口挪到过去 —— 建窗口子不再接受落在过去的时段(第 5 条),
// 但"过期的窗口不放行""窗口期之外的不算"仍要测。
func (a *testApp) backdateWindow(id int64, from, to time.Time) {
	a.t.Helper()
	w, err := a.repo.GetExecWindow(id)
	if err != nil {
		a.t.Fatal(err)
	}
	w.StartsAt, w.EndsAt = &from, &to
	if err := a.repo.UpdateExecWindow(w); err != nil {
		a.t.Fatal(err)
	}
}

// 第 2 条:改一个还在等审批的窗口,旧单要作废;批准只认当前定义对应的那张单。
//
// 黑盒里改完之后旧单仍 pending,批准旧单就激活了**修改后**的定义 —— 审批人签的是 1 小时,
// 生效的却是改过的样子。
func TestExecWindow_EditWithdrawsThePendingTicket(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")
	const sql, db = "DROP TABLE orders_2024_q3", "orders_db"

	winID, oldNo := app.applyWindow(l2, prod, db, "改前")
	now := time.Now()
	r := app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), l2,
		windowBody(prod, db, "改后", now.Add(-time.Minute), now.Add(6*time.Hour)))
	eq(t, r.Code, 0, "改窗口")

	old := app.approvalByNoFull(admin, oldNo)
	if old.Status == model.StatusPending {
		t.Fatalf("改窗口之后旧单仍在待审批: %s", old.Status)
	}
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(old.ID)+"/approve", approver, nil); r.Code == 0 {
		t.Fatal("旧单还能被批准 —— 签的是旧定义,生效的却是新定义")
	}
	eq(t, app.windowStatus(l2, winID), model.WindowPending, "旧单不该让窗口生效")
	if !app.riskCheckIn(admin, prod, sql, db).RequiresApproval {
		t.Fatal("窗口不该因为旧单被动过而开始放行")
	}

	// 新单照常:批准即生效。
	w, _ := app.repo.GetExecWindow(winID)
	if w.ApNo == "" || w.ApNo == oldNo {
		t.Fatalf("改完应生成新单,实际 apNo=%q", w.ApNo)
	}
	app.decideWindowTicket(w.ApNo, true)
	eq(t, app.windowStatus(l2, winID), model.WindowApproved, "批准新单后生效")
}

// 第 2 条的另一半:即便旧单因为别的原因还是 pending(并发、存量数据),它的批准也不能
// 让窗口生效 —— 窗口只认自己当前挂着的那张单。
func TestExecWindow_StaleTicketDecisionIsIgnored(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")

	winID, oldNo := app.applyWindow(l2, prod, "orders_db", "存量旧单")
	now := time.Now()
	eq(t, app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), l2,
		windowBody(prod, "orders_db", "存量旧单", now.Add(-time.Minute), now.Add(2*time.Hour))).Code, 0, "改窗口")
	// 模拟存量:把旧单强行拨回 pending
	old := app.approvalByNoFull(admin, oldNo)
	if ok, err := app.repo.ClaimApproval(old.ID, old.Status, model.StatusPending); err != nil || !ok {
		t.Fatalf("前提:拨回旧单失败 ok=%v err=%v (status=%s)", ok, err, old.Status)
	}
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(old.ID)+"/approve", approver, nil); r.Code == 0 {
		t.Error("非当前单的批准应被拒绝")
	}
	eq(t, app.windowStatus(l2, winID), model.WindowPending, "旧单的批准不能让窗口生效")
	eq(t, app.approvalByNoFull(admin, oldNo).Status, model.StatusCancelled, "批不动的旧单顺手撤回,不再躺在待办里")
}

// 第 3 条:删一个还在等审批的窗口,它的单要一起撤回,不能留在待办里。
func TestExecWindow_DeleteWithdrawsThePendingTicket(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")

	winID, apNo := app.applyWindow(l2, prod, "orders_db", "删掉的班车")
	eq(t, app.do(http.MethodDelete, "/api/v1/exec-windows/"+itoa(winID), l2, nil).Code, 0, "删窗口")

	ap := app.approvalByNoFull(admin, apNo)
	eq(t, ap.Status, model.StatusCancelled, "删窗之后单子应撤回")
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil); r.Code == 0 {
		t.Fatal("删掉的窗口的单还能被批准(还会回一句「执行窗口已生效」)")
	}
}

// 第 4 条:够不到的实例不能申请窗口;不存在的实例与够不到的实例回同一个错误。
func TestExecWindow_ApplicantMustReachTheInstance(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")  // 标签 orders / users
	analytics := app.connIDByName(admin, "analytics-ro") // 标签 analytics,l2 够不到
	prod := app.connIDByEnv(admin, "prod")
	now := time.Now()

	missing := app.do(http.MethodPost, "/api/v1/exec-windows", l2,
		windowBody(999999, "x", "不存在的实例", now.Add(-time.Minute), now.Add(time.Hour)))
	denied := app.do(http.MethodPost, "/api/v1/exec-windows", l2,
		windowBody(analytics, "x", "够不到的实例", now.Add(-time.Minute), now.Add(time.Hour)))
	if denied.Code == 0 {
		t.Fatal("l2 对 analytics-ro 没有访问权,不该能为它申请窗口")
	}
	eq(t, denied.Code, missing.Code, "够不到与不存在应回同一个错误码")
	eq(t, denied.Msg, missing.Msg, "够不到与不存在应回同一句话")

	// 改窗口同样校验:把自己的窗口挪到够不到的实例上
	winID, _ := app.applyWindow(l2, prod, "orders_db", "改到别处")
	moved := app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), l2,
		windowBody(analytics, "x", "改到别处", now.Add(-time.Minute), now.Add(time.Hour)))
	if moved.Code == 0 {
		t.Fatal("不该能把窗口改到够不到的实例上")
	}
	eq(t, moved.Code, missing.Code, "改窗口:够不到与不存在同一个错误码")
}

// 第 4 条:回看按访问权校验 —— 够不到那台实例的人,看不到那扇门放行过的语句原文。
func TestWindowStatements_RequireInstanceAccess(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	analytics := app.connIDByName(admin, "analytics-ro")
	id := app.openWindow(admin, analytics, "analytics", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	r := app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(id)+"/statements", l2, nil)
	if r.Code == 0 {
		t.Fatal("l2 够不到 analytics-ro,不该看得到那扇门的回看")
	}
	none := app.do(http.MethodGet, "/api/v1/exec-windows/999999/statements", l2, nil)
	eq(t, r.Code, none.Code, "够不到与不存在应回同一个错误码")
	eq(t, app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(id)+"/statements", admin, nil).Code, 0, "管理员照常可看")
}

// 第 5 条:一次性窗口的结束时间要晚于现在,时长不超过 31 天。
func TestExecWindow_OnceWindowMustBeFutureAndBounded(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")
	now := time.Now()

	for name, span := range map[string][2]time.Time{
		"完全落在过去":   {now.Add(-48 * time.Hour), now.Add(-24 * time.Hour)},
		"长达 400 天": {now.Add(time.Hour), now.Add(400 * 24 * time.Hour)},
		"刚过 31 天":  {now.Add(time.Minute), now.Add(time.Minute + 31*24*time.Hour + time.Hour)},
	} {
		if r := app.do(http.MethodPost, "/api/v1/exec-windows", admin, windowBody(prod, "orders_db", name, span[0], span[1])); r.Code == 0 {
			t.Errorf("%s 的一次性窗口不该被接受", name)
		}
	}
	if r := app.do(http.MethodPost, "/api/v1/exec-windows", admin,
		windowBody(prod, "orders_db", "整 31 天", now.Add(time.Minute), now.Add(time.Minute+31*24*time.Hour))); r.Code != 0 {
		t.Errorf("31 天以内的窗口应当被接受: code=%d msg=%s", r.Code, r.Msg)
	}
}
