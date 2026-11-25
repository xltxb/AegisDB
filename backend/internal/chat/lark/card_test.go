package lark

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"aegisdb/internal/chat"
)

func pendingCard() chat.Card {
	return chat.Card{
		Title: "高危命令待审批 · AP-1024", Risk: "high", Lang: "zh", DetailURL: "https://gw.example.com/#/approvals",
		Token:     "tok-0123456789",
		Fields:    []chat.Field{{Label: "实例", Value: "order-cluster"}, {Label: "环境", Value: "PROD"}},
		Statement: "DROP TABLE orders_2024_q3;", Reason: "清理过期表",
	}
}

func decode(t *testing.T, c chat.Card) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(RenderCard(c)), &m); err != nil {
		t.Fatalf("不是合法 JSON:%v", err)
	}
	return m
}

// walk 走遍一份 JSON 里的每个对象。
func walk(v any, f func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		f(x)
		for _, c := range x {
			walk(c, f)
		}
	case []any:
		for _, c := range x {
			walk(c, f)
		}
	}
}

func byTag(v any, tag string) (out []map[string]any) {
	walk(v, func(m map[string]any) {
		if m["tag"] == tag {
			out = append(out, m)
		}
	})
	return
}

func text(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["content"].(string)
	return s
}

// body 是正文里全部文字(不含标题、按钮与确认框)。
func body(m map[string]any) string {
	var b strings.Builder
	for _, d := range byTag(m["elements"], "div") {
		b.WriteString(text(d["text"]) + "\n")
		fields, _ := d["fields"].([]any)
		for _, f := range fields {
			b.WriteString(text(f.(map[string]any)["text"]) + "\n")
		}
	}
	return b.String()
}

func button(m map[string]any, label string) map[string]any {
	for _, b := range byTag(m, "button") {
		if text(b["text"]) == label {
			return b
		}
	}
	return nil
}

func TestRenderCard_Pending(t *testing.T) {
	m := decode(t, pendingCard())
	cfg, _ := m["config"].(map[string]any)
	if cfg["update_multi"] != true || cfg["enable_forward"] != false {
		t.Errorf("config = %v", cfg)
	}
	head, _ := m["header"].(map[string]any)
	if text(head["title"]) != "高危命令待审批 · AP-1024" || head["template"] != "red" {
		t.Errorf("header = %v", head)
	}
	for _, want := range []string{"实例: order-cluster", "环境: PROD", "原因: 清理过期表", "DROP TABLE orders_2024_q3;"} {
		if !strings.Contains(body(m), want) {
			t.Errorf("正文缺 %q:\n%s", want, body(m))
		}
	}
	if got := len(byTag(m, "button")); got != 3 {
		t.Fatalf("应有 3 个按钮,有 %d", got)
	}

	approve := button(m, "通过")
	if want := map[string]any{"t": "tok-0123456789", "a": "confirm"}; !reflect.DeepEqual(approve["value"], want) {
		t.Errorf("通过的值 = %v", approve["value"])
	}
	confirm, _ := approve["confirm"].(map[string]any)
	if text(confirm["text"]) != chat.Tr("zh", "confirm.approve") || text(confirm["title"]) == "" {
		t.Errorf("确认框 = %v", confirm)
	}

	forms := byTag(m, "form")
	if len(forms) != 1 || forms[0]["name"] == "" {
		t.Fatalf("form = %v", forms)
	}
	inputs := byTag(forms[0], "input")
	if len(inputs) != 1 || inputs[0]["name"] != "reason" || inputs[0]["required"] != false {
		t.Errorf("原因输入 = %v", inputs)
	}
	reject := button(forms[0], "拒绝")
	if want := map[string]any{"t": "tok-0123456789", "a": "reject"}; !reflect.DeepEqual(reject["value"], want) {
		t.Errorf("拒绝的值 = %v", reject["value"])
	}
	if reject["action_type"] != "form_submit" || reject["name"] == nil {
		t.Errorf("拒绝应提交表单:%v", reject)
	}

	detail := button(m, "查看详情")
	if detail["url"] != "https://gw.example.com/#/approvals" || detail["value"] != nil {
		t.Errorf("查看详情 = %v", detail)
	}
	if !strings.Contains(RenderCard(pendingCard()), "https://gw.example.com/#/approvals") {
		t.Error("卡片里没有详情地址")
	}
}

func TestRenderCard_Variants(t *testing.T) {
	t.Run("没有详情地址", func(t *testing.T) {
		for _, u := range []string{"", "javascript:alert(1)", "lark://x"} {
			c := pendingCard()
			c.DetailURL = u
			if m := decode(t, c); len(byTag(m, "button")) != 2 || button(m, "查看详情") != nil {
				t.Errorf("%q 不该有查看详情", u)
			}
		}
	})
	t.Run("未显示全文", func(t *testing.T) {
		c := pendingCard()
		c.Partial = true
		m := decode(t, c)
		want := chat.Tr("zh", "partial")
		if !strings.Contains(body(m), want) {
			t.Error("正文没写未显示全文")
		}
		confirm, _ := button(m, "通过")["confirm"].(map[string]any)
		if !strings.Contains(text(confirm["text"]), want) {
			t.Error("确认框没写未显示全文")
		}
		if full := decode(t, pendingCard()); strings.Contains(body(full), want) {
			t.Error("显示了全文的卡片不该有这句")
		}
	})
	t.Run("已结束", func(t *testing.T) {
		c := pendingCard()
		c.Outcome, c.Partial = "已由 王芳 通过 · 11-25 19:40", true
		m := decode(t, c)
		if n := len(byTag(m, "button")) + len(byTag(m, "input")) + len(byTag(m, "form")); n != 0 {
			t.Errorf("已结束的卡片还有 %d 个可操作的东西", n)
		}
		if !strings.Contains(body(m), c.Outcome) {
			t.Error("没写结论")
		}
		// 「通过前请核对」对一张已有结论的单没有意义。
		if strings.Contains(body(m), chat.Tr("zh", "partial")) {
			t.Error("已结束的卡片不该再提醒核对")
		}
		if cfg := m["config"].(map[string]any); cfg["update_multi"] != true {
			t.Error("更新后的卡片也要声明 update_multi")
		}
	})
	t.Run("测试卡片", func(t *testing.T) {
		m := decode(t, chat.Card{Title: "测试卡片", Lang: "zh", Test: true, Token: "x"})
		bs := byTag(m, "button")
		if len(bs) != 1 || bs[0]["value"].(map[string]any)["a"] != "test" || bs[0]["confirm"] != nil {
			t.Errorf("测试卡片的按钮 = %v", bs)
		}
	})
	t.Run("没有令牌就没有决定按钮", func(t *testing.T) {
		c := pendingCard()
		c.Token = ""
		if m := decode(t, c); len(byTag(m, "button")) != 1 {
			t.Error("没有令牌的按钮谁也认不出来")
		}
	})
	t.Run("英文", func(t *testing.T) {
		c := pendingCard()
		c.Lang, c.Partial = "en", true
		m := decode(t, c)
		if button(m, "Approve") == nil || button(m, "Reject") == nil || button(m, "View details") == nil {
			t.Error("按钮没有跟卡片语言走")
		}
	})
	t.Run("风险配色", func(t *testing.T) {
		for risk, want := range map[string]string{"high": "red", "mid": "orange", "low": "blue", "": "blue"} {
			c := pendingCard()
			c.Risk = risk
			if got := decode(t, c)["header"].(map[string]any)["template"]; got != want {
				t.Errorf("%q → %v", risk, got)
			}
		}
	})
}

// 一段来自用户的文字只能以 plain_text 的内容出现:不当标记解析,也进不了按钮的值、地址与键。
func TestRenderCard_Adversarial(t *testing.T) {
	for _, evil := range []string{
		"```\nDROP TABLE x\n```",
		"``````", "`a`", "[x](http://evil)", "![i](http://evil/i.png)", "**bold** ~~s~~ *i*",
		`<at id=all></at>`, `<at user_id="all">所有人</at>`, `<a href="http://evil">x</a>`, "<font color='red'>x</font>",
		"${token}", "{{token}}", "&lt;at id=all&gt;", `"}],"elements":[{"tag":"button"}]`, `\"}`, `\u0022}`,
		"# h1\n---\n> q", ":DONE:", "</script>", "a\tb",
	} {
		t.Run(evil, func(t *testing.T) {
			c := pendingCard()
			c.Title, c.Statement, c.Reason = "T "+evil, "S "+evil, "R "+evil
			c.Fields = []chat.Field{{Label: "实例", Value: "V " + evil}}
			c.Partial = true
			for _, outcome := range []string{"", "O " + evil} {
				c.Outcome = outcome
				m := decode(t, c)
				for _, tag := range []string{"lark_md", "markdown", "a", "at", "img"} {
					if len(byTag(m, tag)) != 0 {
						t.Errorf("卡片里有会解析标记的 %s", tag)
					}
				}
				walk(m, func(o map[string]any) {
					for k, v := range o {
						s, ok := v.(string)
						if !ok || !strings.Contains(s, evil) && !strings.Contains(s, strings.Join(strings.Fields(evil), " ")) {
							continue
						}
						if k != "content" || o["tag"] != "plain_text" {
							t.Errorf("出现在 %s(tag %v)", k, o["tag"])
						}
					}
				})
				// 语句要原样看得见,不能因转义走样。
				if !strings.Contains(body(m), "S "+evil) {
					t.Errorf("语句走样:%s", body(m))
				}
				if outcome == "" {
					if len(byTag(m, "button")) != 3 {
						t.Error("按钮数变了")
					}
				} else if len(byTag(m, "button")) != 0 {
					t.Error("已结束却有按钮")
				}
			}
		})
	}
}

// 看着像另一句话的字符要露出来:双向控制符能让 DROP 看着像 PORD,单行的值里换行能伪造出下一行。
func TestRenderCard_InvisibleAndForgedLines(t *testing.T) {
	c := pendingCard()
	c.Statement = "SELECT 1 \u202eDROP\u2066 x\u200b\x00\x1b\u2028y\r\nz"
	c.Reason = "清理\n语句\nSELECT 1\u2029ok"
	c.Fields = []chat.Field{{Label: "实例", Value: "a\n风险: 低危"}}
	m := decode(t, c)
	if got := body(m); !strings.Contains(got, `SELECT 1 \u202eDROP\u2066 x\u200b\u0000\u001b\u2028y`+"\nz") {
		t.Errorf("语句 = %q", got)
	}
	if !strings.Contains(body(m), "原因: 清理 语句 SELECT 1") || !strings.Contains(body(m), "实例: a 风险: 低危") {
		t.Errorf("单行的值里不该有换行:%q", body(m))
	}
}

func TestRenderCard_Size(t *testing.T) {
	for name, unit := range map[string]string{
		"引号": `"`, "反斜杠": `\`, "换行": "\n", "汉字": "表", "表情": "😀", "尖括号": "<", "控制符": "\x01", "坏字节": "a\xff",
	} {
		t.Run(name, func(t *testing.T) {
			c := pendingCard()
			c.Statement = "START" + strings.Repeat(unit, 40000)
			out := RenderCard(c)
			if n := sizeInBody(out); n > maxCardBytes {
				t.Fatalf("放进请求体后 %d 字节", n)
			}
			if !json.Valid([]byte(out)) || !utf8.ValidString(out) {
				t.Fatal("不是合法 JSON / UTF-8")
			}
			m := decode(t, c)
			if !strings.Contains(body(m), "START") || !strings.Contains(body(m), "…") {
				t.Error("应留下开头并标出截断")
			}
			if strings.Contains(body(m), "\uFFFD") && unit != "a\xff" {
				t.Error("截断劈开了字符")
			}
			if !strings.Contains(body(m), chat.Tr("zh", "partial")) {
				t.Error("截断了就要写明未显示全文")
			}
			confirm, _ := button(m, "通过")["confirm"].(map[string]any)
			if !strings.Contains(text(confirm["text"]), chat.Tr("zh", "partial")) {
				t.Error("确认框也要写")
			}
		})
	}
	t.Run("别的字段过长也不超", func(t *testing.T) {
		c := pendingCard()
		c.Title, c.Reason, c.Outcome = strings.Repeat("\"", 9000), strings.Repeat("\"", 90000), ""
		for i := 0; i < 12; i++ {
			c.Fields = append(c.Fields, chat.Field{Label: fmt.Sprint("f", i), Value: strings.Repeat("\"", 9000)})
		}
		if n := sizeInBody(RenderCard(c)); n > maxCardBytes {
			t.Fatalf("%d 字节", n)
		}
		if m := decode(t, c); !strings.Contains(body(m), chat.Tr("zh", "partial")) {
			t.Error("没写未显示全文")
		}
	})
	t.Run("平常的长语句不动", func(t *testing.T) {
		c := pendingCard()
		c.Statement = strings.Repeat("表", 3000)
		if m := decode(t, c); !strings.Contains(body(m), c.Statement) || strings.Contains(body(m), chat.Tr("zh", "partial")) {
			t.Error("放得下就不该截")
		}
	})
}
