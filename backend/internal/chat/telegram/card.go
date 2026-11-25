package telegram

// 把 chat.Card 画成 Telegram 的 HTML 消息。
//
// 用 HTML 不用 MarkdownV2:HTML 只有 < > & " 四个字符要转义,漏不了;Markdown 要转义的有十几个,随上下文
// 而变。来自用户的值(字段、原因、语句、结论里的名字)放进 code / pre:那里面平台不会再去认链接、@ 某人和
// /命令。标题只有固定文案与单号。

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"aegisdb/internal/chat"
)

const (
	maxUnits = 4096    // 解完实体之后的 UTF-16 单位数
	maxRaw   = 1 << 15 // 解之前的字节数;4096 个单位全要转义也只有 2.4 万,在这里只是兜底

	// 语句之外的内容不会这么长;超出说明不寻常,截掉并写明未显示全文。单位同上。
	maxTitle  = 100
	maxLabel  = 60
	maxField  = 300
	maxReason = 1000
	maxURL    = 1000

	maxToast = 200
)

// message 是一条要发的消息。keyboard 不为 nil:空的就是「没有按钮」,更新时要明说。
type message struct {
	text     string
	keyboard [][]button
}

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// render 画出一张卡片。放不下时截短语句并写明未显示全文。同样的卡片永远画成一样。
func render(c chat.Card) message {
	cut := false
	clip := func(s string, n int) string {
		out, short := clipUTF16(chat.OneLine(s), n)
		cut = cut || short
		return out
	}
	c.Title = clip(c.Title, maxTitle)
	c.Reason = clip(c.Reason, maxReason)
	c.Outcome = clip(c.Outcome, maxField)
	fields := make([]chat.Field, len(c.Fields))
	for i, f := range c.Fields {
		fields[i] = chat.Field{Label: clip(f.Label, maxLabel), Value: clip(f.Value, maxField)}
	}
	c.Fields, c.Statement = fields, chat.Visible(c.Statement)
	c.Partial = c.Partial || cut

	r := compose(c)
	if !r.fits() && c.Statement != "" {
		c.Partial = true
		full := c.Statement
		c.Statement = ""
		// 先按纯文本截,再转义:不会劈开字符、实体或标签。
		if room := maxUnits - compose(c).units - len("\n\n"); room > 0 {
			c.Statement, _ = clipUTF16(full, room)
		}
		r = compose(c)
	}
	// 还放不下就从后往前丢字段。剩下的标题、原因、结论各有上限,一定放得下。
	for !r.fits() && len(c.Fields) > 0 {
		c.Partial = true
		c.Fields = c.Fields[:len(c.Fields)-1]
		r = compose(c)
	}
	return message{text: r.html.String(), keyboard: keyboard(c)}
}

// rich 一边拼 HTML,一边数解完之后有多长。
type rich struct {
	html  strings.Builder
	units int
}

func (r *rich) fits() bool { return r.units <= maxUnits && r.html.Len() <= maxRaw }

// add 加一段文字,tag 非空则包在里面。
func (r *rich) add(tag, s string) {
	r.units += unitsOf(s)
	if tag != "" {
		r.html.WriteString("<" + tag + ">")
		defer r.html.WriteString("</" + tag + ">")
	}
	_, _ = escaper.WriteString(&r.html, s)
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func compose(c chat.Card) *rich {
	tr := func(key string) string { return chat.Tr(c.Lang, key) }
	r := &rich{}
	r.add("b", icon(c)+c.Title)
	sep := "\n\n"
	line := func(label, value string) {
		r.add("", sep+label+": ")
		r.add("code", value)
		sep = "\n"
	}
	for _, f := range c.Fields {
		line(f.Label, f.Value)
	}
	if c.Reason != "" {
		line(tr("field.reason"), c.Reason)
	}
	if c.Statement != "" {
		r.add("", "\n\n")
		r.add("pre", c.Statement)
	}
	if c.Outcome != "" {
		r.add("", "\n\n")
		r.add("code", c.Outcome)
		return r
	}
	if c.Partial {
		r.add("", "\n\n⚠️ "+tr("partial"))
	}
	if c.Confirming {
		r.add("", "\n\n")
		r.add("b", tr("confirm.approve"))
	}
	return r
}

// icon 按风险给标题一个记号;有了结论的不给,结论自己带着。
func icon(c chat.Card) string {
	switch {
	case c.Outcome != "":
		return ""
	case c.Risk == "high":
		return "🔴 "
	case c.Risk == "mid":
		return "🟠 "
	}
	return "🔵 "
}

// actions 是动作在 callback_data 里的短码:整个串最多 64 字节,令牌占了 32。
var actions = map[string]chat.Action{
	"a": chat.ActApprove, "c": chat.ActConfirm, "b": chat.ActBack, "r": chat.ActReject, "t": chat.ActTest,
}

var dataShape = regexp.MustCompile(`\A([acbrt]):([A-Za-z0-9_-]{1,62})\z`)

// keyboard 是卡片下面的按钮。「通过」只进入二次确认;「拒绝」没有原因可填,直接决定。
func keyboard(c chat.Card) [][]button {
	rows := [][]button{}
	if c.Outcome != "" {
		return rows
	}
	press := func(key, code string) button { return button{Text: chat.Tr(c.Lang, key), Data: code + ":" + c.Token} }
	// 放不进去的令牌不给按钮:平台会整条退回来。
	if dataShape.MatchString("a:" + c.Token) {
		switch {
		case c.Test:
			rows = append(rows, []button{press("title.test", "t")})
		case c.Confirming:
			rows = append(rows, []button{press("btn.confirm", "c"), press("btn.back", "b")})
		default:
			rows = append(rows, []button{press("btn.approve", "a"), press("btn.reject", "r")})
		}
	}
	if u, err := url.Parse(c.DetailURL); err == nil && len(c.DetailURL) <= maxURL && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		rows = append(rows, []button{{Text: chat.Tr(c.Lang, "btn.detail"), URL: c.DetailURL}})
	}
	return rows
}

func unitsOf(s string) (n int) {
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// clipUTF16 把 s 截到最多 max 个 UTF-16 单位(含省略号),报告截了没有。s 必须是合法的 UTF-8。
func clipUTF16(s string, max int) (string, bool) {
	if unitsOf(s) <= max {
		return s, false
	}
	n := 0
	for i, r := range s {
		if n += utf16.RuneLen(r); n > max-1 {
			return s[:i] + "…", true
		}
	}
	return s, false
}
