package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"aegisdb/internal/chat"
)

var testCard = strings.Repeat("Tk_-", 8) // 与 service 的令牌一样长:32 个字符

func pendingCard() chat.Card {
	return chat.Card{
		Title: "高危命令待审批 · AP-1024", Risk: "high", Lang: "zh", DetailURL: "https://db.example.com/#/approvals", Token: testCard,
		Fields:    []chat.Field{{Label: "单号", Value: "AP-1024"}, {Label: "实例", Value: "order-cluster"}},
		Statement: "DROP TABLE orders_2024_q3;", Reason: "清理过期表",
	}
}

func u16(s string) int { return len(utf16.Encode([]rune(s))) }

var (
	tagShape    = regexp.MustCompile(`</?[a-z]+>`)
	entityShape = regexp.MustCompile(`&(amp|lt|gt|quot);`)
)

// parseHTML 按 Telegram 的规矩解一遍:只许这几个不带属性的标签、正确嵌套、别处没有裸的 < > & "。
// 返回解完之后的文字,4096 是按它算的。
func parseHTML(t *testing.T, s string) string {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("不是合法的 UTF-8")
	}
	var stack []string
	var out strings.Builder
	for s != "" {
		loc := tagShape.FindStringIndex(s)
		text := s
		if loc != nil {
			text = s[:loc[0]]
		}
		if bare := entityShape.ReplaceAllString(text, ""); strings.ContainsAny(bare, `<>&"`) {
			t.Fatalf("没转义:%q", bare)
		}
		out.WriteString(html.UnescapeString(text))
		if loc == nil {
			break
		}
		tag := s[loc[0]:loc[1]]
		s = s[loc[1]:]
		name := strings.Trim(tag, "</>")
		if name != "b" && name != "code" && name != "pre" {
			t.Fatalf("不该有的标签 %s", tag)
		}
		if !strings.HasPrefix(tag, "</") {
			stack = append(stack, name)
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != name {
			t.Fatalf("标签不配对:%s,栈 %v", tag, stack)
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) != 0 {
		t.Fatalf("没关的标签 %v", stack)
	}
	return out.String()
}

// datas 是按钮上的 callback_data,按先后。
func datas(m message) (out []string) {
	for _, row := range m.keyboard {
		for _, b := range row {
			if b.Data != "" {
				out = append(out, b.Data)
			}
		}
	}
	return out
}

func labels(m message) (out []string) {
	for _, row := range m.keyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

func TestRender_Pending(t *testing.T) {
	m := render(pendingCard())
	text := parseHTML(t, m.text)
	for _, want := range []string{"🔴 高危命令待审批 · AP-1024", "单号: AP-1024", "实例: order-cluster", "原因: 清理过期表", "DROP TABLE orders_2024_q3;"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q:\n%s", want, text)
		}
	}
	if !strings.Contains(m.text, "<pre>DROP TABLE orders_2024_q3;</pre>") {
		t.Errorf("语句要放在 pre 里:%s", m.text)
	}
	if strings.Contains(text, "未显示全文") || strings.Contains(text, "确定通过") {
		t.Errorf("不该有的提示:%s", text)
	}
	if got, want := fmt.Sprint(datas(m)), fmt.Sprintf("[a:%s r:%s]", testCard, testCard); got != want {
		t.Errorf("按钮 %s,应为 %s", got, want)
	}
	if got := fmt.Sprint(labels(m)); got != "[通过 拒绝 查看详情]" {
		t.Errorf("按钮 %s", got)
	}
	if last := m.keyboard[len(m.keyboard)-1]; last[0].URL != "https://db.example.com/#/approvals" {
		t.Errorf("查看详情 %+v", last)
	}
}

// 二次确认:按钮换成「确认通过 / 返回」,正文问一句;没显示全文的,这里也写明。
func TestRender_Confirming(t *testing.T) {
	c := pendingCard()
	c.Confirming, c.Partial = true, true
	m := render(c)
	text := parseHTML(t, m.text)
	if !strings.Contains(text, "确定通过这张审批单吗") || !strings.Contains(text, "未显示全文") {
		t.Errorf("正文:%s", text)
	}
	if got, want := fmt.Sprint(datas(m)), fmt.Sprintf("[c:%s b:%s]", testCard, testCard); got != want {
		t.Errorf("按钮 %s,应为 %s", got, want)
	}
	if got := fmt.Sprint(labels(m)); got != "[确认通过 返回 查看详情]" {
		t.Errorf("按钮 %s", got)
	}
}

func TestRender_OutcomeHasNoButtons(t *testing.T) {
	c := pendingCard()
	c.Outcome, c.Partial, c.Confirming = "✅ 已由 王芳 通过 · 11-25 19:40", true, true
	m := render(c)
	// 结论里有人的名字,也放进 code;结论自己带记号,标题不再带一个
	if !strings.Contains(m.text, "<code>"+c.Outcome+"</code>") || !strings.HasPrefix(m.text, "<b>"+c.Title) {
		t.Errorf("正文:%s", m.text)
	}
	if text := parseHTML(t, m.text); !strings.Contains(text, c.Outcome) || strings.Contains(text, "未显示全文") || strings.Contains(text, "确定通过") {
		t.Errorf("正文:%s", text)
	}
	if m.keyboard == nil || len(m.keyboard) != 0 {
		t.Errorf("要明说没有按钮:%+v", m.keyboard)
	}
}

func TestRender_TestCard(t *testing.T) {
	m := render(chat.Card{Title: "Test card", Risk: "low", Lang: "en", Token: testCard, Test: true})
	parseHTML(t, m.text)
	if got := fmt.Sprint(datas(m)); got != "[t:"+testCard+"]" || fmt.Sprint(labels(m)) != "[Test card]" {
		t.Errorf("按钮 %s %v", got, labels(m))
	}
}

func TestRender_Buttons(t *testing.T) {
	for _, d := range datas(render(pendingCard())) {
		if len(d) > 64 {
			t.Errorf("callback_data %d 字节", len(d))
		}
	}
	// 没有令牌、令牌放不进去或有怪字符:不给按钮,别让平台整条退回来
	for _, token := range []string{"", strings.Repeat("a", 63), "a:b", "a b", "<x>"} {
		c := pendingCard()
		c.Token = token
		if d := datas(render(c)); len(d) != 0 {
			t.Errorf("令牌 %q 的按钮 %v", token, d)
		}
	}
	for _, u := range []string{"", "javascript:alert(1)", "tg://resolve?domain=x", "https://", "ftp://a/b", "https://a/" + strings.Repeat("x", 2000)} {
		c := pendingCard()
		c.DetailURL = u
		if got := fmt.Sprint(labels(render(c))); got != "[通过 拒绝]" {
			t.Errorf("%q → %s", u, got)
		}
	}
}

// 平台的标记、换行、双向控制符都不能让内容变成别的样子。
func TestRender_Escapes(t *testing.T) {
	evil := `</pre><a href="tg://x">通过</a><b>&amp;" & <i>` + "`*_[x](y)"
	c := pendingCard()
	c.Title, c.Statement, c.Reason, c.Outcome = evil, evil+"\nSELECT 1", evil+"\n伪造的一行", evil
	c.Fields = []chat.Field{{Label: evil, Value: evil + "\r\n单号: AP-1"}}
	text := parseHTML(t, render(c).text)
	for _, want := range []string{evil + "\nSELECT 1", "原因: " + evil + " 伪造的一行", evil + ": " + evil + " 单号: AP-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q:\n%s", want, text)
		}
	}
	c = pendingCard()
	c.Statement = "SELECT 'a\u202eb\u200bc\x00d\xff'"
	if text := parseHTML(t, render(c).text); !strings.Contains(text, `SELECT 'a\u202eb\u200bc\u0000d�'`) {
		t.Errorf("看不见的字符:%q", text)
	}
}

// 4096 按解完实体之后的 UTF-16 计;放不下就截语句并写明,不劈开字符、实体与标签。
func TestRender_Fits(t *testing.T) {
	for name, unit := range map[string]string{"ascii": "a", "要转义的": `&<>"`, "汉字": "汉", "表情": "😀", "混着": "a😀&汉<"} {
		t.Run(name, func(t *testing.T) {
			c := pendingCard()
			c.Statement = strings.Repeat(unit, 6000)
			m := render(c)
			text := parseHTML(t, m.text)
			if n := u16(text); n > 4096 || n < 4090 {
				t.Errorf("%d 个单位", n)
			}
			if len(m.text) > 1<<15 {
				t.Errorf("原文 %d 字节", len(m.text))
			}
			if !strings.Contains(text, "…") || !strings.Contains(text, "未显示全文") || len(datas(m)) != 2 {
				t.Errorf("截短了要写明:%s", text[len(text)-200:])
			}
			if strings.Contains(text, "�") {
				t.Error("劈开了一个字符")
			}
			// 再长一倍,画出来一样:更新、返回时前后一致
			c.Partial = true
			if again := render(c); again.text != m.text {
				t.Error("同样的内容画出来不一样")
			}
		})
	}
	// 恰好放得下的不截
	c := pendingCard()
	c.Statement = ""
	room := 4096 - u16(parseHTML(t, render(c).text)) - len("\n\n")
	c.Statement = strings.Repeat("x", room)
	if m := render(c); u16(parseHTML(t, m.text)) != 4096 || strings.Contains(m.text, "未显示全文") {
		t.Errorf("恰好放得下的截了:%d", u16(parseHTML(t, m.text)))
	}
	c.Statement += "x"
	if m := render(c); u16(parseHTML(t, m.text)) > 4096 || !strings.Contains(m.text, "未显示全文") {
		t.Error("多一个字就该截")
	}
}

// 语句之外的东西长得离谱也要发得出去。
func TestRender_HugeEverything(t *testing.T) {
	long := strings.Repeat(`&"😀`, 5000)
	c := pendingCard()
	c.Title, c.Reason, c.Statement = long, long, long
	for range 200 {
		c.Fields = append(c.Fields, chat.Field{Label: long, Value: long})
	}
	for _, outcome := range []string{"", long} {
		c.Outcome = outcome
		m := render(c)
		if n := u16(parseHTML(t, m.text)); n > 4096 || len(m.text) > 1<<15 {
			t.Errorf("%d 个单位,原文 %d 字节", n, len(m.text))
		}
		if outcome == "" && !strings.Contains(m.text, "未显示全文") {
			t.Error("要写明未显示全文")
		}
	}
}

// 没有语句的卡片(默认不带 SQL):不留空的 pre;没有原因也不留空行。
func TestRender_WithoutSQL(t *testing.T) {
	c := pendingCard()
	c.Statement, c.Reason, c.Partial = "", "", true
	m := render(c)
	text := parseHTML(t, m.text)
	if strings.Contains(m.text, "<pre>") || strings.Contains(text, "原因") || !strings.Contains(text, "未显示全文") {
		t.Errorf("正文:%s", text)
	}
}

func TestClip(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{{"abc", 3, "abc"}, {"abcd", 3, "ab…"}, {"😀😀", 4, "😀😀"}, {"😀😀😀", 4, "😀…"}, {"😀😀", 2, "…"}, {"", 5, ""}} {
		if got, cut := clipUTF16(c.in, c.max); got != c.want || cut != (c.in != c.want) || u16(got) > c.max {
			t.Errorf("%q/%d → %q %v", c.in, c.max, got, cut)
		}
	}
}
