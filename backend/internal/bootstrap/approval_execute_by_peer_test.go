package bootstrap

// 已批准待执行的工单,由**同事**接手执行。
//
// 原先只有发起人能按下那一下。现实把它推翻了:变更在凌晨两点批下来,发起人已经下班,
// 值班的同事接不了手 —— 于是这张单要么干等到第二天,要么有人拿发起人的账号去跑,
// 而后者比放开更糟。
//
// 放开的**只有**"必须是发起人"这一条。所以这组测试的重点不是"别人也能跑通",而是
// 放开之后那几道闸仍然按**按下按钮的那个人**算:
//
//   - 能力矩阵按 actor 的角色复判 —— 只读角色不会因为单子批过就能删生产表
//   - 标签授权按 actor 算 —— 够不到那台实例的人既执行不了,也看不见那张单
//     (工单里带着完整 SQL,只挡执行、却让它出现在列表里同样是泄露)
//   - 一张单仍然只跑一次
//
// 少了其中任何一条,这次放开就从"值班同事接手"变成了"批准 = 谁都能跑"。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// approvedOn 在一台 prod 连接上造一张已批准、尚未执行的工单。
func (a *testApp) approvedOn(admin string, conn int64, sql string) int64 {
	a.t.Helper()
	ap := a.interceptedTicket(admin, conn, sql)
	approver := a.login("zhangwei@vela.io", "vela123")
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil); r.Code != 0 {
		a.t.Fatalf("审批通过失败: code=%d msg=%s", r.Code, r.Msg)
	}
	return ap.ID
}

/** 列表里能不能看到这张单,以及服务端说不说它可执行。 */
func (a *testApp) seesApprovalRow(token string, id int64) (seen, canExec bool) {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&status=approved&page=1&pageSize=100", token, nil)
	if r.Code != 0 {
		// 连审批页都进不去(菜单闸)—— 那就更谈不上看见某一张单了。
		return false, false
	}
	var page struct {
		Items []struct {
			ID         int64 `json:"id"`
			CanExecute bool  `json:"canExecute"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("decode approvals: %v", err)
	}
	for _, it := range page.Items {
		if it.ID == id {
			return true, it.CanExecute
		}
	}
	return false, false
}

// 同事(够得到那台实例、能力允许)可以接手执行,而且命令是真的跑了。
func TestApprovedCommand_PeerWithAccessMayExecute(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "peer-run") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_peer_run (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	id := app.approvedOn(admin, conn, `DROP TABLE t_peer_run`)

	// zhangwei 是 DBA 负责人(无标签限制),既不是发起人也不是这张单的执行人。
	peer := app.login("zhangwei@vela.io", "vela123")
	seen, canExec := app.seesApprovalRow(peer, id)
	if !seen || !canExec {
		t.Fatalf("同事看不到这张待执行的单,或按钮不会亮(seen=%v canExecute=%v)", seen, canExec)
	}
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", peer, nil); r.Code != 0 {
		t.Fatalf("同事接手执行被拒:code=%d msg=%s", r.Code, r.Msg)
	}
	// 真的跑了,不只是状态变了。
	if app.tableExists(admin, dev, "t_peer_run") {
		t.Error("执行返回成功,但表还在 —— 命令没有真的下发")
	}
	// 一张单仍然只跑一次。
	if app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", peer, nil).Code == 0 {
		t.Error("同一张工单被执行了两次")
	}
}

// 只读角色不行 —— 能力矩阵按**执行的人**复判。
//
// 这是这次放开最要紧的一条:批准授权的是"这条命令可以跑",不是"任何人都可以绕过
// 访问控制"。少了它,一张批过的 DROP 就成了发给全公司的通行证。
//
// 特意先把实例贴上 ro 够得到的标签,好让它**越过标签这道闸**走到能力矩阵那一步 ——
// 否则这条用例会因为"看不见这台实例"而通过,测的就不是它声称的东西了。
func TestApprovedCommand_ReadOnlyPeerStillDenied(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "peer-ro")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_peer_ro (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), admin,
		map[string]any{"tags": "analytics"}).Code, 0, "贴上 ro 够得到的标签")

	id := app.approvedOn(admin, conn, `DROP TABLE t_peer_ro`)

	ro := app.login("zhaolei@vela.io", "vela123") // 研发只读:PROD 上 ddl = deny
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", ro, nil); r.Code == 0 {
		t.Fatal("只读角色执行了一张已批准的 DROP —— 能力矩阵没有按执行的人复判")
	}
	if !app.tableExists(admin, dev, "t_peer_ro") {
		t.Error("被拒的执行不该真的跑了")
	}
}

// 够不到那台实例的人:执行不了,列表里也看不见。
//
// 两件事必须一起成立。只挡执行、却让单子出现在他的列表里,等于把别人的命令原文摊给
// 一个本来无权看这台实例的人 —— 工单里带着完整 SQL。
func TestApprovedCommand_PeerWithoutAccessNeitherSeesNorRuns(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "peer-notag")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_peer_notag (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	// 贴一个只有 l2 有的标签 —— ro 的标签是 analytics/readonly,够不到。
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), admin,
		map[string]any{"tags": "orders"}).Code, 0, "贴上 ro 够不到的标签")

	id := app.approvedOn(admin, conn, `DROP TABLE t_peer_notag`)

	ro := app.login("zhaolei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", ro, nil); r.Code == 0 {
		t.Error("够不到这台实例的人执行成功了")
	}
	if seen, _ := app.seesApprovalRow(ro, id); seen {
		t.Error("够不到这台实例的人在列表里看到了这张单 —— 工单里带着完整 SQL")
	}
}
