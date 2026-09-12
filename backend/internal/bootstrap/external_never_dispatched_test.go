package bootstrap

// 一张从未外发过的工单,不接受外部回调。
//
// 回调靠 ApNo 关联,而 ApNo 是一个**可预测的计数器** —— 提交人在自己的执行响应里就
// 看得到,往前往后数几个就是别人的单号。把它和 callbackSecret 放在一起,持有密钥的
// 人就能终审站内任意一张待审工单,包括从来没有外发过的那些。
//
// 交叉校验原本只在 `ap.ExternalTaskID != ""` 时才做,理由是 task id 是异步写入的
// (出站在 goroutine 里跑),条件收紧会误伤"回调比我们自己的写入更快"那一瞬。但那一瞬
// 的代价与这个洞不成比例:出站失败、厂商不回 task id、总开关打开之前建的单 ——
// 这三种情况下 ExternalTaskID **永远**为空,而不是"暂时"为空。
//
// 所以改成 fail closed:手上没有 task id,就不认外部回调。代价是那个竞态里的回调会
// 被拒(厂商重试即可,站内审批这条路始终可用),换来的是"没外发过的单外部动不了"。

import "testing"

func TestExternalApproval_CallbackRefusedForNeverDispatchedTicket(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	// 打开总开关并配好密钥,但**不配** baseURL —— 于是没有任何出站发生,
	// 这张单的 ExternalTaskID 永远是空的。这正是出站失败时的样子。
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)

	r, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "approved": true,
		"approver": []string{"zhangwei@vela.io"},
	})
	if r.Code == 0 {
		t.Error("一张从未外发过的单被外部回调终审了 —— 持密钥者可以按可预测的单号挑任意一张")
	}
	eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "单据必须还留在站内等签字")
}

// 反过来那一半:真正派发过的单,回调照常工作。
func TestExternalApproval_CallbackWorksForDispatchedTicket(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)
	app.markExternallyDispatched(ap.ApNo, "vendor-task-77")

	// external_task_id 装的是**我们的** ApNo(厂商原样回显),厂商自己的任务号在 task_id。
	r, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vendor-task-77", "approved": true,
		"approver": []string{"zhangwei@vela.io"},
	})
	eq(t, r.Code, 0, "派发过的单,回调应当被接受")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "回调决策应当落库")
}

// markExternallyDispatched 直接在库里写上厂商任务号,替代一次真实出站。
//
// 出站本身另有用例覆盖(TestExternalApproval_OutboundDispatchStoresTaskID 起了一个
// stub 厂商);这里要的只是"这张单外发过"这个前提。
func (a *testApp) markExternallyDispatched(apNo, taskID string) {
	a.t.Helper()
	if err := a.repo.DB().Table("tbl_approval").
		Where("ap_no = ?", apNo).Update("external_task_id", taskID).Error; err != nil {
		a.t.Fatalf("标记外发: %v", err)
	}
}

// 通过,但没说是谁批的 —— 不认。
//
// 禁自审这道网靠的是比对 approver 与发起人。approver 为空时那个比对什么也比不出来,
// 而旧代码把"比不出来"当成了"不是自审"直接放行:发起人在自己的卡片上点通过,厂商回调
// 不带 approver(或者带的是飞书昵称而不是邮箱),这道网就整个落空了 —— 审计里留下的
// 审批人是「审批魔方」四个字,事后谁也说不出到底是谁签的。
//
// 所以 approve 且 approver 为空 → 拒绝(fail closed)。驳回不受影响:驳回是安全方向,
// 它不需要 SoD 保护,而外部系统超时自动关单这类正当场景恰恰不带审批人。
func TestExternalApproval_ApproveWithoutApproverIsRefused(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"不带 approver 字段", map[string]any{"approved": true}},
		{"approver 是空数组", map[string]any{"approved": true, "approver": []string{}}},
		{"approver 全是空白", map[string]any{"approved": true, "approver": []string{"", "  "}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ap := app.submitProdHighRisk(token)
			app.markExternallyDispatched(ap.ApNo, "vt-"+ap.ApNo)
			body := map[string]any{"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo}
			for k, v := range c.body {
				body[k] = v
			}
			r, _ := app.postLarkCallback("s3cr3t", body)
			if r.Code == 0 {
				t.Error("一次说不出审批人的通过被接受了 —— 禁自审那道网整个落空")
			}
			eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "单据必须还留在站内等签字")
		})
	}
}

// 驳回不需要审批人 —— 外部系统超时自动关单正是这样。
func TestExternalApproval_RejectWithoutApproverStillWorks(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)
	app.markExternallyDispatched(ap.ApNo, "vt-"+ap.ApNo)

	r, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo, "approved": false,
	})
	eq(t, r.Code, 0, "没有审批人的驳回应当照常生效")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "rejected", "驳回落库")
}
