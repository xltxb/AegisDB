package lark

import "velagateway/internal/imchannel"

// cardConfig:update_multi 让卡片能被「更新应用发送的消息卡片」接口改写,收卡靠它。
func cardConfig() map[string]any {
	return map[string]any{"wide_screen_mode": true, "update_multi": true}
}

// decisionButton 的 value 只说哪张单、什么动作,不带任何权限信息(spec §2.2)。
func decisionButton(label, style, apNo, act string) map[string]any {
	return map[string]any{
		"tag": "button", "type": style,
		"text":  map[string]any{"tag": "plain_text", "content": label},
		"value": map[string]any{"ap": apNo, "act": act},
	}
}

// interactiveCard 是私信给审批人的卡片:群通知那张的内容,把「前往审批」换成
// 「通过 / 驳回 / 前往控制台」一组按钮。
func interactiveCard(c imchannel.ApprovalCard) map[string]any {
	card := approvalCard(c)
	actions := []any{
		decisionButton("通过", "primary", c.ApNo, "a"),
		decisionButton("驳回", "danger", c.ApNo, "r"),
	}
	if c.ConsoleURL != "" {
		actions = append(actions, map[string]any{
			"tag": "button", "type": "default",
			"text": map[string]any{"tag": "plain_text", "content": "前往控制台"},
			"url":  c.ConsoleURL,
		})
	}
	var els []any
	for _, e := range card["elements"].([]any) {
		if m, ok := e.(map[string]any); ok && m["tag"] == "action" {
			continue
		}
		els = append(els, e)
	}
	note := els[len(els)-1] // approvalCard 的最后一个元素总是 note
	els = append(els[:len(els)-1], map[string]any{"tag": "action", "actions": actions}, note)
	card["elements"] = els
	card["header"].(map[string]any)["title"] = map[string]any{"tag": "plain_text", "content": "高危命令待你审批 · " + c.ApNo}
	card["config"] = cardConfig()
	return card
}

// closedCard 是收起后的卡片。网关不存卡片原文,改写只能整张替换,所以只留单号和终态,
// 详情在控制台。不带按钮:收起之后无处可点。
func closedCard(apNo string, final imchannel.CardState) map[string]any {
	tmpl := "grey"
	switch final.Status {
	case "approved":
		tmpl = "green"
	case "rejected":
		tmpl = "red"
	}
	title := "审批卡片已收起"
	if apNo != "" {
		title = "工单 " + apNo + " · 已处理"
	}
	return map[string]any{
		"config": cardConfig(),
		"header": map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": title}},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]any{"tag": "plain_text", "content": final.Text}},
			map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text", "content": "AegisDB 数据库网关"}}},
		},
	}
}
