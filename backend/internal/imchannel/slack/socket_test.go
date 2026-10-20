package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
)

// recSink 记下交上来的点击与私聊,回一个能看出收到了什么的提示 / 回复。
type recSink struct {
	mu      sync.Mutex
	actions []imchannel.Action
	dms     []imchannel.DM
}

func (s *recSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	s.mu.Lock()
	s.actions = append(s.actions, a)
	s.mu.Unlock()
	return imchannel.ActionResult{Toast: "ok " + a.PlatformUserID + " " + a.ApNo + " " + strconv.FormatBool(a.Approve)}
}

func (s *recSink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	s.mu.Lock()
	s.dms = append(s.dms, dm)
	s.mu.Unlock()
	return "reply to " + dm.Text
}

func (s *recSink) dmList() []imchannel.DM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imchannel.DM(nil), s.dms...)
}

func (s *recSink) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.actions), len(s.dms)
}

// startRun 在后台跑 Run;返回的 stop 取消它并拿到返回值。
func startRun(t *testing.T, a *App, sink imchannel.ActionSink) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				err = errors.New("Run did not return after cancel")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// runUntilError 跑 Run,期望它自己很快返回一个错误(永久性错误 / 配置错误)。
func runUntilError(t *testing.T, a *App) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going; a permanent failure must stop it with a reason")
		return nil
	}
}

// settle 给已确认、仍在协程里处理的信封一点时间:断言「什么都没发生」之前用。
func settle() { time.Sleep(300 * time.Millisecond) }

func TestRun_ChecksTheBotTokenThenConnectsWithTheAppToken(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	var _ imchannel.BotNamer = a
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	auth, open := srv.Calls("auth.test"), srv.Calls("apps.connections.open")
	if len(auth) != 1 || auth[0].Token != srv.BotToken || len(open) != 1 || open[0].Token != srv.AppToken {
		t.Fatalf("auth.test = %+v, apps.connections.open = %+v", auth, open)
	}
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
}

func TestRun_AClickReachesTheSinkAndOnlyTheClickerSeesTheAnswer(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if got := srv.Click(t, "U0OWNER", "AP-2294", "a"); got != "ok U0OWNER AP-2294 true" {
		t.Fatalf("approve → %q", got)
	}
	if got := srv.Click(t, "U0OWNER", "AP-2294", "r"); got != "ok U0OWNER AP-2294 false" {
		t.Fatalf("reject → %q", got)
	}
	if e := srv.Calls("chat.postEphemeral")[0]; e.Get("channel") != "D0OWNER" || e.Get("user") != "U0OWNER" || e.Token != srv.BotToken {
		t.Fatalf("ephemeral = %+v", e)
	}
}

func TestRun_ClicksThatDoNotNameATicketAndAnActionAreDropped(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	bad := []struct{ action, value string }{
		{actionApprove, "AP-2294"},               // 不是网关造的 value
		{actionApprove, "ap:AP 1:a"},             // 单号里有空格
		{actionApprove, "ap:AP-2294:x"},          // 不认识的动作
		{"someone_elses_button", "ap:AP-2294:a"}, // 不是网关的按钮
	}
	for _, c := range bad {
		srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", c.action, c.value))
	}
	for i := range bad {
		if got := srv.WaitEphemeral(t, i); got != "无法识别的操作" {
			t.Errorf("#%d → %q", i, got)
		}
	}
	// 认不出点击者的:无人可回,什么也不发
	srv.Push(t, "interactive", slacktest.Interaction("", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.Push(t, "interactive", slacktest.Interaction("not-a-member", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.Push(t, "interactive", map[string]any{"type": "view_submission", "user": map[string]any{"id": "U0OWNER"}})
	settle()
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d malformed clicks reached the sink", n)
	}
	if n := len(srv.Calls("chat.postEphemeral")); n != len(bad) {
		t.Fatalf("%d ephemeral messages, want %d", n, len(bad))
	}
}

func TestRun_TheConsoleLinkButtonIsAcknowledgedAndIgnored(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	// 链接按钮被点时 Slack 打开链接,同时照样回调一次:不是决策,也不该收到一句「无法识别的操作」。
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionOpen, ""))
	settle()
	if n, _ := sink.counts(); n != 0 || len(srv.Calls("chat.postEphemeral")) != 0 {
		t.Fatalf("a console link click did something: %d actions, %d messages", n, len(srv.Calls("chat.postEphemeral")))
	}
}

func TestRun_ADirectMessageIsPassedOnAndAnswered(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "U0OWNER", "  BIND-7K3QX9MP ")
	post := srv.WaitCall(t, "chat.postMessage", nil)
	if post.Get("channel") != "D0OWNER" || post.Get("text") != "reply to BIND-7K3QX9MP" || post.Get("blocks") != "" {
		t.Fatalf("reply = %+v", post.Params)
	}
	if dms := sink.dmList(); len(dms) != 1 || dms[0].PlatformUserID != "U0OWNER" || dms[0].Text != "BIND-7K3QX9MP" {
		t.Fatalf("dms = %+v", dms)
	}
}

func TestRun_OnlyDirectTextFromPeopleIsRead(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.WaitConnected(t)
	for i, extra := range []map[string]any{
		{"channel_type": "channel", "channel": "C0GENERAL"}, // 频道里的码:就是泄露了,更不能认
		{"channel_type": "mpim", "channel": "G0GROUP"},      // 多人私聊同理
		{"subtype": "message_changed"},                      // 编辑、删除、入群提示 …
		{"bot_id": "B0OTHER"},                               // 机器人发的
		{"user": srv.BotUserID},                             // 网关自己发的
		{"text": "   "},
		{"user": "not-a-member"},
	} {
		srv.Push(t, "events_api", slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", fmt.Sprintf("EvIgnored%d", i), extra))
	}
	srv.Push(t, "events_api", map[string]any{"type": "app_rate_limited", "team_id": "T0TEST001"})
	settle()
	if _, n := sink.counts(); n != 0 {
		t.Fatalf("%d messages reached the sink", n)
	}
	if n := len(srv.Calls("chat.postMessage")); n != 0 {
		t.Fatalf("%d replies were sent", n)
	}
}

func TestRun_ARedeliveredEventIsHandledOnce(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	ev := slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", "EvDup", nil)
	srv.Push(t, "events_api", ev)
	srv.Push(t, "events_api", ev) // 重投的信封照样要确认
	srv.WaitCall(t, "chat.postMessage", nil)
	settle()
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same event_id was handled %d times", n)
	}
}

func TestRun_EveryEnvelopeIsAcknowledged(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	// 网关不处理斜杠命令和没见过的类型,但都得确认 —— 不确认 Slack 会一直重投。Push 自己断言确认。
	srv.Push(t, "slash_commands", map[string]any{"command": "/aegis", "user_id": "U0OWNER"})
	srv.Push(t, "something_new", map[string]any{})
}

func TestRun_ReconnectsWhenAskedAndWhenTheConnectionDrops(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	srv.Disconnect(t, "refresh_requested")
	srv.WaitConnects(t, 2)
	srv.DropConnection()
	srv.WaitConnects(t, 3)
	if n := len(srv.Calls("apps.connections.open")); n != 3 {
		t.Fatalf("each connection needs a fresh ticket; apps.connections.open called %d times", n)
	}
	if n := len(srv.Calls("auth.test")); n != 1 {
		t.Fatalf("reconnecting must not re-check the bot token; auth.test called %d times", n)
	}
	if got := srv.Click(t, "U0OWNER", "AP-2294", "a"); got != "ok U0OWNER AP-2294 true" {
		t.Fatalf("clicks must work after reconnecting, got %q", got)
	}
}

func TestRun_SocketModeTurnedOffInSlackStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	srv.WaitConnected(t)
	srv.Disconnect(t, "link_disabled")
	select {
	case err := <-done:
		if !errors.Is(err, errLinkDisabled) || !permanent(err) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept reconnecting to an app whose Socket Mode was turned off")
	}
	if srv.ConnectCount() != 1 {
		t.Fatalf("%d connections", srv.ConnectCount())
	}
}

func TestRun_StopsWhenCancelledAndStaysStopped(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
	n := srv.ConnectCount()
	time.Sleep(1500 * time.Millisecond)
	if srv.ConnectCount() != n || len(srv.Calls("apps.connections.open")) != n {
		t.Fatal("a cancelled Run must not reconnect: after Reload the old connection would keep taking events")
	}
}

// blockingSink 的 OnAction 停在 release 上,记下当时 ctx 是否已被取消。
type blockingSink struct {
	recSink
	started chan struct{}
	release chan struct{}
	ctxErr  chan error
}

func (s *blockingSink) OnAction(ctx context.Context, _ imchannel.Action) imchannel.ActionResult {
	close(s.started)
	<-s.release
	s.ctxErr <- ctx.Err()
	return imchannel.ActionResult{Toast: "decided"}
}

func TestRun_AClickAcknowledgedBeforeAReloadIsStillDecided(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{}), ctxErr: make(chan error, 1)}
	stop := startRun(t, a, sink)
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the click never reached the sink")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }() // Reload:Run 的 ctx 被取消
	time.Sleep(200 * time.Millisecond)
	close(sink.release)
	if err := <-sink.ctxErr; err != nil {
		t.Fatalf("the envelope was already acknowledged, so Slack will not resend it — a cancelled ctx here loses the decision: %v", err)
	}
	if got := srv.WaitEphemeral(t, 0); got != "decided" {
		t.Fatalf("the clicker must still be told, got %q", got)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// panicSink 处理点击时 panic:那个协程死掉,连接与其他事件不受影响。
type panicSink struct{ recSink }

func (*panicSink) OnAction(context.Context, imchannel.Action) imchannel.ActionResult { panic("boom") }

func TestRun_APanickingHandlerDoesNotTakeTheConnectionDown(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &panicSink{})
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.SendDM(t, "U0OWNER", "BIND-7K3QX9MP")
	srv.WaitCall(t, "chat.postMessage", nil)
	if srv.ConnectCount() != 1 {
		t.Fatalf("%d connections: the panic took the connection down", srv.ConnectCount())
	}
}

func TestRun_ABotTokenInTheAppTokenSlotStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	// 绕过了保存时的前缀校验(比如直接改库):Slack 回 not_allowed_token_type,重连只会被同样拒绝。
	a := newTestApp(t, srv, kv{"im.slack.appToken": srv.BotToken})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "not_allowed_token_type") || strings.Contains(err.Error(), srv.BotToken) {
		t.Fatalf("err = %v", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("no connection with the wrong kind of token")
	}
}

func TestRun_ARejectedBotTokenStopsTheLoopWithoutTheToken(t *testing.T) {
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token"
	a := newTestApp(t, srv, kv{"im.slack.botToken": wrong})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") || !strings.Contains(err.Error(), "<bot-token>") ||
		strings.Contains(err.Error(), wrong) {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("%d socket mode connections attempted with a rejected bot token", n)
	}
}

func TestRun_AWrongAPIAddressStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("auth.test", slacktest.Failure{Raw: "<html>console</html>"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "检查 API 地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_RefusesBeforeAnyRequestWithoutAnAppToken(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, kv{"im.slack.appToken": ""})
	if err := runUntilError(t, a); !errors.Is(err, errNoAppToken) {
		t.Fatalf("err = %v, want errNoAppToken", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := slacktest.New(t)
	d := deps(kv{"im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api"})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "受控的拨号器") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

// Slack 下发的长连接地址被出站校验拒绝(解析到内网)是永久性的;校验拿到的是 http(s) 形式。
func TestRun_AnEndpointRefusedByTheOutboundCheckStopsTheLoop(t *testing.T) {
	for _, c := range []struct{ endpoint, checked string }{
		{"ws://10.0.0.5:9000/link", "http://10.0.0.5:9000"},
		{"wss://10.0.0.5/link", "https://10.0.0.5"},
	} {
		srv := slacktest.New(t)
		a := newTestApp(t, srv, nil)
		srv.SetEndpoint(c.endpoint)
		var mu sync.Mutex
		var checked []string
		a.validate = func(u string) error {
			mu.Lock()
			checked = append(checked, u)
			mu.Unlock()
			if strings.Contains(u, "10.0.0.5") {
				return errors.New("解析到内网地址")
			}
			return nil
		}
		err := runUntilError(t, a)
		if !errors.Is(err, errBadBase) || !strings.Contains(err.Error(), "内网") {
			t.Fatalf("%s: err = %v", c.endpoint, err)
		}
		if srv.ConnectCount() != 0 {
			t.Fatalf("%s: a refused endpoint must not be dialled", c.endpoint)
		}
		mu.Lock()
		found := false
		for _, u := range checked {
			found = found || u == c.checked
		}
		mu.Unlock()
		if !found {
			t.Fatalf("%s: validated %q, want %s", c.endpoint, checked, c.checked)
		}
	}
}

func TestRun_AnOpenResponseWithoutAURLStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("apps.connections.open", slacktest.Failure{Raw: `{"ok":true}`})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "没有返回长连接地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_AnUnusableEndpointStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetEndpoint("https://example.com/link")
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "无法使用") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_WebhookModeNeedsTheSigningSecretAndOpensNoSocket(t *testing.T) {
	srv := slacktest.New(t)
	if err := runUntilError(t, newTestApp(t, srv, kv{"im.slack.mode": "webhook"})); !errors.Is(err, errNoSigningSecret) {
		t.Fatalf("err = %v, want errNoSigningSecret", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
	// webhook 模式不需要 App Token,也不建长连接;Run 仍验 Bot Token、拿机器人名,然后待命。
	a := newTestApp(t, srv, kv{"im.slack.mode": "webhook", "im.slack.signingSecret": srv.SigningSecret, "im.slack.appToken": ""})
	startRun(t, a, &recSink{})
	deadline := time.Now().Add(5 * time.Second)
	for a.BotName() != srv.BotName && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
	settle()
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("webhook mode opened %d socket mode connections", n)
	}
}
