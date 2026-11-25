package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"aegisdb/internal/chat"
)

const (
	testApp    = "cli_test"
	testSecret = "APPSECRET-do-not-leak"
	testTenant = "t-TENANTTOKEN-do-not-leak"
	testTicket = "WSTICKET-do-not-leak"
	testCard   = "CARDTOKEN-do-not-leak"
)

// ---------------------------------------------------------------- 假的飞书

type apiReq struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

type fakeLark struct {
	*httptest.Server
	t *testing.T

	mu        sync.Mutex
	reqs      []apiReq
	tokens    int
	ping      int                                               // 下发的心跳间隔,秒
	im        func(w http.ResponseWriter, r apiReq)             // 消息接口
	auth      func(w http.ResponseWriter)                       // 换令牌
	bootstrap func(w http.ResponseWriter)                       // 长连接引导
	upgrade   func(w http.ResponseWriter, r *http.Request) bool // 真 = 已处理,不升级
	conns     chan *websocket.Conn
}

func newFake(t *testing.T) *fakeLark {
	f := &fakeLark{t: t, ping: 120, conns: make(chan *websocket.Conn, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["app_id"] != testApp || body["app_secret"] != testSecret {
			t.Errorf("换令牌的请求 = %v", body)
		}
		f.mu.Lock()
		f.tokens++
		n, auth := f.tokens, f.auth
		f.mu.Unlock()
		if auth != nil {
			auth(w)
			return
		}
		fmt.Fprintf(w, `{"code":0,"msg":"ok","tenant_access_token":"%s-%d","expire":7200}`, testTenant, n)
	})
	mux.HandleFunc("/open-apis/im/v1/messages", f.serveIM)
	mux.HandleFunc("/open-apis/im/v1/messages/", f.serveIM)
	mux.HandleFunc("/callback/ws/endpoint", func(w http.ResponseWriter, r *http.Request) {
		if f.bootstrap != nil {
			f.bootstrap(w)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"URL":"%s/ws?device_id=d1&service_id=7&access_key=ak&ticket=%s","ClientConfig":{"PingInterval":%d}}}`,
			strings.Replace(f.URL, "http", "ws", 1), testTicket, f.ping)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if f.upgrade != nil && f.upgrade(w, r) {
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.conns <- c
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeLark) serveIM(w http.ResponseWriter, r *http.Request) {
	req := apiReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
	_ = json.NewDecoder(r.Body).Decode(&req.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	im := f.im
	f.mu.Unlock()
	if im != nil {
		im(w, req)
		return
	}
	_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{"message_id":"om_1","chat_id":"oc_1"}}`)
}

func (f *fakeLark) calls() []apiReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiReq(nil), f.reqs...)
}

func (f *fakeLark) transport(opts ...Option) *Transport {
	return New("feishu", testApp, testSecret, append([]Option{WithBaseURL(f.URL)}, opts...)...)
}

func code(n int, msg string) func(http.ResponseWriter, apiReq) {
	return func(w http.ResponseWriter, _ apiReq) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":%d,"msg":%q}`, n, msg)
	}
}

// captureLog 把默认日志接到一个缓冲里。
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuf {
	buf := &lockedBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func noSecrets(t *testing.T, where, s string) {
	t.Helper()
	for _, bad := range []string{testSecret, testTenant, testTicket, testCard, "Bearer", "do-not-leak"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s 里有 %q:%s", where, bad, s)
		}
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("等不到:" + what)
}

// ---------------------------------------------------------------- 发与改

func TestSend(t *testing.T) {
	f := newFake(t)
	tr := f.transport()
	if tr.BotID() != testApp {
		t.Errorf("BotID = %q", tr.BotID())
	}
	ref, err := tr.Send(context.Background(), "ou_1", pendingCard())
	if err != nil || ref != (chat.Ref{ChatID: "oc_1", MessageID: "om_1"}) {
		t.Fatalf("Send = %v, %v", ref, err)
	}
	if _, err := tr.Send(context.Background(), "ou_2", pendingCard()); err != nil {
		t.Fatal(err)
	}
	got := f.calls()
	r := got[0]
	if r.Method != http.MethodPost || r.Path != "/open-apis/im/v1/messages" || r.Query != "receive_id_type=open_id" ||
		r.Auth != "Bearer "+testTenant+"-1" {
		t.Errorf("请求 = %+v", r)
	}
	if r.Body["receive_id"] != "ou_1" || r.Body["msg_type"] != "interactive" || r.Body["content"] != RenderCard(pendingCard()) {
		t.Errorf("请求体 = %v", r.Body)
	}
	if f.tokens != 1 {
		t.Errorf("令牌应缓存,换了 %d 次", f.tokens)
	}
}

func TestUpdate(t *testing.T) {
	f := newFake(t)
	c := pendingCard()
	c.Outcome = "已撤回"
	if err := f.transport().Update(context.Background(), chat.Ref{ChatID: "oc_1", MessageID: "om_1"}, c); err != nil {
		t.Fatal(err)
	}
	r := f.calls()[0]
	if r.Method != http.MethodPatch || r.Path != "/open-apis/im/v1/messages/om_1" || r.Body["content"] != RenderCard(c) || len(r.Body) != 1 {
		t.Errorf("请求 = %+v", r)
	}
}

func TestUpdate_Gone(t *testing.T) {
	for _, tc := range []struct {
		code int
		gone bool
	}{{230031, true}, {230011, true}, {230110, true}, {230099, false}, {230020, false}, {99991400, false}} {
		f := newFake(t)
		f.im = code(tc.code, "nope")
		err := f.transport().Update(context.Background(), chat.Ref{MessageID: "om_1"}, pendingCard())
		if err == nil || errors.Is(err, chat.ErrGone) != tc.gone || !strings.Contains(err.Error(), fmt.Sprint(tc.code)) {
			t.Errorf("%d → %v", tc.code, err)
		}
	}
	// 不像消息号的东西不拼进地址。
	f := newFake(t)
	for _, id := range []string{"", "..", "om_1/../../x", "om?x=1", "om 1"} {
		if err := f.transport().Update(context.Background(), chat.Ref{MessageID: id}, pendingCard()); !errors.Is(err, chat.ErrGone) {
			t.Errorf("%q → %v", id, err)
		}
	}
	if len(f.calls()) != 0 {
		t.Error("不该发请求")
	}
	// 发送失败不是「消息没了」。
	f.im = code(230011, "x")
	if _, err := f.transport().Send(context.Background(), "ou", pendingCard()); err == nil || errors.Is(err, chat.ErrGone) {
		t.Errorf("Send → %v", err)
	}
}

func TestTokenRefresh(t *testing.T) {
	f := newFake(t)
	f.im = func(w http.ResponseWriter, r apiReq) {
		if strings.HasSuffix(r.Auth, "-1") {
			code(99991663, "Invalid access token")(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"om_1","chat_id":"oc_1"}}`)
	}
	if _, err := f.transport().Send(context.Background(), "ou", pendingCard()); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 || len(f.calls()) != 2 {
		t.Errorf("换了 %d 次令牌,发了 %d 次", f.tokens, len(f.calls()))
	}
	// 换了还是不行就算了,不来回打。
	f.im = code(99991663, "Invalid access token")
	if _, err := f.transport().Send(context.Background(), "ou", pendingCard()); err == nil {
		t.Error("应报错")
	}
	if n := len(f.calls()); n != 4 {
		t.Errorf("一共 %d 次", n)
	}
}

// 每一条失败的路,错误文本里都不能有密钥、令牌、Authorization;平台把它们原样念回来也不行。
func TestErrorsCarryNoSecrets(t *testing.T) {
	logs := captureLog(t)
	echo := testSecret + " Authorization: Bearer " + testTenant + "-1 " + testCard + " " + strings.Repeat("x", 190)
	dead := httptest.NewServer(nil)
	dead.Close()
	for name, setup := range map[string]func(f *fakeLark) []Option{
		"消息接口报错": func(f *fakeLark) []Option { f.im = code(230001, echo); return nil },
		"消息没了":   func(f *fakeLark) []Option { f.im = code(230031, echo); return nil },
		"不是 JSON": func(f *fakeLark) []Option {
			f.im = func(w http.ResponseWriter, _ apiReq) { _, _ = io.WriteString(w, "<html>"+echo) }
			return nil
		},
		"502": func(f *fakeLark) []Option {
			f.im = func(w http.ResponseWriter, _ apiReq) { w.WriteHeader(502); _, _ = io.WriteString(w, echo) }
			return nil
		},
		"没有消息号": func(f *fakeLark) []Option {
			f.im = func(w http.ResponseWriter, _ apiReq) { _, _ = io.WriteString(w, `{"code":0,"msg":"`+testTenant+`"}`) }
			return nil
		},
		"断开": func(f *fakeLark) []Option {
			f.im = func(http.ResponseWriter, apiReq) { panic(http.ErrAbortHandler) }
			return nil
		},
		"换令牌报错": func(f *fakeLark) []Option {
			f.auth = func(w http.ResponseWriter) { fmt.Fprintf(w, `{"code":10014,"msg":%q}`, echo) }
			return nil
		},
		"换令牌不是 JSON": func(f *fakeLark) []Option {
			f.auth = func(w http.ResponseWriter) { _, _ = io.WriteString(w, testSecret) }
			return nil
		},
		"换来空令牌": func(f *fakeLark) []Option {
			f.auth = func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"code":0}`) }
			return nil
		},
		"连不上":    func(*fakeLark) []Option { return []Option{WithBaseURL(dead.URL)} },
		"地址里有密钥": func(*fakeLark) []Option { return []Option{WithBaseURL("http://u:" + testSecret + "@127.0.0.1:1")} },
		"跳转": func(f *fakeLark) []Option {
			f.im = func(w http.ResponseWriter, r apiReq) {
				w.Header().Set("Location", "http://evil.invalid/?"+testTenant)
				w.WriteHeader(307)
				_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"om_x"}}`)
			}
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			tr := f.transport(setup(f)...)
			c := pendingCard()
			c.Token = testCard
			_, err := tr.Send(context.Background(), "ou", c)
			if err == nil && name != "没有消息号" {
				t.Error("Send 应报错")
			}
			err2 := tr.Update(context.Background(), chat.Ref{MessageID: "om_1"}, c)
			if err2 == nil && name != "没有消息号" {
				t.Error("Update 应报错")
			}
			for _, e := range []error{err, err2} {
				for ; e != nil; e = errors.Unwrap(e) {
					noSecrets(t, "错误", fmt.Sprintf("%v %+v", e, e))
				}
			}
		})
	}
	noSecrets(t, "日志", logs.String())
}

// 截短之前先去密钥:反过来,截断处会剩下半个密钥,再也认不出来。
func TestScrub(t *testing.T) {
	s := &scrubber{fixed: []string{testSecret, `we"ird+se/cret`}}
	for pad := maxErrRunes - len(testSecret); pad <= maxErrRunes; pad++ {
		if got := s.text(strings.Repeat("x", pad) + testSecret); strings.Contains(got, testSecret[:3]) {
			t.Fatalf("pad %d:%s", pad, got)
		}
	}
	for _, msg := range []string{
		`we"ird+se/cret`, `{"app_secret":"we\"ird+se/cret"}`, "x=we%22ird%2Bse%2Fcret", "/we%22ird+se%2Fcret",
		"dial wss://h.feishu.cn/ws?ticket=ird&access_key=ird failed", "Authorization: Bearer ird-unknown",
	} {
		if got := s.text(msg); strings.Contains(got, "ird") || strings.Contains(got, "cret") && !strings.Contains(got, "app_secret") {
			t.Errorf("%s → %s", msg, got)
		}
	}
	logs := captureLog(t)
	l := sdkLog{s}
	l.Debug(context.Background(), "debug-line")
	l.Info(context.Background(), "info-line")
	l.Warn(context.Background(), "warn-line ", testSecret)
	l.Error(context.Background(), "error-line ", errors.New(testSecret))
	noSecrets(t, "日志", logs.String())
	if out := logs.String(); strings.Contains(out, "debug-line") || strings.Contains(out, "info-line") ||
		strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "error-line") {
		t.Errorf("日志 = %s", out)
	}
}

// 还在用的租户令牌不能被长连接的票据挤出去:令牌有效近两小时,其间连不上会重试许多次。
func TestScrub_TokenOutlivesReconnects(t *testing.T) {
	f := newFake(t)
	f.upgrade = func(http.ResponseWriter, *http.Request) bool { panic(http.ErrAbortHandler) }
	n := 0
	f.bootstrap = func(w http.ResponseWriter) {
		n++
		fmt.Fprintf(w, `{"code":0,"data":{"URL":"%s/ws?device_id=device-%d&access_key=accesskey-%d&ticket=%s-%d"}}`,
			strings.Replace(f.URL, "http", "ws", 1), n, n, testTicket, n)
	}
	tr := f.transport()
	if _, err := tr.Send(context.Background(), "ou", pendingCard()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err := tr.Run(context.Background(), &fakeHandler{}, func(chat.State, string) {}); err == nil {
			t.Fatal("应报错")
		}
	}
	f.im = code(230001, "bad token "+testTenant+"-1")
	_, err := tr.Send(context.Background(), "ou", pendingCard())
	if err == nil {
		t.Fatal("应报错")
	}
	noSecrets(t, "错误", err.Error())
	if f.tokens != 1 {
		t.Fatalf("要测的是同一个令牌,换了 %d 次", f.tokens)
	}

	// 最近一次连接的票据也还认得;反过来,令牌换得再勤也有界,当前这个总在。
	noSecrets(t, "票据", tr.scrub.text(fmt.Sprintf("%s-%d", testTicket, n)))
	for i := 0; i < 100; i++ {
		tr.scrub.token(fmt.Sprint("tenant-token-", i))
		tr.scrub.token("tenant-token-keep")
	}
	if got := tr.scrub.text("x tenant-token-99 tenant-token-keep"); strings.Contains(got, "tenant-token") {
		t.Errorf("当前与上一个令牌 → %s", got)
	}
	for i := 0; i < 100; i++ {
		tr.scrub.ticket("same-ticket-value")
	}
	if len(tr.scrub.tokens) > maxTokens || len(tr.scrub.tickets) > maxTickets ||
		strings.Count(strings.Join(tr.scrub.tickets, " "), "same-ticket-value") != 1 {
		t.Errorf("应有界且不重复:%d %v", len(tr.scrub.tokens), tr.scrub.tickets)
	}
}

func TestSend_TimesOut(t *testing.T) {
	f := newFake(t)
	block := make(chan struct{})
	f.im = func(http.ResponseWriter, apiReq) { <-block }
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.transport().Send(ctx, "ou", pendingCard()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err) // 扫描靠它认出「整个渠道不通」
	}
}

// 出站只去这个地区的主机:跳转不跟,别的主机不连。
func TestOutboundGuard(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer evil.Close()
	f := newFake(t)
	f.im = func(w http.ResponseWriter, _ apiReq) { w.Header().Set("Location", evil.URL); w.WriteHeader(307) }
	tr := f.transport()
	if _, err := tr.Send(context.Background(), "ou", pendingCard()); err == nil {
		t.Error("跳转应是错误")
	}
	if _, err := tr.http.Get(evil.URL); err == nil {
		t.Error("别的主机应拒绝")
	}
	if hits.Load() != 0 {
		t.Error("连到了别的主机")
	}
	// 同一个主机上的跳转也不跟。
	f.im = func(w http.ResponseWriter, _ apiReq) {
		w.Header().Set("Location", "/open-apis/im/v1/messages/elsewhere")
		w.WriteHeader(307)
	}
	before := len(f.calls())
	if _, err := tr.Send(context.Background(), "ou", pendingCard()); err == nil {
		t.Error("跳转应是错误")
	}
	if n := len(f.calls()) - before; n != 1 {
		t.Errorf("跟了跳转:%d 次请求", n)
	}
	if tr.http.Timeout != 10*time.Second {
		t.Errorf("超时 = %v", tr.http.Timeout)
	}
	for region, want := range map[string]string{"feishu": "https://open.feishu.cn", "lark": "https://open.larksuite.com", "": "https://open.feishu.cn"} {
		tr := New(region, "a", "b")
		if tr.base != want {
			t.Errorf("%q → %s", region, tr.base)
		}
		// 不真的往外连:换掉放行之后的那一层。
		g, ok := tr.http.Transport.(hostGuard)
		if !ok || g.base != want {
			t.Fatalf("%q 没有主机限制:%+v", region, tr.http.Transport)
		}
		var passed []string
		g.next = roundTrip(func(r *http.Request) (*http.Response, error) {
			passed = append(passed, r.URL.String())
			return nil, errors.New("stub")
		})
		for _, u := range []string{want + "/x", strings.Replace(want, "https", "http", 1), "https://evil.example.com", want + ":8443", want + ".evil.com"} {
			_, _ = (&http.Client{Transport: g}).Get(u)
		}
		if len(passed) != 1 || passed[0] != want+"/x" {
			t.Errorf("%q 放行了 %v", region, passed)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckEndpoint(t *testing.T) {
	cn, intl := New("feishu", "a", "b"), New("lark", "a", "b")
	for _, tc := range []struct {
		tr   *Transport
		url  string
		good bool
	}{
		{cn, "wss://msg-frontier.feishu.cn/ws/v2?ticket=x", true},
		{intl, "wss://msg-frontier-sg.larksuite.com/ws/v2", true},
		{cn, "ws://msg-frontier.feishu.cn/ws/v2", false},
		{cn, "wss://msg-frontier.larksuite.com/", false},
		{intl, "wss://msg-frontier.feishu.cn/", false},
		{cn, "wss://evilfeishu.cn/", false},
		{cn, "wss://feishu.cn.evil.com/", false},
		{cn, "wss://u:p@x.feishu.cn/", false},
		{cn, "wss://127.0.0.1/", false},
		{cn, "://", false},
		{cn, "", false},
	} {
		if _, err := tc.tr.checkEndpoint(tc.url); (err == nil) != tc.good {
			t.Errorf("%s → %v", tc.url, err)
		}
	}
}

// ---------------------------------------------------------------- 长连接

type fakeHandler struct {
	mu     sync.Mutex
	texts  []string
	clicks []chat.Click
	text   func(text string) string
	click  func(ctx context.Context, c chat.Click) chat.ClickResult
}

func (h *fakeHandler) OnText(_ context.Context, ch chat.Channel, botID, accountID, chatID, text string) string {
	h.mu.Lock()
	h.texts = append(h.texts, strings.Join([]string{string(ch), botID, accountID, chatID, text}, "|"))
	h.mu.Unlock()
	if h.text != nil {
		return h.text(text)
	}
	return "reply to " + text
}

func (h *fakeHandler) OnClick(ctx context.Context, c chat.Click) chat.ClickResult {
	h.mu.Lock()
	h.clicks = append(h.clicks, c)
	h.mu.Unlock()
	if h.click != nil {
		return h.click(ctx, c)
	}
	return chat.ClickResult{}
}

func (h *fakeHandler) seen() ([]string, []chat.Click) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.texts...), append([]chat.Click(nil), h.clicks...)
}

type session struct {
	t      *testing.T
	conn   *websocket.Conn
	states func() []string
	done   chan error
	stop   context.CancelFunc
	n      int
}

// start 跑起 Run,连上之后把服务端那一头交出来。
func start(t *testing.T, f *fakeLark, tr *Transport, h chat.Handler) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var states []string
	s := &session{t: t, done: make(chan error, 1), stop: cancel, states: func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), states...)
	}}
	go func() {
		s.done <- tr.Run(ctx, h, func(st chat.State, why string) {
			mu.Lock()
			states = append(states, string(st))
			mu.Unlock()
		})
	}()
	select {
	case s.conn = <-f.conns:
	case err := <-s.done:
		t.Fatalf("没连上:%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("没连上")
	}
	t.Cleanup(func() { cancel(); s.conn.Close() })
	return s
}

// event 推一条事件,返回 SDK 写回来的应答(业务数据与状态码)。
func (s *session) event(payload any) (data []byte, status int) {
	s.t.Helper()
	s.n++
	raw, ok := payload.(string)
	if !ok {
		b, _ := json.Marshal(payload)
		raw = string(b)
	}
	id := fmt.Sprintf("frame-%d", s.n)
	fr := &larkws.Frame{Method: int32(larkws.FrameTypeData), Payload: []byte(raw), Headers: []larkws.Header{
		{Key: "type", Value: "event"}, {Key: "message_id", Value: id}, {Key: "sum", Value: "1"}, {Key: "seq", Value: "0"}}}
	b, err := fr.Marshal()
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.conn.WriteMessage(websocket.BinaryMessage, b); err != nil {
		s.t.Fatal(err)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			s.t.Fatalf("等应答:%v", err)
		}
		var got larkws.Frame
		if err := got.Unmarshal(msg); err != nil {
			s.t.Fatal(err)
		}
		if got.Method != int32(larkws.FrameTypeData) || larkws.Headers(got.Headers).GetString("message_id") != id {
			continue // 心跳
		}
		var resp larkws.Response
		if err := json.Unmarshal(got.Payload, &resp); err != nil {
			s.t.Fatal(err)
		}
		return resp.Data, resp.StatusCode
	}
}

func textEvent(id, chatType, msgType, content string) obj {
	return obj{"schema": "2.0",
		"header": obj{"event_id": "e-" + id, "event_type": "im.message.receive_v1", "app_id": testApp, "tenant_key": "tk"},
		"event": obj{
			"sender":  obj{"sender_id": obj{"open_id": "ou_1"}, "sender_type": "user", "tenant_key": "tk"},
			"message": obj{"message_id": "om_" + id, "chat_id": "oc_1", "chat_type": chatType, "message_type": msgType, "content": content},
		}}
}

func clickEvent(value, form any) obj {
	return obj{"schema": "2.0",
		"header": obj{"event_id": "c1", "event_type": "card.action.trigger", "app_id": testApp, "tenant_key": "tk"},
		"event": obj{
			"operator": obj{"tenant_key": "tk", "open_id": "ou_9"}, "token": "c-updatetoken", "host": "im_message",
			"action":  obj{"tag": "button", "value": value, "form_value": form},
			"context": obj{"open_message_id": "om_1", "open_chat_id": "oc_1"},
		}}
}

func TestRun_States(t *testing.T) {
	f := newFake(t)
	tr := f.transport()
	s := start(t, f, tr, &fakeHandler{})
	eventually(t, "已连接", func() bool { return strings.Join(s.states(), ",") == "connecting,connected" })
	s.conn.Close() // 对端断开 → Run 返回错误,不自己重连
	select {
	case err := <-s.done:
		if err == nil {
			t.Error("断开应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("断开后 Run 没返回")
	}
	select {
	case <-f.conns:
		t.Error("不该自己重连")
	case <-time.After(50 * time.Millisecond):
	}
	// 同一个 Transport 可以再连。
	s = start(t, f, tr, &fakeHandler{})
	s.stop()
	select {
	case err := <-s.done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 没返回")
	}
}

func TestRun_StopsGoroutines(t *testing.T) {
	f := newFake(t)
	stack := func() string {
		b := make([]byte, 1<<20)
		return string(b[:runtime.Stack(b, true)])
	}
	for i := 0; i < 3; i++ {
		s := start(t, f, f.transport(), &fakeHandler{})
		s.event(textEvent(fmt.Sprint(i), "p2p", "text", `{"text":"hi"}`))
		s.stop()
		<-s.done
		s.conn.Close()
	}
	eventually(t, "SDK 的收发与处理协程都停了", func() bool {
		st := stack()
		// SDK 的缓存清理协程由 GC 的终结器停,不归 Run 管。
		return !strings.Contains(st, "receiveMessageLoop") && !strings.Contains(st, "pingLoop") &&
			!strings.Contains(st, "handleMessageTask") && !strings.Contains(st, "lark.(*Transport)")
	})
}

// SDK 的客户端每建一个就留下一个停不掉的协程;连不上时反复重连,不该越积越多。
func TestRun_RetriesDoNotPileUpGoroutines(t *testing.T) {
	f := newFake(t)
	f.bootstrap = func(w http.ResponseWriter) { w.WriteHeader(500) }
	count := func() int {
		b := make([]byte, 1<<22)
		return strings.Count(string(b[:runtime.Stack(b, true)]), "created by github.com/larksuite/oapi-sdk-go/v3/cache.newCron")
	}
	before, tr := count(), f.transport()
	for i := 0; i < 5; i++ {
		if err := tr.Run(context.Background(), &fakeHandler{}, func(chat.State, string) {}); err == nil {
			t.Fatal("应报错")
		}
	}
	if n := count() - before; n > 1 {
		t.Errorf("连不上 5 次,多了 %d 个协程", n)
	}
	// 恢复之后照常能连。
	f.bootstrap = nil
	start(t, f, tr, &fakeHandler{})
}

func TestRun_CancelWhileConnecting(t *testing.T) {
	f := newFake(t)
	block, entered := make(chan struct{}), make(chan struct{})
	f.upgrade = func(http.ResponseWriter, *http.Request) bool { close(entered); <-block; return true }
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.transport().Run(ctx, &fakeHandler{}, func(chat.State, string) {}) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 没返回")
	}
}

func TestRun_FailuresCarryNoSecrets(t *testing.T) {
	logs := captureLog(t)
	for name, setup := range map[string]func(f *fakeLark){
		"密钥不对": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) { fmt.Fprintf(w, `{"code":1000040345,"msg":"bad %s"}`, testSecret) }
		},
		"系统忙": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) { w.WriteHeader(500); fmt.Fprintf(w, `{"code":1,"msg":"%s"}`, testSecret) }
		},
		"不是 JSON": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) { _, _ = io.WriteString(w, testSecret) }
		},
		"跳转": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) {
				w.Header().Set("Location", "http://evil.invalid/?"+testSecret)
				w.WriteHeader(307)
			}
		},
		"指去别的主机": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) {
				fmt.Fprintf(w, `{"code":0,"data":{"URL":"wss://evil.invalid/ws?ticket=%s"}}`, testTicket)
			}
		},
		"地址是坏的": func(f *fakeLark) {
			f.bootstrap = func(w http.ResponseWriter) {
				fmt.Fprintf(w, `{"code":0,"data":{"URL":"ws://[::1?ticket=%s"}}`, testTicket)
			}
		},
		"握手被拒": func(f *fakeLark) {
			f.upgrade = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Handshake-Status", "403")
				w.Header().Set("Handshake-Msg", "no "+testTicket+" "+testSecret)
				w.WriteHeader(400)
				return true
			}
		},
		"握手断开": func(f *fakeLark) {
			f.upgrade = func(http.ResponseWriter, *http.Request) bool { panic(http.ErrAbortHandler) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			setup(f)
			err := f.transport().Run(context.Background(), &fakeHandler{}, func(_ chat.State, why string) { noSecrets(t, "状态", why) })
			if err == nil {
				t.Fatal("应报错")
			}
			for ; err != nil; err = errors.Unwrap(err) {
				noSecrets(t, "错误", fmt.Sprintf("%v %+v", err, err))
			}
		})
	}
	t.Run("断开的原因", func(t *testing.T) {
		f := newFake(t)
		s := start(t, f, f.transport(), &fakeHandler{})
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4000, "bye "+testSecret+" "+testTicket))
		err := <-s.done
		if err == nil || !strings.Contains(err.Error(), "bye") {
			t.Errorf("err = %v", err)
		}
		noSecrets(t, "错误", err.Error())
	})
	noSecrets(t, "日志", logs.String())
	if !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=INFO") || strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("SDK 的日志应在 Warn:%s", logs.String())
	}
}

func TestRun_ConnLimitIsConflict(t *testing.T) {
	f := newFake(t)
	f.upgrade = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Handshake-Status", "514")
		w.Header().Set("Handshake-Autherrcode", "1000040350")
		w.WriteHeader(400)
		return true
	}
	var got []chat.State
	if err := f.transport().Run(context.Background(), &fakeHandler{}, func(s chat.State, _ string) { got = append(got, s) }); err == nil {
		t.Fatal("应报错")
	}
	if len(got) != 2 || got[1] != chat.StateConflict {
		t.Errorf("状态 = %v", got)
	}
}

// 下发的心跳间隔是 0 会让 SDK 的心跳循环空转。
func TestRun_ClampsPingInterval(t *testing.T) {
	f := newFake(t)
	f.ping = 0
	s := start(t, f, f.transport(), &fakeHandler{})
	_ = s.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n := 0
	for {
		if _, _, err := s.conn.ReadMessage(); err != nil {
			break
		}
		n++
	}
	if n > 2 {
		t.Errorf("300ms 里收到 %d 次心跳", n)
	}
}

// ---------------------------------------------------------------- 文字

func TestText(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{}
	s := start(t, f, f.transport(), h)
	if _, st := s.event(textEvent("1", "p2p", "text", `{"text":"ABCD-1234"}`)); st != 200 {
		t.Errorf("应答 %d", st)
	}
	eventually(t, "回复", func() bool { return len(f.calls()) == 1 })
	texts, _ := h.seen()
	if len(texts) != 1 || texts[0] != "lark|cli_test|ou_1|oc_1|ABCD-1234" {
		t.Errorf("OnText = %v", texts)
	}
	r := f.calls()[0]
	if r.Method != http.MethodPost || r.Query != "receive_id_type=chat_id" || r.Body["receive_id"] != "oc_1" || r.Body["msg_type"] != "interactive" ||
		r.Body["content"] != renderReply("reply to ABCD-1234") {
		t.Errorf("回复 = %+v", r)
	}

	// 重推的同一条消息不算第二次(event_id 换了也不算)。
	again := textEvent("1", "p2p", "text", `{"text":"ABCD-1234"}`)
	s.event(again)
	again["header"].(obj)["event_id"] = "e-other"
	s.event(again)
	s.conn.Close()
	<-s.done
	if texts, _ := h.seen(); len(texts) != 1 || len(f.calls()) != 1 {
		t.Errorf("重推的算了第二次:%v", texts)
	}

	for name, e := range map[string]obj{
		"群聊":    textEvent("2", "group", "text", `{"text":"x"}`),
		"不明会话":  textEvent("3", "", "text", `{"text":"x"}`),
		"图片":    textEvent("4", "p2p", "image", `{"image_key":"k"}`),
		"富文本":   textEvent("4b", "p2p", "post", `{"text":"x"}`),
		"内容是坏的": textEvent("5", "p2p", "text", `nope`),
		"空文字":   textEvent("6", "p2p", "text", `{"text":"  "}`),
		"没有消息号": func() obj {
			e := textEvent("7", "p2p", "text", `{"text":"x"}`)
			delete(e["event"].(obj)["message"].(obj), "message_id")
			return e
		}(),
		"机器人": func() obj {
			e := textEvent("8", "p2p", "text", `{"text":"x"}`)
			e["event"].(obj)["sender"].(obj)["sender_type"] = "bot"
			return e
		}(),
		"没有发送者": func() obj {
			e := textEvent("9", "p2p", "text", `{"text":"x"}`)
			delete(e["event"].(obj), "sender")
			return e
		}(),
		"没有事件": func() obj { e := textEvent("10", "p2p", "text", `{"text":"x"}`); delete(e, "event"); return e }(),
		"别的应用": func() obj {
			e := textEvent("11", "p2p", "text", `{"text":"x"}`)
			e["header"].(obj)["app_id"] = "cli_other"
			return e
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			f, h := newFake(t), &fakeHandler{}
			s := start(t, f, f.transport(), h)
			if _, st := s.event(e); st != 200 {
				t.Errorf("不理会也要应答成功,不然会重推:%d", st)
			}
			s.stop()
			<-s.done
			if texts, _ := h.seen(); len(texts) != 0 || len(f.calls()) != 0 {
				t.Errorf("不该处理:%v", texts)
			}
		})
	}
}

func TestText_DedupeSurvivesReconnect(t *testing.T) {
	f, h := newFake(t), &fakeHandler{}
	tr := f.transport()
	for i := 0; i < 2; i++ {
		s := start(t, f, tr, h)
		s.event(textEvent("1", "p2p", "text", `{"text":"x"}`))
		s.event(textEvent("1", "p2p", "text", `{"text":"x"}`))
		s.stop()
		<-s.done
	}
	if texts, _ := h.seen(); len(texts) != 1 {
		t.Errorf("处理了 %d 次", len(texts))
	}
}

func TestText_AcksBeforeHandling(t *testing.T) {
	f := newFake(t)
	block := make(chan struct{})
	h := &fakeHandler{text: func(string) string { <-block; return "" }}
	s := start(t, f, f.transport(), h)
	began := time.Now()
	if _, st := s.event(textEvent("1", "p2p", "text", `{"text":"x"}`)); st != 200 || time.Since(began) > time.Second {
		t.Errorf("应先应答:%d %v", st, time.Since(began))
	}
	close(block)
	s.stop()
	<-s.done
	if len(f.calls()) != 0 {
		t.Error("空回复不该发")
	}
}

// 忙不过来就不收:应答失败,飞书会再推一次,那一次不能被当成重复。
func TestText_BusyIsRedelivered(t *testing.T) {
	f := newFake(t)
	block := make(chan struct{})
	h := &fakeHandler{text: func(string) string { <-block; return "" }}
	tr := f.transport()
	s := start(t, f, tr, h)
	for i := 0; i < cap(tr.texts); i++ {
		s.event(textEvent(fmt.Sprint("b", i), "p2p", "text", `{"text":"x"}`))
	}
	if _, st := s.event(textEvent("late", "p2p", "text", `{"text":"late"}`)); st == 200 {
		t.Error("忙时不该应答成功")
	}
	close(block)
	eventually(t, "空出来", func() bool { return len(tr.texts) == 0 })
	if _, st := s.event(textEvent("late", "p2p", "text", `{"text":"late"}`)); st != 200 {
		t.Errorf("重推 → %d", st)
	}
	eventually(t, "处理", func() bool { texts, _ := h.seen(); return len(texts) == cap(tr.texts)+1 })
}

func TestText_PanicAndReplyFailure(t *testing.T) {
	logs := captureLog(t)
	f := newFake(t)
	f.im = code(230001, "x "+testSecret+" "+testTenant+"-1")
	s := start(t, f, f.transport(), &fakeHandler{text: func(x string) string {
		if x == "boom" {
			panic("boom " + testSecret)
		}
		return "<at user_id=\"all\">x</at>"
	}})
	s.event(textEvent("1", "p2p", "text", `{"text":"boom"}`))
	s.event(textEvent("2", "p2p", "text", `{"text":"x"}`))
	eventually(t, "两次回复", func() bool { return len(f.calls()) == 2 })
	s.stop()
	<-s.done
	all := ""
	for _, r := range f.calls() {
		all += fmt.Sprint(r.Body["content"])
	}
	if !strings.Contains(all, chat.Tr("zh", "bind.failed")) {
		t.Errorf("panic 之后应请他再试:%s", all)
	}
	if !strings.Contains(all, `"tag":"plain_text"`) {
		t.Errorf("回复不该解析任何标记:%s", all)
	}
	noSecrets(t, "日志", logs.String())
	if !strings.Contains(logs.String(), "230001") {
		t.Errorf("回复失败要留一行:%s", logs.String())
	}
}

// ---------------------------------------------------------------- 点击

func TestClick(t *testing.T) {
	f := newFake(t)
	card := pendingCard()
	card.Statement, card.Outcome = "SELECT '<a> & b'", "已由 王芳 通过 · 11-25 19:40"
	h := &fakeHandler{click: func(context.Context, chat.Click) chat.ClickResult {
		return chat.ClickResult{OK: true, Toast: "已通过", Card: &card}
	}}
	s := start(t, f, f.transport(), h)
	data, st := s.event(clickEvent(obj{"t": "tok", "a": "confirm"}, nil))
	if st != 200 {
		t.Fatalf("应答 %d", st)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if toast := got["toast"].(map[string]any); toast["type"] != "success" || toast["content"] != "已通过" {
		t.Errorf("toast = %v", toast)
	}
	if c := got["card"].(map[string]any); c["type"] != "raw" || fmt.Sprint(c["data"]) != fmt.Sprint(decode(t, card)) {
		t.Errorf("card = %v", c)
	}
	_, clicks := h.seen()
	if want := (chat.Click{Channel: chat.Lark, BotID: testApp, AccountID: "ou_9", Token: "tok", Action: chat.ActConfirm}); len(clicks) != 1 || clicks[0] != want {
		t.Errorf("OnClick = %+v", clicks)
	}
}

func TestClick_Reason(t *testing.T) {
	f, h := newFake(t), &fakeHandler{}
	s := start(t, f, f.transport(), h)
	for _, tc := range []struct {
		action string
		form   any
		want   string
	}{
		{"reject", obj{"reason": " 不合规 "}, "不合规"},
		{"reject", obj{}, ""},
		{"reject", nil, ""},
		{"reject", obj{"reason": 5}, ""},
		{"reject", obj{"reason": strings.Repeat("长", 900)}, strings.Repeat("长", 500)},
		{"reject", obj{"other": "x"}, ""},
		{"confirm", obj{"reason": "x"}, ""},
	} {
		data, st := s.event(clickEvent(obj{"t": "tok", "a": tc.action}, tc.form))
		if st != 200 || string(data) != "{}" {
			t.Errorf("没有提示也没有卡片时应答 = %d %s", st, data)
		}
		_, clicks := h.seen()
		if got := clicks[len(clicks)-1]; got.Reason != tc.want || string(got.Action) != tc.action {
			t.Errorf("%v → %q", tc.form, got.Reason)
		}
	}
}

// 表单提交的回调里带不带 value 没有在真平台上验证过;不带时从按钮的名字里认,两种都要能拒绝。
func TestClick_NameFallback(t *testing.T) {
	f, h := newFake(t), &fakeHandler{}
	s := start(t, f, f.transport(), h)
	c := pendingCard()
	name, _ := button(decode(t, c), "拒绝")["name"].(string)
	click := func(value any, name string) (string, int) {
		e := clickEvent(value, obj{"reason": "不合规"})
		a := e["event"].(obj)["action"].(obj)
		a["name"] = name
		if value == nil {
			delete(a, "value")
		}
		data, _ := s.event(e)
		_, clicks := h.seen()
		return string(data), len(clicks)
	}
	want := chat.Click{Channel: chat.Lark, BotID: testApp, AccountID: "ou_9", Token: c.Token, Action: chat.ActReject, Reason: "不合规"}
	for i, value := range []any{obj{"t": c.Token, "a": "reject"}, nil, obj{}} {
		if _, n := click(value, name); n != i+1 {
			t.Fatalf("值 %v 没交给处理方", value)
		}
		if _, clicks := h.seen(); clicks[i] != want {
			t.Errorf("值 %v → %+v", value, clicks[i])
		}
	}
	for _, bad := range []string{"", "reject", "reject_", "confirm_" + c.Token, "approve_" + c.Token, "test_" + c.Token, "x" + name, "Reject_" + c.Token,
		name + " ", name + "\n", name + "/..", "reject_" + strings.Repeat("x", 129), "reject_汉字"} {
		if data, n := click(nil, bad); n != 3 || !strings.Contains(data, chat.Tr("zh", "click.invalid")) {
			t.Errorf("名字 %q → %s", bad, data)
		}
	}
	// 有值就只认值:值是坏的不拿名字去补。
	for _, value := range []any{obj{"t": c.Token}, obj{"a": "reject"}, obj{"x": 1}, obj{"t": 1, "a": "reject"}} {
		if _, n := click(value, name); n != 3 {
			t.Errorf("值 %v 不该靠名字补上", value)
		}
	}
}

func TestClick_Malformed(t *testing.T) {
	f, h := newFake(t), &fakeHandler{}
	s := start(t, f, f.transport(), h)
	good := obj{"t": "tok", "a": "confirm"}
	with := func(edit func(ev obj)) obj {
		e := clickEvent(good, nil)
		edit(e["event"].(obj))
		return e
	}
	for name, e := range map[string]obj{
		"没有值":    clickEvent(nil, nil),
		"令牌不是串":  clickEvent(obj{"t": 1, "a": "confirm"}, nil),
		"没有令牌":   clickEvent(obj{"a": "confirm"}, nil),
		"令牌过长":   clickEvent(obj{"t": strings.Repeat("x", 129), "a": "confirm"}, nil),
		"动作不是串":  clickEvent(obj{"t": "tok", "a": obj{}}, nil),
		"不认识的动作": clickEvent(obj{"t": "tok", "a": "execute"}, nil),
		"多带了东西":  clickEvent(obj{"t": "tok", "a": "confirm", "apNo": "AP-1"}, nil),
		"不在消息里":  with(func(ev obj) { ev["host"] = "im_top_notice" }),
		"没有宿主":   with(func(ev obj) { delete(ev, "host") }),
		"没有点击者":  with(func(ev obj) { delete(ev, "operator") }),
		"点击者是空的": with(func(ev obj) { ev["operator"] = obj{"open_id": ""} }),
		"没有动作":   with(func(ev obj) { delete(ev, "action") }),
		"没有消息":   with(func(ev obj) { ev["context"] = obj{"open_chat_id": "oc_1"} }),
		"没有会话":   with(func(ev obj) { ev["context"] = obj{"open_message_id": "om_1"} }),
		"没有上下文":  with(func(ev obj) { delete(ev, "context") }),
		"没有事件":   func() obj { e := clickEvent(good, nil); delete(e, "event"); return e }(),
		"别的应用":   func() obj { e := clickEvent(good, nil); e["header"].(obj)["app_id"] = "cli_other"; return e }(),
	} {
		data, st := s.event(e)
		if st != 200 || !strings.Contains(string(data), chat.Tr("zh", "click.invalid")) {
			t.Errorf("%s → %d %s", name, st, data)
		}
	}
	// 值不是对象:SDK 解不开,应答失败,也到不了处理方。
	if _, st := s.event(clickEvent("tok", nil)); st != 500 {
		t.Errorf("值是字符串 → %d", st)
	}
	if _, clicks := h.seen(); len(clicks) != 0 {
		t.Errorf("不该交给处理方:%+v", clicks)
	}
}

func TestClick_Slow(t *testing.T) {
	f := newFake(t)
	block, finished := make(chan struct{}), make(chan error, 1)
	h := &fakeHandler{click: func(ctx context.Context, _ chat.Click) chat.ClickResult {
		<-block
		finished <- ctx.Err()
		return chat.ClickResult{OK: true, Toast: "已通过"}
	}}
	tr := f.transport(WithLang(func() string { return "en" }))
	if tr.answerWithin != 2500*time.Millisecond {
		t.Errorf("答复时限 = %v", tr.answerWithin)
	}
	tr.answerWithin = 30 * time.Millisecond
	s := start(t, f, tr, h)
	data, st := s.event(clickEvent(obj{"t": "tok", "a": "confirm"}, nil))
	if st != 200 || !strings.Contains(string(data), chat.Tr("en", "click.processing")) || !strings.Contains(string(data), `"info"`) ||
		strings.Contains(string(data), `"card"`) {
		t.Errorf("%d %s", st, data)
	}
	// 连接断了,已经开始的决定也照样做完。
	s.conn.Close()
	time.Sleep(20 * time.Millisecond)
	close(block)
	if err := <-finished; err != nil {
		t.Errorf("断线不该取消正在处理的点击:%v", err)
	}
	if err := <-s.done; err == nil {
		t.Error("断线应返回错误")
	}
}

func TestClick_PanicAndBusy(t *testing.T) {
	logs := captureLog(t)
	f := newFake(t)
	tr := f.transport()
	tr.answerWithin = 20 * time.Millisecond
	block := make(chan struct{})
	h := &fakeHandler{click: func(_ context.Context, c chat.Click) chat.ClickResult {
		if c.Action == chat.ActConfirm {
			panic("boom " + testSecret)
		}
		<-block
		return chat.ClickResult{}
	}}
	s := start(t, f, tr, h)
	if data, st := s.event(clickEvent(obj{"t": "tok", "a": "confirm"}, nil)); st != 200 || !strings.Contains(string(data), chat.Tr("zh", "click.retry")) {
		t.Errorf("panic → %d %s", st, data)
	}
	for i := 0; i < cap(tr.clicks); i++ {
		s.event(clickEvent(obj{"t": "tok", "a": "reject"}, nil))
	}
	if data, _ := s.event(clickEvent(obj{"t": "tok", "a": "reject"}, nil)); !strings.Contains(string(data), chat.Tr("zh", "click.retry")) {
		t.Errorf("忙 → %s", data)
	}
	if _, clicks := h.seen(); len(clicks) != cap(tr.clicks)+1 {
		t.Errorf("忙时不该再交给处理方:%d", len(clicks))
	}
	close(block)
	noSecrets(t, "日志", logs.String())
	if !strings.Contains(logs.String(), "panic") {
		t.Error("panic 要留下调用栈")
	}
}

// Run 里 panic 了(由调用方恢复):SDK 客户端停在「已在运行」,不换掉就再也连不上。
func TestRun_PanicDoesNotPoisonClient(t *testing.T) {
	f := newFake(t)
	tr := f.transport()
	good := tr.http.Transport
	tr.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { panic("boom") })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic 应当交给调用方")
			}
		}()
		_ = tr.Run(context.Background(), &fakeHandler{}, func(chat.State, string) {})
	}()
	tr.http.Transport = good
	s := start(t, f, tr, &fakeHandler{})
	eventually(t, "连上", func() bool { return strings.Join(s.states(), ",") == "connecting,connected" })
}
