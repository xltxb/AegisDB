package lark

import (
	"context"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

func TestSendCard_ShowsTheReasonVerbatim(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	_, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{
		ApNo: "AP-1", Reason: "[查看详情](https://evil.example) `x`", Command: "SELECT 1 WHERE a < b && c",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := srv.Calls("send")[0].Str("content")
	// 原因走 plain_text:发起人写的 markdown 链接在审批人的卡片里只是一段字,反引号也原样显示。
	if !strings.Contains(content, "\"content\":\"原因:[查看详情](https://evil.example) `x`\",\"tag\":\"plain_text\"") {
		t.Fatalf("reason must be plain text: %s", content)
	}
}

func TestInteractiveCard_KeepsTheConsoleLinkAndPutsButtonsBeforeTheNote(t *testing.T) {
	card := interactiveCard(imchannel.ApprovalCard{ApNo: "AP-1", ConsoleURL: "https://gw/approvals"})
	els := card["elements"].([]any)
	last := els[len(els)-1].(map[string]any)
	if last["tag"] != "note" {
		t.Fatalf("the note stays last, got %v", last["tag"])
	}
	action := els[len(els)-2].(map[string]any)
	acts := action["actions"].([]any)
	if action["tag"] != "action" || len(acts) != 3 {
		t.Fatalf("approve + reject + console link, got %+v", action)
	}
	if acts[2].(map[string]any)["url"] != "https://gw/approvals" {
		t.Fatalf("console link = %+v", acts[2])
	}
	for _, e := range els[:len(els)-2] {
		if e.(map[string]any)["tag"] == "action" {
			t.Fatal("the notify card's own link button must not survive next to the decision buttons")
		}
	}
}
