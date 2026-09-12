package bootstrap

// 站内审批与外部回调竞争时,只有一方生效,另一方拿到当前状态。
//
// 两条通道都能决定同一张单:控制台里点通过,和飞书卡片上点通过。它们靠
// `ClaimApproval` 的原子认领分出胜负 —— 一次 `UPDATE … WHERE status = 'pending'`,
// 谁的 RowsAffected 是 1 谁赢。
//
// 逻辑上成立,但此前没有任何用例钉住它,而这恰恰是最该钉住的地方:认领一旦退化成
// "后到的覆盖先到的",表现是**一张单被决策两次**,审计里两个审批人都在,而那张单
// 到底算批了还是驳了,取决于哪一条消息跑得快。
//
// 已有的用例只覆盖了「回调 × 2」那半边(CallbackAuthAndIdempotency)。这里补的是另一
// 半:**先站内、后回调**。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestExternalApproval_InAppDecisionWinsOverLateCallback(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)
	app.markExternallyDispatched(ap.ApNo, "vt-"+ap.ApNo)

	// ① 控制台里由**另一个人**通过(不开自审开关 —— 那会让这个用例证明不了 SoD)。
	approver := app.login("zhangwei@vela.io", "vela123")
	id := app.approvalIDByNo(ap.ApNo)
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", approver, nil).Code, 0, "站内通过")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "前置条件:站内已通过")

	// ② 飞书那边的卡片还开着,有人点了驳回,回调随后才到。
	r, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo, "approved": false,
		"approver": []string{"herbert@tbu.net"},
	})

	// 回调不该失败 —— 它只是来晚了,不是出错了。它该拿到这张单**此刻**的状态。
	eq(t, r.Code, 0, "迟到的回调应当被接受并回报当前状态,而不是报错")
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(r.Data, &out)
	eq(t, out.Status, "approved", "回调拿到的应当是当前状态")

	// 关键断言:站内那次决策没有被覆盖。
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved",
		"迟到的回调把站内已经作出的决策改掉了 —— 认领不再是原子的")

	// 而且只决策了一次:审计里不该有第二个审批人。
	var rows []struct {
		ApprovalNo string `json:"approvalNo"`
		Operator   string `json:"operator"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(token, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.ApprovalNo == ap.ApNo && row.Operator != "" {
			n++
			if row.Operator == "herbert@tbu.net" {
				t.Errorf("审计里记下了迟到那一方的审批人 —— 这张单看起来被批了两次")
			}
		}
	}
	if n != 1 {
		t.Errorf("这张单的决策审计有 %d 条,应当只有 1 条", n)
	}
}
