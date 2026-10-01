package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// commandLimit:Telegram 消息正文上限是「解析实体后」4096 个字符。命令在通用层已截到 800,
// 这里再兜一次底,给标题、原因和链接留出余量。
const commandLimit = 3000

var kindTitle = map[string]string{
	"sql": "高危命令待审批", "window": "执行窗口申请待审批",
	"export": "敏感字段导出待审批", "release": "发布单审批",
}

// renderCard 把工单渲染成 HTML 消息。**每一段外部文本都先转义**:SQL 里出现 < 和 & 是
// 常态(WHERE a < b),不转义会让 Telegram 拒收整条消息 —— 那是一条「卡片发不出去」的
// 静默失败。命令放进 <pre>,等宽、可复制。
func renderCard(c imchannel.ApprovalCard) string {
	esc := html.EscapeString
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	title := kindTitle[c.Kind]
	if title == "" {
		title = kindTitle["sql"]
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s · %s · %s</b>\n", risk, esc(title), esc(c.ApNo))
	fmt.Fprintf(&b, "环境 %s · 实例 %s / %s\n", esc(env), esc(c.Instance), esc(db))
	fmt.Fprintf(&b, "发起人 %s\n", esc(c.Initiator))
	if strings.TrimSpace(c.Reason) != "" {
		fmt.Fprintf(&b, "原因 %s\n", esc(c.Reason))
	}
	fmt.Fprintf(&b, "<pre>%s</pre>", esc(clipRunes(c.Command, commandLimit)))
	if c.ConsoleURL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">前往控制台</a>", esc(c.ConsoleURL))
	}
	return b.String()
}

// keyboard 是卡片上的两个按钮。callback_data 只说「哪张单、什么动作」,不带任何权限信息
// —— 权限完全由收到点击后的认人与 DecideBlockFor 决定。
func keyboard(apNo string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": "✅ 通过", "callback_data": "ap:" + apNo + ":a"},
		{"text": "❌ 驳回", "callback_data": "ap:" + apNo + ":r"},
	}}}
}

// stateLabel 是收卡后那颗按钮上的字。
func stateLabel(s imchannel.CardState) string {
	icon := "✔"
	switch s.Status {
	case model.StatusApproved:
		icon = "✅"
	case model.StatusRejected:
		icon = "❌"
	case model.StatusExpired:
		icon = "⏱"
	case model.StatusCancelled:
		icon = "↩"
	}
	return clipRunes(icon+" "+s.Text, 60)
}

// SendCard 私信一张卡片。私聊里 chat_id 就是对方的用户 id —— 绑定时对方已经私聊过机器人,
// 所以机器人能主动给他发消息。
func (c *Channel) SendCard(ctx context.Context, pid string, card imchannel.ApprovalCard) (string, error) {
	var m Message
	err := c.call(ctx, c.client, "sendMessage", map[string]any{
		"chat_id": pid, "text": renderCard(card), "parse_mode": "HTML",
		"disable_web_page_preview": true, "reply_markup": keyboard(card.ApNo),
	}, &m)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(m.Chat.ID, 10) + ":" + strconv.FormatInt(m.MessageID, 10), nil
}

// CloseCard 把两个按钮换成一颗显示终态的按钮。
//
// 不用 editMessageText:那要重写整条正文,而网关没存卡片原文 —— 保留原文、只换按钮,
// 收起之后审批人仍看得到这张单原来是什么。
func (c *Channel) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	chat, msg, ok := strings.Cut(ref, ":")
	if !ok || chat == "" || msg == "" {
		return fmt.Errorf("telegram: 无法识别的消息引用 %q", ref)
	}
	msgID, err := strconv.ParseInt(msg, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: 无法识别的消息引用 %q", ref)
	}
	err = c.call(ctx, c.client, "editMessageReplyMarkup", map[string]any{
		"chat_id": chat, "message_id": msgID,
		"reply_markup": map[string]any{"inline_keyboard": [][]map[string]string{{
			{"text": stateLabel(final), "callback_data": DoneData},
		}}},
	}, nil)
	// 已经是这个样子,或者对方把消息删了:这张卡片都不会再有能点的按钮了,算收起。
	var ae *apiError
	if errors.As(err, &ae) && (strings.Contains(ae.Desc, "message is not modified") ||
		strings.Contains(ae.Desc, "message to edit not found")) {
		return nil
	}
	return err
}
