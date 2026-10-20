package slack

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

func blocksJSON(t *testing.T, blocks []any) string {
	t.Helper()
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return string(b)
}

// block 是测试里读 blocks 用的形状:header / section 的 text、rich_text 的嵌套文本、actions 的按钮。
type block struct {
	Type    string `json:"type"`
	BlockID string `json:"block_id"`
	Text    *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"text"`
	Elements []struct {
		Type     string `json:"type"`
		Text     any    `json:"text"`
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
		URL      string `json:"url"`
		Style    string `json:"style"`
		Elements []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"elements"`
	} `json:"elements"`
}

func decodeBlocks(t *testing.T, blocks []any) []block {
	t.Helper()
	var out []block
	if err := json.Unmarshal([]byte(blocksJSON(t, blocks)), &out); err != nil {
		t.Fatalf("decode blocks: %v", err)
	}
	return out
}

func TestCardBlocks_ShowEveryExternalTextVerbatimAsPlainText(t *testing.T) {
	c := imchannel.ApprovalCard{
		ApNo: "AP-2294", Kind: "sql", Env: "prod", Tier: "core", Instance: "pg-main", Database: "orders",
		Risk: model.RiskHigh, Initiator: "张伟",
		Reason:  "修数据 [点我](https://evil.example) <!channel> *粗体*",
		Command: "DELETE FROM t WHERE a < b && c > 0; -- ``` <@U0ADMIN>",
	}
	blocks := decodeBlocks(t, cardBlocks(c))
	// 没有一处 mrkdwn:发起人写的链接、@channel、反引号都不能被 Slack 渲染出来
	if js := blocksJSON(t, cardBlocks(c)); strings.Contains(js, `"mrkdwn"`) {
		t.Fatalf("external text must not be mrkdwn: %s", js)
	}
	if h := blocks[0]; h.Type != "header" || h.Text.Type != "plain_text" || h.Text.Text != "🔴 高危 · 高危命令待审批 · AP-2294" {
		t.Fatalf("header = %+v", h)
	}
	if s := blocks[1].Text.Text; !strings.Contains(s, "PROD(core)") || !strings.Contains(s, "pg-main / orders") || !strings.Contains(s, "发起人 张伟") {
		t.Fatalf("summary = %q", s)
	}
	var reason, command string
	for _, b := range blocks {
		if b.Type == "section" && b.Text != nil && strings.HasPrefix(b.Text.Text, "原因 ") {
			reason = b.Text.Text
		}
		if b.Type == "rich_text" && len(b.Elements) == 1 && b.Elements[0].Type == "rich_text_preformatted" {
			command = b.Elements[0].Elements[0].Text
		}
	}
	if reason != "原因 "+c.Reason {
		t.Errorf("reason = %q", reason)
	}
	if command != c.Command {
		t.Errorf("the command must reach Slack unchanged, got %q", command)
	}
}

func TestCardBlocks_ButtonsSayOnlyWhichTicketAndWhatAction(t *testing.T) {
	actions := func(c imchannel.ApprovalCard) block {
		for _, b := range decodeBlocks(t, cardBlocks(c)) {
			if b.Type == "actions" {
				return b
			}
		}
		t.Fatal("no actions block")
		return block{}
	}
	a := actions(imchannel.ApprovalCard{ApNo: "AP-2294", ConsoleURL: "https://gw.corp.io/approvals"})
	if a.BlockID != blockActions || len(a.Elements) != 3 {
		t.Fatalf("actions = %+v", a)
	}
	approve, reject, open := a.Elements[0], a.Elements[1], a.Elements[2]
	if approve.ActionID != actionApprove || approve.Value != "ap:AP-2294:a" || approve.Style != "primary" {
		t.Errorf("approve = %+v", approve)
	}
	if reject.ActionID != actionReject || reject.Value != "ap:AP-2294:r" || reject.Style != "danger" {
		t.Errorf("reject = %+v", reject)
	}
	if open.ActionID != actionOpen || open.URL != "https://gw.corp.io/approvals" || open.Value != "" {
		t.Errorf("console = %+v", open)
	}
	if a := actions(imchannel.ApprovalCard{ApNo: "AP-2294"}); len(a.Elements) != 2 {
		t.Errorf("no console URL → no link button, got %d buttons", len(a.Elements))
	}
	if a := actions(imchannel.ApprovalCard{ApNo: "AP-2294", ConsoleURL: "https://gw.corp.io/" + strings.Repeat("x", urlLimit)}); len(a.Elements) != 2 {
		t.Errorf("a console URL over Slack's limit would get the whole card refused; got %d buttons", len(a.Elements))
	}
}

func TestCardBlocks_LongTextIsClippedToSlacksLimits(t *testing.T) {
	long := strings.Repeat("长", 5000)
	for _, b := range decodeBlocks(t, cardBlocks(imchannel.ApprovalCard{ApNo: "AP-1", Instance: long, Initiator: "x", Reason: long, Command: long})) {
		switch b.Type {
		case "header":
			if n := utf8.RuneCountInString(b.Text.Text); n > headerLimit {
				t.Errorf("header is %d characters", n)
			}
		case "section":
			if n := utf8.RuneCountInString(b.Text.Text); n > sectionLimit {
				t.Errorf("section is %d characters", n)
			}
		case "rich_text":
			if n := utf8.RuneCountInString(b.Elements[0].Elements[0].Text); n > commandLimit {
				t.Errorf("command is %d characters", n)
			}
		}
	}
}

func TestCardBlocks_LeaveOutAnEmptyReasonAndCommand(t *testing.T) {
	// Slack 拒收空的 text 对象:没有原因、没有命令的单子(执行窗口申请)不能因此发不出去。
	for _, b := range decodeBlocks(t, cardBlocks(imchannel.ApprovalCard{ApNo: "AP-1", Kind: "window", Initiator: "x", Reason: "  "})) {
		if b.Type == "rich_text" || (b.Text != nil && strings.TrimSpace(b.Text.Text) == "") {
			t.Fatalf("empty block %+v", b)
		}
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "原因") {
			t.Fatalf("a blank reason must be left out: %+v", b)
		}
	}
}

func TestCardText_IsTheEscapedNotificationPreview(t *testing.T) {
	if got := mrkdwnEscape("a < b && c > d"); got != "a &lt; b &amp;&amp; c &gt; d" {
		t.Fatalf("escape = %q", got)
	}
	if got := cardText(imchannel.ApprovalCard{ApNo: "AP-1", Kind: "export"}); got != "AegisDB · 敏感字段导出待审批 · AP-1" {
		t.Fatalf("preview = %q", got)
	}
	if got := closedText("AP-1", imchannel.CardState{Text: "已由 A&B 通过"}); got != "AegisDB · 工单 AP-1 · 已由 A&amp;B 通过" {
		t.Fatalf("closed preview = %q", got)
	}
}

func TestCardRef_RoundTripsAndRefusesAnythingElse(t *testing.T) {
	ref := cardRef("D0123ABC", "1700000000.000100", "AP-2294")
	ch, ts, ap, ok := parseRef(ref)
	if !ok || ch != "D0123ABC" || ts != "1700000000.000100" || ap != "AP-2294" {
		t.Fatalf("parseRef(%q) = %q %q %q %v", ref, ch, ts, ap, ok)
	}
	for _, bad := range []string{
		"", "D0123ABC|1700000000.000100", // 缺段
		"C0123ABC|1700000000.000100|AP-1",   // 卡片只发在私聊里
		"D0123ABC|abc|AP-1",                 // ts 不是数字
		"D0123ABC|1700000000.000100|AP 1",   // 单号里有空格
		"D0123ABC|1700000000.000100|AP-1|x", // 多一段
	} {
		if _, _, _, ok := parseRef(bad); ok {
			t.Errorf("parseRef(%q) accepted", bad)
		}
	}
}

func TestParseValue_KnowsOnlyTheGatewaysOwnButtons(t *testing.T) {
	if ap, approve, ok := parseValue("ap:AP-2294:a"); !ok || ap != "AP-2294" || !approve {
		t.Fatalf("approve = %q %v %v", ap, approve, ok)
	}
	if ap, approve, ok := parseValue("ap:AP-2294:r"); !ok || ap != "AP-2294" || approve {
		t.Fatalf("reject = %q %v %v", ap, approve, ok)
	}
	for _, bad := range []string{"", "AP-2294", "ap:AP 1:a", "ap:AP-2294:x", "ap:AP-2294:a:extra", "ap::a"} {
		if _, _, ok := parseValue(bad); ok {
			t.Errorf("parseValue(%q) accepted", bad)
		}
	}
}

func TestClosedBlocks_SwapTheButtonsForTheOutcomeAndKeepTheRest(t *testing.T) {
	c := imchannel.ApprovalCard{ApNo: "AP-2294", Initiator: "张伟", Reason: "清理", Command: "DROP TABLE orders;"}
	var original []json.RawMessage
	if err := json.Unmarshal([]byte(blocksJSON(t, cardBlocks(c))), &original); err != nil {
		t.Fatal(err)
	}
	closed := closedBlocks(original, "AP-2294", imchannel.CardState{Status: model.StatusApproved, Text: "已由 李四(Slack) 通过"})
	js := blocksJSON(t, closed)
	if strings.Contains(js, blockActions) || strings.Contains(js, actionApprove) || strings.Contains(js, actionOpen) {
		t.Fatalf("closed card still has buttons: %s", js)
	}
	if !strings.Contains(js, "DROP TABLE orders;") || !strings.Contains(js, "清理") || !strings.Contains(js, "✅ 已由 李四(Slack) 通过") {
		t.Fatalf("closed card must keep the ticket and show the outcome: %s", js)
	}
	// 终态占了原来那组按钮的位置:块数不变,页脚仍是最后一块
	got := decodeBlocks(t, closed)
	if len(got) != len(original) || got[len(got)-1].Type != "context" {
		t.Fatalf("closed blocks = %+v", got)
	}
}

func TestClosedBlocks_WithoutTheOriginalKeepTheTicketAndTheOutcome(t *testing.T) {
	for status, icon := range map[string]string{
		model.StatusRejected: "❌", model.StatusExpired: "⏱", model.StatusCancelled: "↩",
	} {
		js := blocksJSON(t, closedBlocks(nil, "AP-2294", imchannel.CardState{Status: status, Text: "已处理"}))
		if !strings.Contains(js, "工单 AP-2294 · 已处理") || !strings.Contains(js, icon+" 已处理") || strings.Contains(js, blockActions) {
			t.Errorf("%s: %s", status, js)
		}
	}
}
