package telegram

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
)

var tagRe = regexp.MustCompile(`<[^>]+>`)

// visible 近似 Telegram「解析实体后」的正文:去掉标签,再反转义。上限按它算。
func visible(s string) string { return html.UnescapeString(tagRe.ReplaceAllString(s, "")) }

func TestRenderCard_EscapesHTMLAndStaysUnderTheLimit(t *testing.T) {
	cmd := "SELECT * FROM t WHERE a < b && c > 1; -- </pre><b>fake</b>"
	out := renderCard(imchannel.ApprovalCard{
		ApNo: "AP-7", Kind: "sql", Env: "prod-hk", Tier: "prod", Instance: "order<1>", Risk: "high",
		Initiator: "张三 & 李四", Reason: "<script>", Command: cmd, ConsoleURL: "https://gw/approvals?x=1&y=2",
	})
	for _, bad := range []string{"<script>", "</pre><b>fake", "a < b", "order<1>"} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q leaked into the HTML: %s", bad, out)
		}
	}
	if !strings.Contains(visible(out), cmd) {
		t.Errorf("the command must survive round-trip as text: %s", visible(out))
	}
	if strings.Count(out, "<pre>") != 1 || strings.Count(out, "</pre>") != 1 {
		t.Errorf("exactly one code block expected: %s", out)
	}

	long := renderCard(imchannel.ApprovalCard{ApNo: "AP-8", Command: strings.Repeat("<", 20000), Reason: strings.Repeat("原", 200)})
	if n := utf8.RuneCountInString(visible(long)); n > 4096 {
		t.Fatalf("visible text is %d chars; Telegram's limit is 4096", n)
	}
}

func TestKeyboardAndStateLabel(t *testing.T) {
	kb := keyboard("AP-2301")["inline_keyboard"].([][]map[string]string)
	if kb[0][0]["callback_data"] != "ap:AP-2301:a" || kb[0][1]["callback_data"] != "ap:AP-2301:r" {
		t.Fatalf("keyboard = %+v", kb)
	}
	if len(kb[0][0]["callback_data"]) > 64 {
		t.Fatal("callback_data is capped at 64 bytes")
	}
	if got := stateLabel(imchannel.CardState{Status: "approved", Text: "已由 李四(Telegram) 通过"}); !strings.HasPrefix(got, "✅") {
		t.Fatalf("label = %q", got)
	}
}
