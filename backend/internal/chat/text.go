package chat

import "fmt"

// texts 是卡片上的固定文案。卡片语言是全局设置 chat.cardLang:用户的语言偏好只存在浏览器里,
// 后端发卡片时不知道。两种语言的键与占位符必须一一对应(TestTr_TablesMatch)。
var texts = map[string]map[string]string{
	"zh": {
		"title.command": "高危命令待审批",
		"title.release": "发布待审批",
		"title.window":  "执行窗口待审批",
		"title.export":  "敏感导出待审批",
		"title.test":    "测试卡片",

		"field.no":        "单号",
		"field.instance":  "实例",
		"field.env":       "环境",
		"field.risk":      "风险",
		"field.initiator": "发起人",
		"field.reason":    "原因",
		"field.statement": "语句",

		"risk.high": "高危",
		"risk.mid":  "中危",
		"risk.low":  "低危",

		"btn.approve": "通过",
		"btn.reject":  "拒绝",
		"btn.confirm": "确认通过",
		"btn.back":    "返回",
		"btn.detail":  "查看详情",

		"confirm.approve":    "确定通过这张审批单吗?",
		"reject.placeholder": "拒绝原因(可不填)",
		"partial":            "卡片未显示全文,通过前请到网关核对。",

		"outcome.approved":  "已由 %s 通过 · %s",
		"outcome.rejected":  "已由 %s 拒绝 · %s",
		"outcome.cancelled": "已撤回",
		"outcome.voided":    "已作废",
	},
	"en": {
		"title.command": "High-risk command awaiting approval",
		"title.release": "Release awaiting approval",
		"title.window":  "Execution window awaiting approval",
		"title.export":  "Sensitive export awaiting approval",
		"title.test":    "Test card",

		"field.no":        "Ticket",
		"field.instance":  "Instance",
		"field.env":       "Environment",
		"field.risk":      "Risk",
		"field.initiator": "Requested by",
		"field.reason":    "Reason",
		"field.statement": "Statement",

		"risk.high": "High",
		"risk.mid":  "Medium",
		"risk.low":  "Low",

		"btn.approve": "Approve",
		"btn.reject":  "Reject",
		"btn.confirm": "Confirm approval",
		"btn.back":    "Back",
		"btn.detail":  "View details",

		"confirm.approve":    "Approve this request?",
		"reject.placeholder": "Reason for rejecting (optional)",
		"partial":            "This card does not show the full content. Check it in the gateway before approving.",

		"outcome.approved":  "Approved by %s · %s",
		"outcome.rejected":  "Rejected by %s · %s",
		"outcome.cancelled": "Withdrawn",
		"outcome.voided":    "Voided",
	},
}

// Tr 取一句卡片文案。不认识的语言按 zh;不认识的键原样返回,让它在卡片上显眼,而不是空着。
func Tr(lang, key string, args ...any) string {
	table, ok := texts[lang]
	if !ok {
		table = texts["zh"]
	}
	s, ok := table[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}
