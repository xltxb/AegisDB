package lark

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

// recSink 记下交上来的点击与私聊,回一个能看出收到了什么的 toast / 回复。
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

func TestRun_ConnectsAndLearnsTheBotName(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
}

func TestRun_AClickReachesTheSinkAndTheToastComesBack(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	if toast := srv.Click(t, "ou_1", map[string]any{"ap": "AP-7", "act": "a"}); toast != "ok ou_1 AP-7 true" {
		t.Fatalf("toast = %q", toast)
	}
	if toast := srv.Click(t, "ou_1", map[string]any{"ap": "AP-7", "act": "r"}); toast != "ok ou_1 AP-7 false" {
		t.Fatalf("reject toast = %q", toast)
	}
}

func TestRun_AnUnrecognisedButtonValueIsRefused(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	for _, v := range []map[string]any{
		{"ap": "AP 7; drop", "act": "a"},
		{"ap": "AP-7", "act": "x"},
		{"ap": 7, "act": "a"},
		{},
	} {
		if toast := srv.Click(t, "ou_1", v); toast != "无法识别的操作" {
			t.Errorf("value %v → toast %q", v, toast)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("a forged value must never reach the sink, got %d actions", n)
	}
}

func TestRun_ADirectMessageIsAnsweredAndGroupTextIsIgnored(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)

	srv.SendDM(t, "ou_9", "group", "BIND-AAAA1111")
	if _, n := sink.counts(); n != 0 {
		t.Fatal("a code posted in a group must not be looked at")
	}
	srv.SendDM(t, "ou_9", "p2p", "  BIND-AAAA1111 ")
	if dms := sink.dmList(); len(dms) != 1 || dms[0].PlatformUserID != "ou_9" || dms[0].Text != "BIND-AAAA1111" {
		t.Fatalf("dms = %+v", dms)
	}
	srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_9" && c.Str("msg_type") == "text" && strings.Contains(c.Str("content"), "reply to BIND-AAAA1111")
	})
}

func TestRun_ARedeliveredMessageIsHandledOnce(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	msg := srv.Message("ou_9", "p2p", "BIND-AAAA1111", "om_same")
	srv.Push(t, msg)
	srv.Push(t, msg) // 飞书没及时收到确认,把同一条消息又推了一次
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("handled %d times; a redelivered bind code would be consumed twice", n)
	}
}

func TestRun_RejectedCredentialsStopTheLoop(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, kv{"im.lark.appSecret": "the-wrong-secret-value"})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "10014") || strings.Contains(err.Error(), "the-wrong-secret-value") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rejected credentials must end Run, not retry forever")
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := larktest.New(t)
	d := deps(kv{"im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	err := a.Run(context.Background(), &recSink{})
	if err == nil || !strings.Contains(err.Error(), "拨号") {
		t.Fatalf("err = %v, want a refusal to fall back to an unguarded dialer", err)
	}
	if srv.ConnectCount() != 0 || len(srv.Calls("token")) != 0 || len(srv.Calls("botInfo")) != 0 {
		t.Fatal("a missing guard is a config error: refuse before any request to Lark")
	}
}

// runOnce 跑 Run 并等它自己返回;5 秒不返回就是在无限重试。
func runOnce(t *testing.T, a *App) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept retrying; a wrong open-platform address must stop it with a reason")
		return nil
	}
}

func TestRun_AWrongOpenPlatformAddressStopsTheLoop(t *testing.T) {
	const html = "<!doctype html><html><body>开发者后台</body></html>"
	for _, tc := range []struct {
		name, method string
		fail         func(srv *larktest.Server) larktest.Failure
	}{
		{"token answered by an HTML page", "token", func(*larktest.Server) larktest.Failure { return larktest.Failure{Raw: html} }},
		{"bot info answered by an HTML page", "botInfo", func(*larktest.Server) larktest.Failure { return larktest.Failure{Raw: html} }},
		{"a redirect", "token", func(srv *larktest.Server) larktest.Failure {
			// 跟随的话会落到 botInfo 上,被记下来
			return larktest.Failure{HTTP: http.StatusFound, Location: srv.URL + "/open-apis/bot/v3/info"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := larktest.New(t)
			srv.SetFail(tc.method, tc.fail(srv))
			err := runOnce(t, newTestApp(t, srv, nil))
			if err == nil || !strings.Contains(err.Error(), "开放平台地址") {
				t.Fatalf("err = %v, want a pointer at the open-platform address", err)
			}
			if tc.method == "token" && len(srv.Calls("botInfo")) != 0 {
				t.Fatal("a redirect must not be followed")
			}
		})
	}
	t.Run("refused by the outbound guard", func(t *testing.T) {
		srv := larktest.New(t)
		a := newTestApp(t, srv, nil)
		a.validate = func(string) error { return errors.New("出站地址必须使用 https(ADR 0003)") }
		if err := runOnce(t, a); err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("err = %v", err)
		}
	})
}
