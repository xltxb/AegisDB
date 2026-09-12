package bootstrap

// 超时自动作废时,这张单挂着的东西也要跟着收场。
//
// 撤回那一路(CancelApproval)早就对了:窗口记 cancelled、导出任务判失败。超时清扫这一路
// 只把工单本身标成 expired 就走了 —— 于是:
//
//   · 执行窗口永远停在 pending:界面上写着「待审批」,而那张单已经作废,永远不会有人批
//   · 导出任务永远停在 awaiting:它在等一张不存在的批准,而人在等那个文件
//
// 方向上是朝严的(门没开、包没生成),所以没人会因为出事而发现它 —— 只会有人反复去点
// 那个永远不动的「待审批」,然后再提一张新的。

import (
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// expireEverythingNow 把超时策略设成「立刻作废」。
func (a *testApp) expireEverythingNow(token string) {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"approval.onTimeout": "auto-reject", "approval.timeoutMinutes": 0,
	}).Code, 0, "配置超时自动作废")
}

func TestTimeoutSweep_ExpiredWindowTicketClosesItsWindow(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	winID, apNo := app.applyWindow(token, prod, "orders_db", "会过期的班车")
	eq(t, app.windowStatus(token, winID), model.WindowPending, "前置条件:窗口在等审批")

	app.expireEverythingNow(token)
	app.svc.SweepApprovalTimeouts()

	if got := app.approvalStatusByNo(token, apNo); got == model.StatusPending {
		t.Fatalf("前置条件不成立:工单没被清扫,状态仍是 %s", got)
	}
	if got := app.windowStatus(token, winID); got == model.WindowPending {
		t.Error("工单已作废,窗口还停在「待审批」—— 界面上那扇门永远等不到人批")
	}
}

func TestTimeoutSweep_ExpiredExportTicketFailsItsJob(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	conn := app.exportSetup(token)
	eq(t, app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "会过期的导出", "includeSensitive": true,
	}).Code, 0, "提交含敏感字段的导出")
	job := app.lastExportJob(conn)
	eq(t, job.Status, model.ExportAwaiting, "前置条件:任务在等审批")
	var ap model.Approval
	if err := app.repo.DB().First(&ap, job.ApprovalID).Error; err != nil {
		t.Fatalf("读审批单: %v", err)
	}
	apNo := ap.ApNo
	app.expireEverythingNow(token)
	app.svc.SweepApprovalTimeouts()

	if got := app.approvalStatusByNo(token, apNo); got == model.StatusPending {
		t.Fatalf("前置条件不成立:工单没被清扫,状态仍是 %s", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := app.lastExportJob(conn).Status; st != model.ExportAwaiting {
			return // 收场了
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("工单已作废,导出任务还停在 awaiting —— 它在等一张不存在的批准,而人在等那个文件")
}
