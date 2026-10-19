package dingtalk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
)

func TestCardParams_ArePlainTextCarryingEveryTemplateVariable(t *testing.T) {
	p := cardParams(imchannel.ApprovalCard{
		ApNo: "AP-1", Env: "prod", Tier: "prod-core", Instance: "pg-main", Risk: "high", Initiator: "张伟",
		Reason: "[查看详情](https://evil.example) `x`", Command: "SELECT 1", ConsoleURL: "https://gw.corp.io/approvals",
	})
	for _, k := range []string{paramTitle, paramSummary, paramReason, paramCommand, paramConsole, paramStatus, paramResult, paramTip} {
		if _, ok := p[k]; !ok {
			t.Errorf("template variable %q missing", k)
		}
	}
	// 模板里用「文本」组件:原因原样交过去,不转义、不改写 —— 纯文本里没有能变成链接的东西。
	if p[paramReason] != "[查看详情](https://evil.example) `x`" {
		t.Errorf("reason must be passed verbatim, got %q", p[paramReason])
	}
	if !strings.Contains(p[paramTitle], "🔴") || !strings.Contains(p[paramTitle], "AP-1") {
		t.Errorf("title = %q", p[paramTitle])
	}
	for _, want := range []string{"PROD(prod-core)", "pg-main / 默认", "张伟"} {
		if !strings.Contains(p[paramSummary], want) {
			t.Errorf("summary %q missing %q", p[paramSummary], want)
		}
	}
	if p[paramConsole] != "https://gw.corp.io/approvals" || p[paramStatus] != statusPending || p[paramResult] != "" || p[paramTip] != "" {
		t.Errorf("params = %+v", p)
	}
}

func TestCardParams_ClipTheCommand(t *testing.T) {
	long := strings.Repeat("长", commandLimit+10)
	got := cardParams(imchannel.ApprovalCard{ApNo: "AP-1", Command: long})[paramCommand]
	if utf8.RuneCountInString(got) != commandLimit+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped command has %d runes", utf8.RuneCountInString(got))
	}
}

func TestClosedParams_OnlyTouchTheStatusVariables(t *testing.T) {
	p := closedParams(imchannel.CardState{Status: "approved", Text: "已由 李四(钉钉) 通过"})
	if len(p) != 3 || p[paramStatus] != statusDone || p[paramResult] != "已由 李四(钉钉) 通过" || p[paramTip] != "" {
		t.Fatalf("closed params = %+v", p)
	}
}

func TestTrackID_RoundTrips(t *testing.T) {
	id, err := newTrackID("AP-2294")
	if err != nil {
		t.Fatal(err)
	}
	if ap, ok := trackApNo(id); !ok || ap != "AP-2294" {
		t.Fatalf("trackApNo(%q) = %q, %v", id, ap, ok)
	}
	if other, _ := newTrackID("AP-2294"); other == id {
		t.Fatal("two cards of the same approval need different outTrackIds")
	}
	for _, bad := range []string{
		"", "AP-2294", "aegis-AP-2294", "aegis-AP 1-0123456789abcdef",
		"aegis-AP-1-0123456789ABCDEF", "x-aegis-AP-1-0123456789abcdef",
	} {
		if _, ok := trackApNo(bad); ok {
			t.Errorf("trackApNo(%q) must be refused", bad)
		}
	}
	if _, err := newTrackID("AP 1"); err == nil {
		t.Fatal("an approval number outside the whitelist cannot name a card")
	}
}
