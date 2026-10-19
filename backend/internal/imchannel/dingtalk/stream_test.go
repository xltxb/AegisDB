package dingtalk

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/dingtalk/dttest"
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
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
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

const testTrack = "aegis-AP-2294-0123456789abcdef"

func TestRun_ConnectsWithItsSubscriptions(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	open := srv.Calls("open")[0]
	if open.Str("clientId") != srv.ClientID || open.Str("clientSecret") != srv.ClientSecret {
		t.Fatalf("open = %+v", open.Params)
	}
	subs := open.Str("subscriptions")
	for _, want := range []string{
		`"topic":"ping"`, `"topic":"disconnect"`,
		`"topic":"/v1.0/im/bot/messages/get"`, `"topic":"/v1.0/card/instances/callback"`,
	} {
		if !strings.Contains(subs, want) {
			t.Errorf("subscriptions %s missing %s", subs, want)
		}
	}
	if open.Str("localIp") != "" {
		t.Errorf("the gateway's LAN address must not be reported: %q", open.Str("localIp"))
	}
}

func TestRun_AClickReachesTheSinkAndItsTipComesBack(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if tip := srv.Click(t, "staff1", testTrack, "a"); tip != "ok staff1 AP-2294 true" {
		t.Fatalf("approve tip = %q", tip)
	}
	if tip := srv.Click(t, "staff1", testTrack, "r"); tip != "ok staff1 AP-2294 false" {
		t.Fatalf("reject tip = %q", tip)
	}
}

func TestRun_ClicksThatDoNotNameACardOrAnActionAreDropped(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	for _, c := range []struct {
		user, track, act string
		idType           int
	}{
		{"staff1", "AP-2294", "a", 1},                     // 不是网关造的 outTrackId
		{"staff1", "aegis-AP 1-0123456789abcdef", "a", 1}, // 单号里有空格
		{"staff1", testTrack, "x", 1},                     // 不认识的动作
		{"", testTrack, "a", 1},                           // 没有点击者
		{"union1", testTrack, "a", 2},                     // unionId:绑定表里存的是 userid
	} {
		r := srv.Push(t, "CALLBACK", payload.CardInstanceCallbackTopic, dttest.CardCallback(c.user, c.track, c.act, c.idType))
		if tip := dttest.Tip(r); r.Code != http.StatusOK || tip != "无法识别的操作" {
			t.Errorf("%+v → code %d tip %q", c, r.Code, tip)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d malformed clicks reached the sink", n)
	}
}

func TestRun_ADirectMessageIsPassedOnAndAnswered(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "staff1", "1", "  BIND-7K3QX9MP ")
	if dms := sink.dmList(); len(dms) != 1 || dms[0].PlatformUserID != "staff1" || dms[0].Text != "BIND-7K3QX9MP" {
		t.Fatalf("dms = %+v", dms)
	}
	// 回复走机器人单聊接口(带受控出站客户端),不用事件里带来的 sessionWebhook 地址。
	oto := srv.WaitCall(t, "oto", nil)
	if oto.Str("userIds") != `["staff1"]` || !strings.Contains(oto.Str("msgParam"), "reply to BIND-7K3QX9MP") {
		t.Fatalf("reply = %+v", oto.Params)
	}
}

func TestRun_GroupMessagesAndOtherSendersAreIgnored(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "staff1", "2", "BIND-7K3QX9MP") // 群聊里的码:就是泄露了,更不能认
	srv.SendDM(t, "", "1", "BIND-7K3QX9MP")       // 没有 senderStaffId:组织外的人
	srv.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, dttest.Message("staff1", "1", "picture", "", "msg-pic"))
	if _, n := sink.counts(); n != 0 {
		t.Fatalf("%d messages reached the sink", n)
	}
	if n := len(srv.Calls("oto")); n != 0 {
		t.Fatalf("%d replies were sent", n)
	}
}

func TestRun_ARedeliveredMessageIsHandledOnce(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	m := dttest.Message("staff1", "1", "text", "BIND-7K3QX9MP", "msg-dup")
	for i := 0; i < 2; i++ {
		if r := srv.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, m); r.Code != http.StatusOK {
			t.Fatalf("redelivery must still be acknowledged: %+v", r)
		}
	}
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same msgId was handled %d times", n)
	}
}

func TestRun_AnswersThePlatformsPingWithItsData(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	r := srv.Push(t, "SYSTEM", "ping", `{"opaque":"p-1"}`)
	if r.Code != http.StatusOK || r.Data != `{"opaque":"p-1"}` {
		t.Fatalf("pong = %+v", r)
	}
}

func TestRun_AnUnsubscribedFrameIsAnswered404(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if r := srv.Push(t, "EVENT", "chat_update_title", `{}`); r.Code != http.StatusNotFound {
		t.Fatalf("event frame → %+v", r)
	}
	if r := srv.Push(t, "CALLBACK", "/v1.0/graph/api/invoke", `{}`); r.Code != http.StatusNotFound {
		t.Fatalf("unknown callback topic → %+v", r)
	}
}

func TestRun_ReconnectsWhenAskedAndWhenTheConnectionDrops(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	srv.Disconnect(t)
	srv.WaitConnects(t, 2)
	srv.DropConnection()
	srv.WaitConnects(t, 3)
	if n := len(srv.Calls("open")); n != 3 {
		t.Fatalf("each connection needs a fresh ticket; open called %d times", n)
	}
	if tip := srv.Click(t, "staff1", testTrack, "a"); tip != "ok staff1 AP-2294 true" {
		t.Fatalf("clicks must work after reconnecting, tip = %q", tip)
	}
}

func TestRun_StopsWhenCancelledAndStaysStopped(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
	n := srv.ConnectCount()
	time.Sleep(1500 * time.Millisecond)
	if srv.ConnectCount() != n || len(srv.Calls("open")) != n {
		t.Fatal("a cancelled Run must not reconnect: after Reload the old connection would keep taking events")
	}
}

func TestRun_RejectedCredentialsStopTheLoopWithoutTheSecret(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, kv{"im.dingtalk.clientSecret": "the-wrong-client-secret"})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "invalidClientIdOrSecret") || !strings.Contains(err.Error(), "<client-secret>") ||
		strings.Contains(err.Error(), "the-wrong-client-secret") {
		t.Fatalf("err = %v", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("no connection with rejected credentials")
	}
}

func TestRun_AMissingStreamPermissionStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("open", dttest.Failure{HTTP: http.StatusForbidden, Code: "Forbidden.AccessDenied", Msg: "应用未开通 Stream 模式"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "Stream 模式") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_AWrongOpenPlatformAddressStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("token", dttest.Failure{Raw: "<html>console</html>"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "检查开放平台地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_ATokenResponseWithoutATokenStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("token", dttest.Failure{Raw: "{}"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "检查开放平台地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_RefusesBeforeAnyRequestWhenTheCardTemplateIsMissing(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, kv{"im.dingtalk.cardTemplateId": ""})
	if err := runUntilError(t, a); !errors.Is(err, errNoTemplate) {
		t.Fatalf("err = %v, want errNoTemplate", err)
	}
	if n := len(srv.Calls("token")) + len(srv.Calls("open")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := dttest.New(t)
	d := deps(kv{
		"im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": testTemplate, "im.dingtalk.apiBase": srv.URL,
	})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "受控的拨号器") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("token")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

// 钉钉下发的长连接地址被出站校验拒绝(解析到内网)是永久性的:Run 停下交出原因,不无限重连;
// 校验拿到的是 http(s) 形式(ws → http、wss → https)。
func TestRun_AnEndpointRejectedByTheOutboundCheckStopsTheLoop(t *testing.T) {
	for _, c := range []struct{ endpoint, checked string }{
		{"ws://10.0.0.5:9000/connect", "http://10.0.0.5:9000"},
		{"wss://10.0.0.5/connect", "https://10.0.0.5"},
	} {
		srv := dttest.New(t)
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
			t.Fatalf("%s: a rejected endpoint must not be dialled", c.endpoint)
		}
		mu.Lock()
		got := checked
		mu.Unlock()
		found := false
		for _, u := range got {
			found = found || u == c.checked
		}
		if !found {
			t.Fatalf("%s: validated %q, want %s", c.endpoint, got, c.checked)
		}
	}
}

func TestRun_AnOpenResponseWithoutATicketStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("open", dttest.Failure{Raw: `{"endpoint":"ws://127.0.0.1:1/connect"}`})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "没有返回长连接地址") {
		t.Fatalf("err = %v", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("no connection without a ticket")
	}
}

func TestRun_AnUnusableEndpointStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetEndpoint("https://example.com/connect")
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "无法使用") {
		t.Fatalf("err = %v", err)
	}
}

// panicSink 处理点击时 panic:适配器仍要回执,不然钉钉会重推同一帧。
type panicSink struct{ recSink }

func (*panicSink) OnAction(context.Context, imchannel.Action) imchannel.ActionResult { panic("boom") }

func TestRun_APanickingHandlerStillAcknowledgesTheFrame(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &panicSink{})
	if tip := srv.Click(t, "staff1", testTrack, "a"); tip != "无法识别的操作" {
		t.Fatalf("tip after a panic = %q", tip)
	}
}
