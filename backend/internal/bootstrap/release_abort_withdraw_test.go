package bootstrap

// 发布单终局与其挂件的一致性(审查修复 #3 / #4)。
//
// A release that reaches a terminal state must not leave live-looking debris
// behind: an aborted run's approval ticket kept sitting in the approvers'
// queue (someone could "approve" a change that no longer exists), and a
// rejected run's remaining stages kept showing 待执行 forever. Both are the
// same defect — the terminal transition tidied the release row and stopped.

import (
	"encoding/json"
	"net/http"
	"testing"
)

// approvalRow is the slice of the approvals listing these tests read.
type approvalRow struct {
	ID     int64  `json:"id"`
	ApNo   string `json:"apNo"`
	Status string `json:"status"`
}

func (a *testApp) approvalRowByNo(token, apNo string) *approvalRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&pageSize=500", token, nil)
	var page struct {
		Items []approvalRow `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	for i := range page.Items {
		if page.Items[i].ApNo == apNo {
			return &page.Items[i]
		}
	}
	return nil
}

// submitParkedRelease drives a release into `waiting` on an approval ticket and
// returns the release view (with the approve stage carrying the ticket).
func submitParkedRelease(t *testing.T, app *testApp, admin string, title string) releaseView {
	t.Helper()
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, title+"流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
		{"name": "结果通知", "type": "notify"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": title, "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #3/#4",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	eq(t, waiting.Status, "waiting", "release parks on the approval")
	return waiting
}

// TestAbortWithdrawsPendingApproval — #3: 终止发布要把挂着的审批单一并作废,
// 否则审批人还能"批准"一个已经不存在的变更。
func TestAbortWithdrawsPendingApproval(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	approver := app.login("zhangwei@vela.io", "vela123")

	waiting := submitParkedRelease(t, app, admin, "将被终止的发布")
	ap := stageByType(waiting, "approve")
	if ap == nil || ap.ApprovalNo == "" {
		t.Fatalf("approve stage should carry a ticket, got %+v", ap)
	}

	r := app.do(http.MethodPost, "/api/v1/releases/"+itoa(waiting.ID)+"/abort", admin, nil)
	eq(t, r.Code, 0, "abort release")

	// The ticket left the pending queue — 作废,同超时失效一个词汇。
	row := app.approvalRowByNo(approver, ap.ApprovalNo)
	if row == nil {
		t.Fatalf("approval %s vanished entirely; it should remain, voided", ap.ApprovalNo)
	}
	eq(t, row.Status, "expired", "the linked ticket is voided, not left pending")

	// And a decision on it must now be refused — the queue row is history, not work.
	dec := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(row.ID)+"/approve", approver, nil)
	if dec.Code == 0 {
		t.Fatal("approving a voided ticket must be refused")
	}
}

// TestRejectionSkipsRemainingStages — #4: 驳回终局要和终止终局长得一样 —— 没跑到
// 的阶段标 skipped,而不是永远displaying 待执行。
func TestRejectionSkipsRemainingStages(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	approver := app.login("zhangwei@vela.io", "vela123")

	waiting := submitParkedRelease(t, app, admin, "将被驳回的发布")
	ap := stageByType(waiting, "approve")

	dec := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/reject", approver,
		map[string]any{"reason": "变更窗口未到"})
	eq(t, dec.Code, 0, "reject")

	app.svc.ResumeReleaseApprovals()
	done := app.waitRelease(admin, waiting.ID, "failed", "success")
	eq(t, done.Status, "failed", "rejection fails the run")
	eq(t, stageByType(done, "approve").Status, "failed", "approve stage failed")
	eq(t, stageByType(done, "execute").Status, "skipped", "execute never ran and says so")
	eq(t, stageByType(done, "notify").Status, "skipped", "notify never ran and says so")
}
