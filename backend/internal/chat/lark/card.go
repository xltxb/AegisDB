package lark

// 把 chat.Card 画成飞书卡片(卡片 JSON 1.0)。
//
// 用 1.0 不用 2.0:2.0 要客户端 7.20 以上,1.0 只要 6.6(表单容器);这里要的东西 1.0 都有。一张卡片
// 更新前后必须是同一版。
//
// 来自用户的文字一律放进 plain_text,不放进会解析标记的 lark_md / markdown:飞书的 markdown 没有可靠的
// 转义(代码块也能被语句里的反引号关掉),而 plain_text 不解析任何标记,语句因此一字不差。${} 变量只对
// 搭建工具里的模板卡片生效,这里发的是完整的卡片 JSON,不会替换。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"aegisdb/internal/chat"
)

const (
	// maxCardBytes:飞书限制整个请求体 30 KB,卡片在里面是一个字符串,得按再转义一次之后的长度算
	// (sizeInBody);留出一点给信封。
	maxCardBytes = 28000

	// 语句之外的内容不会这么长;超出说明不寻常,截掉并写明未显示全文。
	maxTitleRunes  = 100
	maxFieldRunes  = 300
	maxReasonRunes = 1000

	// 拒绝原因输入框:它的名字就是回调 form_value 里的键。
	reasonInput    = "reason"
	maxRejectRunes = 500 // tbl_approval.reject_reason 存得下 512
)

type obj = map[string]any

// RenderCard 画出卡片 JSON。放不下时截短语句并写明未显示全文。
func RenderCard(c chat.Card) string {
	cut := false
	clip := func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		cut = true
		return string(r[:n]) + "…"
	}
	c.Title = clip(oneLine(c.Title), maxTitleRunes)
	c.Reason = clip(oneLine(c.Reason), maxReasonRunes)
	c.Outcome = clip(oneLine(c.Outcome), maxFieldRunes)
	fields := make([]chat.Field, len(c.Fields))
	for i, f := range c.Fields {
		fields[i] = chat.Field{Label: oneLine(f.Label), Value: clip(oneLine(f.Value), maxFieldRunes)}
	}
	c.Fields, c.Statement = fields, visible(c.Statement)
	c.Partial = c.Partial || cut

	out := encode(c)
	if sizeInBody(out) <= maxCardBytes {
		return out
	}
	c.Partial = true
	stmt := []rune(c.Statement)
	at := func(n int) string {
		c.Statement = string(stmt[:n]) + "…"
		return encode(c)
	}
	// 第一个放不下的长度,前一个就是最长的放得下的。
	n := sort.Search(len(stmt), func(n int) bool { return sizeInBody(at(n)) > maxCardBytes })
	return at(max(n-1, 0))
}

func encode(c chat.Card) string {
	tr := func(key string) string { return chat.Tr(c.Lang, key) }
	var els []obj
	if len(c.Fields) > 0 {
		var fs []obj
		for _, f := range c.Fields {
			fs = append(fs, obj{"is_short": true, "text": plain(f.Label + ": " + f.Value)})
		}
		els = append(els, obj{"tag": "div", "fields": fs})
	}
	if c.Reason != "" {
		els = append(els, div(tr("field.reason")+": "+c.Reason))
	}
	if c.Statement != "" {
		// 语句能有多行,前后用分隔线圈住,免得它的内容看着像卡片自己的话。
		els = append(els, obj{"tag": "hr"}, div(c.Statement), obj{"tag": "hr"})
	}
	if c.Outcome != "" {
		els = append(els, div(c.Outcome))
	} else {
		if c.Partial {
			els = append(els, div("⚠️ "+tr("partial")))
		}
		els = append(els, buttons(c, tr)...)
	}
	return marshal(obj{
		"config":   obj{"update_multi": true, "enable_forward": false, "wide_screen_mode": true},
		"header":   obj{"title": plain(c.Title), "template": template(c)},
		"elements": els,
	})
}

// buttons 是还没有结论的卡片下面那一排。「通过」用飞书自己的确认框,点的就是确认(ActConfirm);拒绝在
// 表单里,为的是带上原因输入框。Confirming 在这里用不上。
func buttons(c chat.Card, tr func(string) string) []obj {
	press := func(label, kind string, a chat.Action) obj {
		return obj{"tag": "button", "text": plain(tr(label)), "type": kind, "name": string(a),
			"value": obj{"t": c.Token, "a": string(a)}}
	}
	var row, els []obj
	switch {
	case c.Test:
		row = append(row, press("title.test", "primary", chat.ActTest))
	case c.Token != "":
		ask := tr("confirm.approve")
		if c.Partial {
			ask += "\n" + tr("partial")
		}
		approve := press("btn.approve", "primary", chat.ActConfirm)
		approve["confirm"] = obj{"title": plain(tr("btn.confirm")), "text": plain(ask)}
		row = append(row, approve)
	}
	if u, err := url.Parse(c.DetailURL); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		row = append(row, obj{"tag": "button", "text": plain(tr("btn.detail")), "type": "default", "url": c.DetailURL})
	}
	if len(row) > 0 {
		els = append(els, obj{"tag": "action", "actions": row})
	}
	if c.Token != "" && !c.Test {
		reject := press("btn.reject", "danger", chat.ActReject)
		reject["action_type"] = "form_submit"
		els = append(els, obj{"tag": "form", "name": "reject_form", "elements": []obj{
			{"tag": "input", "name": reasonInput, "required": false, "max_length": maxRejectRunes,
				"placeholder": plain(tr("reject.placeholder"))},
			reject,
		}})
	}
	return els
}

func template(c chat.Card) string {
	switch {
	case c.Outcome != "":
		return "grey"
	case c.Risk == "high":
		return "red"
	case c.Risk == "mid":
		return "orange"
	}
	return "blue"
}

func plain(s string) obj { return obj{"tag": "plain_text", "content": s} }

func div(s string) obj { return obj{"tag": "div", "text": plain(s)} }

// visible 让看不见的字符露出来:双向控制符能让语句显示成另一句话,零宽字符能藏东西。换行与制表保留,
// 零宽连接符也保留(表情和一些文字靠它们组合)。
func visible(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '‌' || r == '‍':
			b.WriteRune(r)
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// oneLine 用于只该占一行的值:里面的换行能伪造出卡片上的下一行。
func oneLine(s string) string {
	return visible(strings.Join(strings.Fields(strings.ToValidUTF8(s, "�")), " "))
}

// marshal 不做 HTML 转义:卡片不进网页,转义只会让 < > & 白占地方。
func marshal(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}" // 里面只有字符串、布尔与数字,编不出来的情况不存在
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// sizeInBody 是卡片放进请求体之后占多少字节。按做 HTML 转义算(点击应答由 SDK 编码,它会转义),只多不少。
func sizeInBody(card string) int {
	b, _ := json.Marshal(card)
	return len(b)
}
