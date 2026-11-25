package bootstrap

// 一张工单最后怎么样了,以**库那边收没收下**为准。
//
// 执行结果原先只存了输出和行数。失败的原因确实写在输出里 —— "· 数据库执行失败: …"
// —— 但没有任何一列说那段文字是一次失败,于是列表上一条跑挂的 DROP 和一条跑成的长
// 得一模一样,都是"已通过"。想分辨就只能去解析那段文字,而那是猜。
//
// 下面这一组用的是**真实的 sqlite 目标库**,不是模拟执行器:模拟执行器永远成功,
// 拿它测"失败会不会被记下来"等于什么都没测。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// apOutcome 是这一组关心的那几列。共用的 apWithSteps 刻意只带 id/apNo/steps,
// 别的用例靠它;在这里另起一个视图,比把字段往那个共享结构上堆要清楚。
type apOutcome struct {
	ApNo       string  `json:"apNo"`
	Status     string  `json:"status"`
	ExecStatus string  `json:"execStatus"`
	ExecutedAt *string `json:"executedAt"`
	Result     string  `json:"result"`
}

// runOwnTicket 走完一整趟:发起 → 被拦 → 通过 → 发起人自己执行,返回执行完的工单。
func (a *testApp) runOwnTicket(initiator string, connID int64, sql string) apOutcome {
	a.t.Helper()
	ap := a.interceptedTicket(initiator, connID, sql)
	approver := a.login("zhangwei@vela.io", "vela123")
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil); r.Code != 0 {
		a.t.Fatalf("审批通过: code=%d msg=%s", r.Code, r.Msg)
	}
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", initiator, nil); r.Code != 0 {
		a.t.Fatalf("发起人执行: code=%d msg=%s", r.Code, r.Msg)
	}
	r := a.do(http.MethodGet, "/api/v1/approvals?ap="+ap.ApNo, initiator, nil)
	var page struct {
		Items []apOutcome `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil || len(page.Items) == 0 {
		a.t.Fatalf("取回工单 %s 失败: err=%v data=%s", ap.ApNo, err, string(r.Data))
	}
	return page.Items[0]
}

// 目标库拒绝了这条语句 —— 工单要说自己失败了。
func TestApprovalExec_FailureIsRecordedOnTheTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, prod := app.sameFileConns(admin, "exec-status-fail")

	// 删一张不存在的表:sqlite 会实打实地报错,而且什么也没改动。
	after := app.runOwnTicket(admin, prod, `DROP TABLE t_never_existed`)

	if after.ExecStatus != "failed" {
		t.Errorf("目标库拒绝了这条语句,工单的执行状态应为 failed,实际 %q", after.ExecStatus)
	}
	if after.ExecutedAt == nil {
		t.Error("跑挂了也是跑过了 —— executed_at 仍要落下,否则这张单会重新变成可执行")
	}
	// 失败的理由要留在工单上,详情页正是靠它回答"为什么没生效"。
	if !strings.Contains(after.Result, "失败") {
		t.Errorf("执行结果里应当留下失败原因,实际 %q", after.Result)
	}
}

// 跑成了就是 success —— 不能因为"有结果就算成功"而把两种情况混成一种。
func TestApprovalExec_SuccessIsRecordedOnTheTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, prod := app.sameFileConns(admin, "exec-status-ok")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_ok (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	after := app.runOwnTicket(admin, prod, `DROP TABLE t_ok`)

	if after.ExecStatus != "success" {
		t.Errorf("命令跑成了,工单的执行状态应为 success,实际 %q", after.ExecStatus)
	}
	if app.tableExists(admin, dev, "t_ok") {
		t.Error("前提不成立:这一趟本该真的把表删掉")
	}
}

// 执行失败不会把一张已批准的工单变回没批准。
//
// status 记的是**人的决定**,exec_status 记的是那一次下发的结果。把失败写进 status,
// 等于让一次执行事故改写一次审批 —— 而按状态筛选、能不能执行,走的都是 status。
func TestApprovalExec_FailureDoesNotUnapproveTheTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, prod := app.sameFileConns(admin, "exec-status-keep")

	after := app.runOwnTicket(admin, prod, `DROP TABLE t_also_never_existed`)

	if after.Status != "approved" {
		t.Errorf("审批结论应当仍是 approved,实际 %q", after.Status)
	}
	// 而且它依然出现在"已通过"这一筛里 —— 总览的"待执行"就是按这个筛取数的。
	page := app.listApprovals(admin, "approved")
	found := false
	for _, it := range page.Items {
		if it.ApNo == after.ApNo {
			found = true
		}
	}
	if !found {
		t.Error("跑挂的工单从 approved 这一筛里消失了 —— 审批结论被执行结果改写了")
	}
}
