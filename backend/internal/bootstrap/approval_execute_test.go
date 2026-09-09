package bootstrap

// 审批通过 ≠ 已经执行。
//
// 原先的行为是:审批人一点通过,网关就代替发起人把命令跑了。对**升级单**来说这早就
// 不是这样了(流水线的执行阶段拥有执行,审批只是授权);但终端里那条被拦下来的命令
// 仍然是"批了就跑"。
//
// 这有两个问题:
//   - 命令在**审批人点下去的那一刻**执行,而发起人可能已经不在现场了。一条 DROP
//     在半夜被批准并执行,没有人在看着它。
//   - 审批人按下的是"我同意",不是"现在就跑"。把这两件事绑在一起,等于让审批人
//     替发起人选择了执行时机。
//
// 改成:审批通过 → 通知发起人 → **由发起人自己来执行**。下面这一组把它钉住,
// 其中最要紧的是第一条:通过之后,数据必须还没有变。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// interceptedTicket 提交一条会被拦下来的命令,返回工单。
func (a *testApp) interceptedTicket(token string, connID int64, sql string) apWithSteps {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": connID, "sql": sql, "reason": "执行时机用例",
	})
	var data struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if data.ApprovalNo == "" {
		a.t.Fatalf("这条命令本应被拦成审批工单,实际: code=%d msg=%s data=%s", r.Code, r.Msg, string(r.Data))
	}
	return a.approvalByNo(token, data.ApprovalNo)
}

func (a *testApp) tableExists(token string, connID int64, table string) bool {
	a.t.Helper()
	r := a.execSQL(token, connID, "SELECT name FROM sqlite_master WHERE type='table' AND name='"+table+"'")
	return strings.Contains(string(r.Data), table)
}

// 这条是整件事的核心:审批通过之后,命令**还没有跑**。
func TestApprovedCommand_IsNotExecutedUntilTheInitiatorRunsIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-timing") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_victim (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if !app.tableExists(admin, dev, "t_victim") {
		t.Fatal("前置条件不成立:表应该存在")
	}

	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_victim`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	// ——— 关键断言 ———
	if !app.tableExists(admin, dev, "t_victim") {
		t.Fatal("审批一通过命令就跑了 —— 执行时机应该由发起人决定,而不是审批人点下去的那一刻")
	}

	// 发起人自己执行
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil)
	eq(t, r.Code, 0, "发起人执行")
	if app.tableExists(admin, dev, "t_victim") {
		t.Error("发起人执行后,命令应该真的跑了")
	}
}

// 一张工单只能执行一次。批准是对**一次**执行的授权,不是一张可以反复使用的通行证。
func TestApprovedCommand_ExecutesOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-once") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_once (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_once`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "第一次执行")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("同一张工单执行第二次应被拒 —— 否则一次批准就成了可反复使用的通行证")
	}
}

// 执行的人必须**自己**够得到那台实例。
//
// "只有发起人能执行"这条已经放开了(值班同事要能接手,见
// approval_execute_by_peer_test.go)。放开的只有那一条 —— 访问控制没有跟着松:
// 一个够不到这台实例的人,拿着一张已批准的工单也跑不了。
func TestApprovedCommand_ExecutorMustReachTheInstance(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-who") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_who (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	// 贴一个只读角色够不到的标签(ro 的标签是 analytics/readonly)。
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), admin,
		map[string]any{"tags": "orders"}).Code, 0, "贴标签")
	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_who`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	outsider := app.login("zhaolei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", outsider, nil); r.Code == 0 {
		t.Error("够不到这台实例的人不该能执行这张工单")
	}
	if !app.tableExists(admin, dev, "t_who") {
		t.Error("被拒的执行不该真的跑了")
	}
}

// 还没批、或被驳回的工单,不能执行。
func TestApprovedCommand_RefusesWhenNotApproved(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-state") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_state (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	pending := app.interceptedTicket(admin, conn, `DROP TABLE t_state`)
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(pending.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("待审批的工单不该能执行")
	}

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(pending.ID)+"/reject", approver, nil).Code, 0, "驳回")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(pending.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("被驳回的工单不该能执行")
	}
	if !app.tableExists(admin, dev, "t_state") {
		t.Error("被驳回的命令绝不能跑")
	}
}

// 审批通过时要通知发起人,而且话里要说清**还需要他去执行** —— 一句"已通过"会让人
// 以为事情办完了,然后那条命令就一直挂在那里。
func TestApprovedCommand_NotifiesTheInitiatorToGoRunIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-notify") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_notify (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_notify`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	n, ok := app.waitForNotif(admin, "approval-approved", 5000000000) // 5s
	if !ok {
		t.Fatal("审批通过后应通知发起人")
	}
	if !strings.Contains(n.Title+n.Body, "执行") {
		t.Errorf("通知要说清还需要他去执行,实际: %q / %q", n.Title, n.Body)
	}
}

// 升级单的审批仍旧走流水线:它的执行由执行阶段拥有,不能用这个接口插队执行,
// 否则同一个变更会被应用两次 —— 一次在这里,一次在还以为自己没跑过的流水线里。
func TestReleaseTicket_IsNotExecutableThroughThisPath(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	pid := app.createPipeline(admin, "执行时机流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
	})
	connID := app.connIDByEnv(admin, "staging")
	r := app.submitRelease(admin, map[string]any{
		"title": "升级单不受影响", "pipelineId": pid, "connectionId": connID,
		"sql": "SELECT 1;", "reason": "执行时机用例",
	})
	eq(t, r.Code, 0, "提交升级单")

	// 找到这张发布单产生的审批工单
	list := app.do(http.MethodGet, "/api/v1/approvals?page=1&pageSize=50", admin, nil)
	var page struct {
		Items []struct {
			ID        int64 `json:"id"`
			ReleaseID int64 `json:"releaseId"`
		} `json:"items"`
	}
	_ = json.Unmarshal(list.Data, &page)
	var relTicket int64
	for _, it := range page.Items {
		if it.ReleaseID > 0 {
			relTicket = it.ID
		}
	}
	if relTicket == 0 {
		t.Skip("这条流程没有产生带 releaseId 的审批工单")
	}
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(relTicket)+"/execute", admin, nil); r.Code == 0 {
		t.Error("升级单的审批工单不该能用这个接口执行 —— 它的执行归流水线所有")
	}
}

// 批准的时刻和执行的时刻是两个时刻,不能互相覆盖。
//
// 执行结果原先沿用 SetApprovalResult 写回,而那一条顺手改了 decided_at —— 一张
// 周一批、周三执行的单子,事后看就成了周三才批的。审批时间是这套东西对外交代的
// 一部分(审计、合规回溯都读它),被执行动作改掉是不能接受的。
func TestApprovedCommand_ExecutingDoesNotRewriteWhenItWasApproved(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "exec-times") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_times (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_times`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	decidedBefore := app.approvalTimes(admin, ap.ApNo).DecidedAt
	if decidedBefore == nil {
		t.Fatal("通过之后应记下审批时刻")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "发起人执行")

	after := app.approvalTimes(admin, ap.ApNo)
	if after.DecidedAt == nil || *after.DecidedAt != *decidedBefore {
		t.Errorf("执行不该改写审批时刻:通过时 %v,执行后 %v", decidedBefore, after.DecidedAt)
	}
	if after.ExecutedAt == nil {
		t.Error("执行之后应记下执行时刻")
	}
}

type apTimes struct {
	DecidedAt  *string `json:"decidedAt"`
	ExecutedAt *string `json:"executedAt"`
}

func (a *testApp) approvalTimes(token, apNo string) apTimes {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?ap="+apNo, token, nil)
	var page struct {
		Items []apTimes `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	if len(page.Items) != 1 {
		a.t.Fatalf("按单号查 %s 应恰好一行,实际 %d", apNo, len(page.Items))
	}
	return page.Items[0]
}
