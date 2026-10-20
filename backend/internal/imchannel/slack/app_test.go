package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
	"velagateway/internal/model"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings: s,
		HTTP: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext},
			// 与网关的出站客户端(service.newOutboundClient)一样拒绝跟随重定向
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("重定向已被禁止(防 SSRF)") },
		},
		ValidateURL: func(string) error { return nil },
	}
}

// newTestApp 造一个指向假 Slack 的 *App(Socket Mode,两种 token 都配好)。
func newTestApp(t *testing.T, srv *slacktest.Server, extra kv) *App {
	t.Helper()
	s := kv{
		"im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api",
	}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}

var bg = context.Background()

// richText 取 blocks 里 rich_text_preformatted 的文本(即命令)。
func richText(t *testing.T, blocks string) string {
	t.Helper()
	var bs []struct {
		Type     string `json:"type"`
		Elements []struct {
			Elements []struct {
				Text string `json:"text"`
			} `json:"elements"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(blocks), &bs); err != nil {
		t.Fatalf("blocks %q: %v", blocks, err)
	}
	for _, b := range bs {
		if b.Type == "rich_text" {
			return b.Elements[0].Elements[0].Text
		}
	}
	return ""
}

func TestNew_NeedsTheBotToken(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if ch := New(deps(kv{"im.slack.appToken": "xapp-1"})); ch != nil {
		t.Fatal("an app-level token alone cannot send a card")
	}
	a, ok := New(deps(kv{"im.slack.botToken": "xoxb-1"})).(*App)
	if !ok || a.base != "https://slack.com/api" || a.mode != modeSocket || a.Kind() != imchannel.Slack || a.BotName() != "" {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{"im.slack.botToken": "xoxb-1", "im.slack.mode": "webhook", "im.slack.apiBase": "https://slack.corp.io/api/"})).(*App)
	if a.mode != modeWebhook || a.base != "https://slack.corp.io/api" {
		t.Fatalf("overrides: mode=%s base=%s", a.mode, a.base)
	}
}

func TestSendCard_OpensTheDirectMessageAndPostsTheCard(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	const cmd = "DELETE FROM t WHERE a < b & c;"
	ref, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{
		ApNo: "AP-2294", Command: cmd, Risk: model.RiskHigh, Env: "prod", Instance: "pg-main", Initiator: "张伟",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "D0OWNER|1700000000.000001|AP-2294" {
		t.Fatalf("ref = %q", ref)
	}
	open := srv.Calls("conversations.open")
	if len(open) != 1 || open[0].Get("users") != "U0OWNER" || open[0].Token != srv.BotToken {
		t.Fatalf("conversations.open = %+v", open)
	}
	post := srv.Calls("chat.postMessage")[0]
	if post.Get("channel") != "D0OWNER" || post.Get("unfurl_links") != "false" || post.Get("unfurl_media") != "false" ||
		post.Get("text") != "AegisDB · 高危命令待审批 · AP-2294" {
		t.Fatalf("chat.postMessage = %+v", post.Params)
	}
	// 命令原样进了 rich_text:出网关之前不再改写,也没有被 mrkdwn 转义过
	if got := richText(t, post.Get("blocks")); got != cmd {
		t.Fatalf("command in the card = %q", got)
	}
}

func TestSendCard_RefusesMalformedRecipientsAndTickets(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	for _, c := range []struct{ user, apNo string }{
		{"U0 OWNER", "AP-1"}, {"C0CHANNEL", "AP-1"}, {"", "AP-1"}, {"U0OWNER", "AP 1"},
	} {
		if _, err := a.SendCard(bg, c.user, imchannel.ApprovalCard{ApNo: c.apNo}); err == nil {
			t.Errorf("%+v: accepted", c)
		}
	}
	if n := len(srv.Calls("conversations.open")) + len(srv.Calls("chat.postMessage")); n != 0 {
		t.Fatalf("%d requests went out for malformed input", n)
	}
}

func TestSendCard_AMissingScopeNamesTheScope(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("chat.postMessage", slacktest.Failure{Error: "missing_scope", Needed: "chat:write"})
	_, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !permanent(err) || !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "chat:write") {
		t.Fatalf("err = %v", err)
	}
}

func sendAndClose(t *testing.T, srv *slacktest.Server, a *App, final imchannel.CardState) (ref string, updates []slacktest.Call) {
	t.Helper()
	ref, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{ApNo: "AP-2294", Initiator: "张伟", Command: "DROP TABLE orders;"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CloseCard(bg, ref, final); err != nil {
		t.Fatalf("close: %v", err)
	}
	return ref, srv.Calls("chat.update")
}

func TestCloseCard_KeepsTheCardAndSwapsTheButtonsForTheOutcome(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusApproved, Text: "已由 李四(Slack) 通过"})
	if len(updates) != 1 {
		t.Fatalf("chat.update called %d times", len(updates))
	}
	u := updates[0]
	h := srv.Calls("conversations.history")
	if len(h) != 1 || h[0].Get("latest") != u.Get("ts") || h[0].Get("inclusive") != "true" || h[0].Get("limit") != "1" {
		t.Fatalf("history = %+v", h)
	}
	blocks := u.Get("blocks")
	if u.Get("channel") != "D0OWNER" || strings.Contains(blocks, blockActions) || !strings.Contains(blocks, "已由 李四(Slack) 通过") ||
		richText(t, blocks) != "DROP TABLE orders;" {
		t.Fatalf("chat.update = %+v", u.Params)
	}
}

func TestCloseCard_FallsBackWhenTheOriginalCannotBeRead(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("conversations.history", slacktest.Failure{Error: "missing_scope", Needed: "im:history"})
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusExpired, Text: "已超时"})
	if len(updates) != 1 || !strings.Contains(updates[0].Get("blocks"), "工单 AP-2294 · 已处理") || strings.Contains(updates[0].Get("blocks"), blockActions) {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestCloseCard_RetriesWithoutTheOriginalWhenSlackRefusesIt(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("chat.update", slacktest.Failure{Error: "invalid_blocks", Times: 1})
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusRejected, Text: "已驳回"})
	if len(updates) != 2 || !strings.Contains(updates[1].Get("blocks"), "工单 AP-2294 · 已处理") {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestCloseCard_ADeletedMessageCountsAsClosed(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	// 对方把消息删了:这张卡片不会再有能点的按钮,算收起,不必每次扫描都 WARN 一遍。
	if err := a.CloseCard(bg, "D0OWNER|1700000000.000099|AP-2294", imchannel.CardState{Text: "已撤回"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if err := a.CloseCard(bg, "not-a-ref", imchannel.CardState{}); err == nil {
		t.Fatal("an unknown ref must be refused")
	}
}

func TestReplies_AreEscapedForMrkdwn(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	if err := a.sendText(bg, "D0OWNER", "已绑定到 张三<zhang@corp.io> & co"); err != nil {
		t.Fatal(err)
	}
	if err := a.postEphemeral(bg, "D0OWNER", "U0OWNER", "a < b"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Calls("chat.postMessage")[0].Get("text"); got != "已绑定到 张三&lt;zhang@corp.io&gt; &amp; co" {
		t.Errorf("reply = %q", got)
	}
	if e := srv.Calls("chat.postEphemeral")[0]; e.Get("text") != "a &lt; b" || e.Get("user") != "U0OWNER" || e.Get("channel") != "D0OWNER" {
		t.Errorf("ephemeral = %+v", e.Params)
	}
}

func TestAuthTest_ReadsTheBotsNameAndID(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	info, err := a.authTest(bg)
	if err != nil || info.name != srv.BotName || info.userID != srv.BotUserID {
		t.Fatalf("auth.test = %+v, %v", info, err)
	}
}

func TestAuthTest_AUserTokenIsRefused(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	// 用户 token(xoxp-)也能过 auth.test,只是没有 bot_id:拿它发的卡片会以那个人的名义出现。
	srv.SetFail("auth.test", slacktest.Failure{Raw: `{"ok":true,"user":"zhangwei","user_id":"U0OWNER","team_id":"T0TEST001"}`})
	if _, err := a.authTest(bg); err == nil || !permanent(err) || !strings.Contains(err.Error(), "xoxb-") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequest_ARejectedTokenIsPermanentAndNeverShown(t *testing.T) {
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token"
	a := newTestApp(t, srv, kv{"im.slack.botToken": wrong})
	_, err := a.authTest(bg)
	if err == nil || !permanent(err) || !strings.Contains(err.Error(), "invalid_auth") ||
		!strings.Contains(err.Error(), "<bot-token>") || strings.Contains(err.Error(), wrong) {
		t.Fatalf("err = %v", err)
	}
}

func TestPermanent_SortsSlacksErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 200, Code: "invalid_auth"}, true},
		{&apiError{HTTP: 200, Code: "not_allowed_token_type"}, true},
		{&apiError{HTTP: 200, Code: "missing_scope"}, true},
		{&apiError{HTTP: 302, Msg: "响应无法解析,检查 API 地址"}, true},
		{&apiError{HTTP: 404}, true},
		{fmt.Errorf("%w: x", errBadBase), true},
		{&apiError{HTTP: 200, Code: "ratelimited"}, false},
		{&apiError{HTTP: 200, Code: "internal_error"}, false},
		{&apiError{HTTP: 200, Code: "fatal_error"}, false},
		{&apiError{HTTP: 200, Code: "service_unavailable"}, false},
		{&apiError{HTTP: 200, Code: "request_timeout"}, false},
		{&apiError{HTTP: http.StatusTooManyRequests}, false},
		{&apiError{HTTP: 503}, false},
		{errors.New("connection refused"), false},
	} {
		if got := permanent(c.err); got != c.want {
			t.Errorf("permanent(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestRequest_AWrongAPIAddressIsPermanent(t *testing.T) {
	for name, f := range map[string]slacktest.Failure{
		"a console page":  {Raw: "<html>console</html>"},
		"a redirect":      {HTTP: http.StatusFound, Location: "https://example.com/", Raw: "moved"},
		"json without ok": {Raw: "{}"},
	} {
		srv := slacktest.New(t)
		a := newTestApp(t, srv, nil)
		srv.SetFail("auth.test", f)
		if _, err := a.authTest(bg); err == nil || !permanent(err) || !strings.Contains(err.Error(), "检查 API 地址") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("auth.test", slacktest.Failure{HTTP: http.StatusBadGateway, Raw: "<html>bad gateway</html>"})
	if _, err := a.authTest(bg); err == nil || permanent(err) {
		t.Errorf("a 5xx page is Slack having a bad moment, not a wrong address: %v", err)
	}
}

func TestRequest_AnAddressRefusedByTheOutboundCheckIsPermanentButDNSIsNot(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	a.validate = func(string) error { return errors.New("解析到内网地址") }
	if _, err := a.authTest(bg); !errors.Is(err, errBadBase) || !permanent(err) {
		t.Fatalf("err = %v", err)
	}
	a.validate = func(string) error { return &net.DNSError{Err: "no such host", Name: "slack.com", IsTemporary: true} }
	if _, err := a.authTest(bg); err == nil || permanent(err) {
		t.Fatalf("a DNS failure may be a passing network problem: %v", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out to a refused address", n)
	}
}
