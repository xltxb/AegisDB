package bootstrap

// 工单执行前的**复判**,看的必须是即将下发的每一条语句。
//
// 下发前再判一次,是因为工单可能在待执行状态里放了很久:字典、实例归属、发起人的
// 角色都可能变过。而这次复判原先把工单正文整个交给判定引擎,引擎不拆句、只认首
// 动词 —— 于是 `SELECT 1; DROP TABLE t` 复判成一次 select,规则变严之后照样放行,
// 随后执行那一步却是逐条真下发的。判的是一条,跑的是两条。
//
// 终端提交那一路早就按"拆句 + 取最严"判了(A1/ER3),两条路必须给出同一个答案。

import (
	"net/http"
	"testing"
)

// denyDDLOnProd 把这个角色在 PROD 上的 DDL 能力关掉 —— 用来扮演"工单躺着的这段
// 时间里规则变严了"。
func (a *testApp) denyDDLOnProd(token string) {
	a.t.Helper()
	r := a.do(http.MethodPut, "/api/v1/roles/"+itoa(a.roleIDByCode(token, "admin"))+"/capabilities", token,
		map[string]any{"matrix": map[string]map[string]string{"ddl": {"prod": "deny"}}})
	if r.Code != 0 {
		a.t.Fatalf("收紧 PROD 的 DDL 能力: code=%d msg=%s", r.Code, r.Msg)
	}
}

// 批量工单:首句无害,尾句是 DROP。复判必须看见尾句。
func TestApprovedBatch_ReJudgementReadsEveryStatement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, prod := app.sameFileConns(admin, "rejudge-batch")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_smuggle (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	ap := app.interceptedTicket(admin, prod, `SELECT 1; DROP TABLE t_smuggle`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	app.denyDDLOnProd(admin) // 规则变了:这个角色从此不能在 PROD 上做 DDL

	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("复判只读了首句 SELECT,整批照样下发 —— 只读角色靠一张批过的单子删掉了生产表")
	}
	if !app.tableExists(admin, dev, "t_smuggle") {
		t.Error("表已经没了 —— 复判那一层等于不存在")
	}
}

// 前导分隔符是同一个洞的另一种形态:`;DROP …` 解析不出首动词,未知动词落进读维度。
func TestApprovedBatch_LeadingSeparatorIsNormalisedBeforeReJudgement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, prod := app.sameFileConns(admin, "rejudge-sep")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_sep (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	ap := app.interceptedTicket(admin, prod, `;DROP TABLE t_sep`)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	app.denyDDLOnProd(admin)

	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("一个前导分号就让复判读不出动词,DROP 落进了读维度")
	}
	if !app.tableExists(admin, dev, "t_sep") {
		t.Error("表已经没了 —— 复判那一层等于不存在")
	}
}
