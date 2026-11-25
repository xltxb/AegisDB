package bootstrap

// 撤回一张待审批工单。
//
// 这组守的是**撤回与驳回的分界**,而不只是"能不能撤":
// 审批人如果也能撤回,他就可以把一张本该驳回的工单悄悄抹掉,而记录上看不出有人拒绝
// 过什么。所以审批人走到这条路上必须被挡住,并且被告知他手里的动作是驳回。

import (
	"net/http"
	"strings"
	"testing"
)

func (a *testApp) cancel(token string, id int64) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/cancel", token, nil)
}

// 发起人可以撤回自己的待审批工单;撤回之后它不再是待审批,也不能再被批准。
func TestApprovalCancel_InitiatorWithdrawsOwnPending(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("chenhao@vela.io", "vela123") // DBA L2
	prod := app.connIDByEnv(initiator, "prod")

	apNo := app.interceptOn(initiator, prod, "DROP TABLE t_cancel_mine")
	if apNo == "" {
		t.Fatal("这条语句本该被拦成工单")
	}
	id := app.approvalIDByNo(apNo)

	eq(t, app.cancel(initiator, id).Code, 0, "发起人撤回自己的单")
	eq(t, app.approvalRow(initiator, apNo).Status, "cancelled", "撤回后状态")

	// 撤回之后不能再被批准 —— 否则那条命令会在发起人已经放弃之后获得授权。
	approver := app.login("zhangwei@vela.io", "vela123")
	if app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", approver, nil).Code == 0 {
		t.Error("已撤回的工单又被批准了")
	}
}

// 审批人不能撤回别人的单 —— 他手里的动作是驳回。
func TestApprovalCancel_ApproverMustRejectNotCancel(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(initiator, "prod")

	apNo := app.interceptOn(initiator, prod, "DROP TABLE t_cancel_by_approver")
	id := app.approvalIDByNo(apNo)

	approver := app.login("zhangwei@vela.io", "vela123") // 链上的审批人
	r := app.cancel(approver, id)
	if r.Code == 0 {
		t.Fatal("审批人撤回了别人的工单 —— 他可以用撤回把本该驳回的单悄悄抹掉,记录上看不出有人拒绝过")
	}
	// 拒绝理由要把人指到正确的动作上,而不是一句笼统的"无权"。
	if !strings.Contains(r.Msg, "驳回") {
		t.Errorf("拒绝理由该告诉他去驳回,实际:%q", r.Msg)
	}
	eq(t, app.approvalRow(initiator, apNo).Status, "pending", "单子仍在待审批")
}

// 已批准但**还没执行**的单子也能撤:批是批了,但发起人可以不跑。
//
// 批准解锁的是发起人的一次执行,放不放弃这次执行是他的事。
func TestApprovalCancel_ApprovedButNotYetRun(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(initiator, "prod")

	apNo := app.interceptOn(initiator, prod, "DROP TABLE t_cancel_after_approve")
	id := app.approvalIDByNo(apNo)

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", approver, nil).Code, 0, "先批准")

	eq(t, app.cancel(initiator, id).Code, 0, "待执行的单子可以撤")
	eq(t, app.approvalRow(initiator, apNo).Status, "cancelled", "撤回后状态")

	// 撤回之后不能再执行 —— 否则"撤回"只是改了个标签,命令照样跑得掉。
	if app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", initiator, nil).Code == 0 {
		t.Error("已撤回的工单仍然执行成功了")
	}
}

// 跑过的单子撤不了。
//
// 那条命令已经落到库上了,把工单改成"已撤回"只会让记录与事实对不上 —— 要收回它得
// 再发一条变更,而不是改一行状态。这一条同时守住了撤回与执行的赛跑:执行先占住
// executed_at 之后,撤回必须拿不到。
func TestApprovalCancel_NotAfterItRan(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(initiator, "prod")

	apNo := app.interceptOn(initiator, prod, "DROP TABLE t_cancel_after_run")
	id := app.approvalIDByNo(apNo)

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", approver, nil).Code, 0, "先批准")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", initiator, nil).Code, 0, "再执行")

	if app.cancel(initiator, id).Code == 0 {
		t.Fatal("已经执行过的工单被撤回了 —— 库里会留下一张\"已撤回\"的单和一次真实发生过的下发")
	}
	if got := app.approvalRow(initiator, apNo).Status; got == "cancelled" {
		t.Errorf("状态被改成了 %q", got)
	}
}

// 撤回一张窗口申请单,那扇门要跟着关上 —— 不能留在 pending 里等一个永远不来的结论。
func TestApprovalCancel_WindowTicketClosesTheDoor(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	winID, apNo := app.applyWindow(token, prod, "orders_db", "撤回的班车")
	eq(t, app.cancel(token, app.approvalIDByNo(apNo)).Code, 0, "撤回窗口申请")

	if got := app.windowStatus(token, winID); got == "pending" {
		t.Error("撤回之后窗口仍停在待审批 —— 它会一直挂在那里等一个不会到来的结论")
	}
	// 撤回之后窗口当然也不该放行任何东西。
	if !app.riskCheckIn(token, prod, "DROP TABLE orders_2024_q3", "orders_db").RequiresApproval {
		t.Error("撤回申请之后窗口居然在放行")
	}
}
