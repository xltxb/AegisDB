package telegram_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{Settings: s, HTTP: &http.Client{Timeout: 5 * time.Second}, ValidateURL: func(string) error { return nil }}
}

func newChannel(t *testing.T, srv *tgtest.Server, extra kv) *telegram.Channel {
	t.Helper()
	s := kv{"im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL}
	for k, v := range extra {
		s[k] = v
	}
	ch, _ := telegram.New(deps(s)).(*telegram.Channel)
	if ch == nil {
		t.Fatal("configured channel must build")
	}
	return ch
}

func TestNew_UnconfiguredIsNil(t *testing.T) {
	if ch := telegram.New(deps(kv{})); ch != nil {
		t.Fatalf("no token → nil, got %#v", ch)
	}
}

func TestSendCard_PostsHTMLWithTwoButtons(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	ref, err := ch.SendCard(context.Background(), "777", imchannel.ApprovalCard{ApNo: "AP-1", Command: "DROP TABLE t;"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "777:101" {
		t.Fatalf("ref = %q, want chat:message", ref)
	}
	c := srv.Calls("sendMessage")[0]
	if c.Str("chat_id") != "777" || c.Str("parse_mode") != "HTML" || !strings.Contains(c.Str("reply_markup"), "ap:AP-1:a") {
		t.Fatalf("sendMessage params = %+v", c.Params)
	}
}

func TestCloseCard_ReplacesTheButtonsAndToleratesGoneMessages(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(Telegram) 通过"}
	if err := ch.CloseCard(context.Background(), "777:101", final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("editMessageReplyMarkup")[0]
	if c.Str("chat_id") != "777" || c.Str("message_id") != "101" || !strings.Contains(c.Str("reply_markup"), "已由 李四") ||
		!strings.Contains(c.Str("reply_markup"), `"callback_data":"done"`) {
		t.Fatalf("edit params = %+v", c.Params)
	}
	srv.SetFail("editMessageReplyMarkup", tgtest.Failure{Code: 400, Desc: "Bad Request: message to edit not found"})
	if err := ch.CloseCard(context.Background(), "777:102", final); err != nil {
		t.Fatalf("a message the user deleted counts as closed, got %v", err)
	}
	if err := ch.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestCall_ErrorsNeverContainTheToken(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)

	srv.SetFail("sendMessage", tgtest.Failure{Code: 400, Desc: "Bad Request: chat not found"})
	_, err := ch.SendCard(context.Background(), "1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || strings.Contains(err.Error(), srv.Token) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("api error = %v", err)
	}

	srv.Close() // 网络错误:*url.Error 会带上整个 URL
	_, err = ch.SendCard(context.Background(), "1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || strings.Contains(err.Error(), srv.Token) {
		t.Fatalf("network error leaked the token: %v", err)
	}
	if !strings.Contains(err.Error(), "<bot-token>") {
		t.Fatalf("expected the token to be replaced, got %v", err)
	}
}
