package slack

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// Block Kit 的长度上限(字符)。超了 Slack 拒收整条消息 —— 那是一次「卡片发不出去」的静默失败,
// 所以每一段外部文本都先截断。
const (
	headerLimit  = 150
	sectionLimit = 3000
	// commandLimit:命令在通用层已截到 800,这里再兜一次底。
	commandLimit = 2900
	urlLimit     = 3000
)

// action_id 认动作,block_id 认出卡片上那组按钮(收卡时把它换成终态)。按钮的 value 只说「哪张单、
// 什么动作」,不带任何权限信息 —— 权限完全由收到点击后的认人与 DecideBlockFor 决定。
const (
	actionApprove = "aegis_approve"
	actionReject  = "aegis_reject"
	actionOpen    = "aegis_open" // 「前往控制台」:链接按钮被点时 Slack 照样回调一次,网关不做任何事
	blockActions  = "aegis_actions"
	footer        = "AegisDB 数据库网关"
)

// 点击与事件里的 ID 都会流进后面的查询、请求与提示语,一律按白名单收。
var (
	apNoRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)
	valueRe = regexp.MustCompile(`^ap:([A-Za-z0-9-]{1,32}):([ar])$`)
	// userIDRe 是 Slack 成员 ID(U…;Enterprise Grid 里是 W…),也就是绑定表里的平台 ID。
	userIDRe = regexp.MustCompile(`^[UW][A-Z0-9]{2,31}$`)
	// dmRe 是私聊频道;convRe 是任意会话(C 公开频道、G 私有频道、D 私聊)。
	dmRe   = regexp.MustCompile(`^D[A-Z0-9]{2,31}$`)
	convRe = regexp.MustCompile(`^[CDG][A-Z0-9]{2,31}$`)
	tsRe   = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,8}$`)
	refRe  = regexp.MustCompile(`^(D[A-Z0-9]{2,31})\|([0-9]{1,12}\.[0-9]{1,8})\|([A-Za-z0-9-]{1,32})$`)
)

var kindTitle = map[string]string{
	"sql": "高危命令待审批", "window": "执行窗口申请待审批",
	"export": "敏感字段导出待审批", "release": "发布单审批",
}

func title(kind string) string {
	if t := kindTitle[kind]; t != "" {
		return t
	}
	return kindTitle["sql"]
}

// plain 是一段不解析的文本。卡片上**每一段外部文本都用它**(命令用 rich_text_preformatted):
// 发起人写的 [链接](…)、<!channel>、SQL 里的 < & ``` 都原样显示,没有可注入的链接、提及或代码块
// 逃逸,也就不必转义。emoji 关掉:原因里的 :xxx: 照原样显示。
func plain(s string) map[string]any {
	return map[string]any{"type": "plain_text", "text": s, "emoji": false}
}

func section(s string) map[string]any {
	return map[string]any{"type": "section", "text": plain(clipRunes(s, sectionLimit))}
}

func button(label, actionID, style, value string) map[string]any {
	return map[string]any{"type": "button", "action_id": actionID, "style": style, "text": plain(label), "value": value}
}

func footerBlock() map[string]any {
	return map[string]any{"type": "context", "elements": []any{plain(footer)}}
}

// cardBlocks 是私信给审批人的卡片。
func cardBlocks(c imchannel.ApprovalCard) []any {
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	blocks := []any{
		map[string]any{"type": "header", "text": plain(clipRunes(risk+" · "+title(c.Kind)+" · "+c.ApNo, headerLimit))},
		section("环境 " + env + " · 实例 " + c.Instance + " / " + db + "\n发起人 " + c.Initiator),
	}
	if strings.TrimSpace(c.Reason) != "" {
		blocks = append(blocks, section("原因 "+c.Reason))
	}
	if strings.TrimSpace(c.Command) != "" {
		blocks = append(blocks, map[string]any{"type": "rich_text", "elements": []any{
			map[string]any{"type": "rich_text_preformatted", "elements": []any{
				map[string]any{"type": "text", "text": clipRunes(c.Command, commandLimit)},
			}},
		}})
	}
	buttons := []any{
		button("✅ 通过", actionApprove, "primary", "ap:"+c.ApNo+":a"),
		button("❌ 驳回", actionReject, "danger", "ap:"+c.ApNo+":r"),
	}
	// 超长或不是 http(s) 绝对地址的链接不放:一颗按钮的 URL 不合法,Slack 会拒收整张卡片。
	if linkable(c.ConsoleURL) {
		buttons = append(buttons, map[string]any{"type": "button", "action_id": actionOpen, "text": plain("前往控制台"), "url": c.ConsoleURL})
	}
	return append(blocks, map[string]any{"type": "actions", "block_id": blockActions, "elements": buttons}, footerBlock())
}

// linkable 判断控制台地址能否做成链接按钮。notify.consoleURL 保存时不校验,填成 aegis.corp:5173
// 这种没有协议的值在飞书、Telegram 里无害,在 Slack 里会让每张卡片都以 invalid_blocks 发不出去。
func linkable(s string) bool {
	if s == "" || len(s) > urlLimit {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

var mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// mrkdwnEscape 转义 Slack mrkdwn 的三个控制字符。用于顶层 text(通知预览)与机器人发的纯文本:
// 不转义的话,一句「已绑定到 张三<zhang@…>」里的尖括号会被 Slack 当成链接或提及。
func mrkdwnEscape(s string) string { return mrkdwnEscaper.Replace(s) }

// cardText 是卡片的顶层 text:通知横幅与无障碍朗读用它,blocks 里才是正文。
func cardText(c imchannel.ApprovalCard) string {
	return mrkdwnEscape("AegisDB · " + title(c.Kind) + " · " + c.ApNo)
}

func closedText(apNo string, final imchannel.CardState) string {
	return mrkdwnEscape("AegisDB · 工单 " + apNo + " · " + final.Text)
}

// stateLine 是收卡后取代按钮的那一行。
func stateLine(final imchannel.CardState) string {
	icon := "✔"
	switch final.Status {
	case model.StatusApproved:
		icon = "✅"
	case model.StatusRejected:
		icon = "❌"
	case model.StatusExpired:
		icon = "⏱"
	case model.StatusCancelled:
		icon = "↩"
	}
	return icon + " " + final.Text
}

// closedBlocks 是收起后的卡片。Slack 改消息只能整条替换 blocks,而网关不存卡片原文 —— 所以
// original 是收卡前从 Slack 取回的原卡片:把那组按钮换成一行终态,其余原样保留,审批人仍看得到
// 这张单原来是什么。取不到原卡片时只留单号与终态。
func closedBlocks(original []json.RawMessage, apNo string, final imchannel.CardState) []any {
	state := section(stateLine(final))
	if len(original) == 0 {
		return []any{
			map[string]any{"type": "header", "text": plain("工单 " + apNo + " · 已处理")},
			state,
			footerBlock(),
		}
	}
	out := make([]any, 0, len(original))
	replaced := false
	for _, b := range original {
		var head struct {
			BlockID string `json:"block_id"`
		}
		_ = json.Unmarshal(b, &head)
		if head.BlockID == blockActions {
			out = append(out, state)
			replaced = true
			continue
		}
		out = append(out, b)
	}
	if !replaced {
		out = append(out, state)
	}
	return out
}

// cardRef 是 tbl_im_card.msg_ref:私聊频道、消息 ts、单号。收卡要前两者定位消息,单号给取不到
// 原卡片时的那张替代卡片用。
func cardRef(channel, ts, apNo string) string { return channel + "|" + ts + "|" + apNo }

func parseRef(ref string) (channel, ts, apNo string, ok bool) {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// parseValue 读按钮的 value:ap:<ApNo>:a | ap:<ApNo>:r。webhook 模式下点击可以被伪造(验签挡住了
// 伪造者,挡不住一个拿到签名密钥的人),只认网关自己造的形状。
func parseValue(v string) (apNo string, approve, ok bool) {
	m := valueRe.FindStringSubmatch(v)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] == "a", true
}

// clipRunes 把 s 截到至多 n 个字符(含省略号):Slack 的上限是硬的,多一个字符整条消息就被拒。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
