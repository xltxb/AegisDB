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

		"kind.command": "高危命令",
		"kind.release": "发布",
		"kind.window":  "执行窗口",
		"kind.export":  "敏感导出",

		"field.no":        "单号",
		"field.type":      "类型",
		"field.instance":  "实例",
		"field.database":  "数据库",
		"field.env":       "环境",
		"field.risk":      "风险",
		"field.initiator": "发起人",
		"field.reason":    "原因",
		"field.statement": "语句",
		"field.notice":    "注意",
		"field.window":    "窗口",
		"field.schedule":  "时间段",
		"field.export":    "导出内容",
		"field.tier":      "判定分层",
		"field.method":    "执行方式",

		"method.osc": "在线变更",

		"export.raw":    "原值",
		"export.masked": "按脱敏导出",

		// 单子上存着的系统句子,键是 ref. + 它的 code。中文与单子上存的那句一字不差(有用例钉着)。
		"ref.winOnce":         "一次性 %s → %s",
		"ref.winOnceUnset":    "一次性(时间未设置)",
		"ref.winDaily":        "班车 每天 %s-%s (%s)",
		"ref.winWeekly":       "班车 周%s %s-%s (%s)",
		"ref.winUnknownModel": "(未知时间模型)",
		"ref.oscBigDirect":    "表 %s 约 %s 行 / %s,这条 DDL 将直接执行(在线变更不支持:%s),可能长时间锁表",

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
		// 查不到是谁决定的(步骤没写成)时退而求其次
		"outcome.approvedBare": "已通过",
		"outcome.rejectedBare": "已拒绝",

		"bind.usage":   "请先在网关右上角用户菜单的「审批机器人」里生成绑定码,再把它发给我。",
		"bind.done":    "已绑定网关账号 %s。之后需要你审批的单子会发到这里。",
		"bind.invalid": "绑定码无效或已过期,请在网关里重新生成。",
		"bind.taken":   "这个聊天账号已绑定另一个网关账号,请先在那个账号里解绑。",
		"bind.locked":  "绑定码输错次数过多,请 15 分钟后再试。",
		"bind.off":     "这个审批机器人没有开启,暂时不能绑定。",
		"bind.failed":  "绑定失败,请稍后再试。",

		// 点击之后的提示。Telegram 的上限是 200 个字符。
		"click.approved": "已通过",
		"click.rejected": "已拒绝",
		"click.handled":  "这张审批单已经处理过了。",
		"click.invalid":  "卡片已失效,请到网关查看。",
		"click.unbound":  "这个聊天账号没有绑定网关账号,请先在网关里绑定。",
		"click.inactive": "你的网关账号不可用,不能审批。",
		"click.notHuman": "服务账号不能审批。",
		"click.noMenu":   "你没有「审批」菜单的权限,请联系管理员。",
		"click.self":     "不能审批自己发起的单子,请审批链上的其他人处理。",
		"click.notChain": "你不在这张审批单的审批链上。",
		"click.off":      "这个审批机器人没有开启,请到网关处理。",
		"click.retry":    "处理失败,请稍后再试。",
		"click.test":     "测试成功。这是测试卡片,没有审批任何单子。",
	},
	"en": {
		"title.command": "High-risk command awaiting approval",
		"title.release": "Release awaiting approval",
		"title.window":  "Execution window awaiting approval",
		"title.export":  "Sensitive export awaiting approval",
		"title.test":    "Test card",

		"kind.command": "High-risk command",
		"kind.release": "Release",
		"kind.window":  "Execution window",
		"kind.export":  "Sensitive export",

		"field.no":        "Ticket",
		"field.type":      "Type",
		"field.instance":  "Instance",
		"field.database":  "Database",
		"field.env":       "Environment",
		"field.risk":      "Risk",
		"field.initiator": "Requested by",
		"field.reason":    "Reason",
		"field.statement": "Statement",
		"field.notice":    "Notice",
		"field.window":    "Window",
		"field.schedule":  "Schedule",
		"field.export":    "Exports",
		"field.tier":      "Judged tier",
		"field.method":    "Runs as",

		"method.osc": "Online change",

		"export.raw":    "Original values",
		"export.masked": "Masked",

		"ref.winOnce":         "One-off %s → %s",
		"ref.winOnceUnset":    "One-off (time not set)",
		"ref.winDaily":        "Recurring daily %s-%s (%s)",
		"ref.winWeekly":       "Recurring on weekdays %s (1 = Mon) %s-%s (%s)",
		"ref.winUnknownModel": "(unknown schedule type)",
		"ref.oscBigDirect":    "Table %s has about %s rows / %s; this DDL runs directly (online change does not support it: %s) and may lock the table for a long time",

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

		"outcome.approvedBare": "Approved",
		"outcome.rejectedBare": "Rejected",

		"bind.usage":   "Generate a binding code under “Approval bots” in the gateway's user menu (top right), then send it to me.",
		"bind.done":    "Linked to gateway account %s. Requests that need your approval will be sent here.",
		"bind.invalid": "This binding code is invalid or has expired. Generate a new one in the gateway.",
		"bind.taken":   "This chat account is already linked to another gateway account. Unlink it there first.",
		"bind.locked":  "Too many wrong binding codes. Try again in 15 minutes.",
		"bind.off":     "This approval bot is turned off. Linking is not available right now.",
		"bind.failed":  "Linking failed. Please try again later.",

		"click.approved": "Approved",
		"click.rejected": "Rejected",
		"click.handled":  "This request has already been handled.",
		"click.invalid":  "This card is no longer valid. Check the gateway.",
		"click.unbound":  "This chat account is not linked to a gateway account. Link it in the gateway first.",
		"click.inactive": "Your gateway account is not active, so you cannot approve.",
		"click.notHuman": "Service accounts cannot approve.",
		"click.noMenu":   "You do not have access to the Approvals menu. Contact an administrator.",
		"click.self":     "You cannot decide your own request. Ask someone else on the approval chain.",
		"click.notChain": "You are not on the approval chain of this request.",
		"click.off":      "This approval bot is turned off. Use the gateway instead.",
		"click.retry":    "Something went wrong. Please try again later.",
		"click.test":     "Test succeeded. This is a test card; no request was approved.",
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
