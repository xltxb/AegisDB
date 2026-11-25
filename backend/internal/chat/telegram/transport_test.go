package telegram

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aegisdb/internal/chat"
)

const (
	testBot    = int64(987654321)
	testSecret = "AAH_TOKEN-do-not-leak-0123456789xyz"
	testToken  = "987654321:" + testSecret
	testUser   = int64(4242)
)

// ---------------------------------------------------------------- 假的 Telegram

type call struct {
	Method string
	Body   map[string]any
	At     time.Time
}

func (c call) num(key string) int64 { n, _ := c.Body[key].(float64); return int64(n) }
func (c call) str(key string) string {
	s, _ := c.Body[key].(string)
	return s
}

type fakeTG struct {
	*httptest.Server
	t *testing.T

	mu      sync.Mutex
	calls   []call
	hooks   map[string]func(w http.ResponseWriter, r *http.Request, c call) bool // 真 = 已答
	pending []map[string]any                                                     // 还没确认的 update
	wake    chan struct{}
	nextID  int64
}

func newFake(t *testing.T) *fakeTG {
	f := &fakeTG{t: t, hooks: map[string]func(http.ResponseWriter, *http.Request, call) bool{}, wake: make(chan struct{}), nextID: 100}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() { f.CloseClientConnections(); f.Close() })
	return f
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+testToken+"/")
	if !ok || r.Method != http.MethodPost {
		f.t.Errorf("请求 %s %s", r.Method, r.URL.Path)
		fail(w, 404, "Not Found", 0)
		return
	}
	c := call{Method: method, At: time.Now()}
	_ = json.NewDecoder(r.Body).Decode(&c.Body)
	f.mu.Lock()
	f.calls = append(f.calls, c)
	hook := f.hooks[method]
	f.mu.Unlock()
	if hook != nil && hook(w, r, c) {
		return
	}
	switch method {
	case "getMe":
		ok200(w, map[string]any{"id": testBot, "is_bot": true, "username": "aegis_bot"})
	case "getWebhookInfo":
		ok200(w, map[string]any{"url": ""})
	case "getUpdates":
		f.serveUpdates(w, r, c)
	case "sendMessage", "editMessageText":
		ok200(w, map[string]any{"message_id": 77, "chat": map[string]any{"id": c.num("chat_id"), "type": "private"}})
	case "answerCallbackQuery":
		ok200(w, true)
	default:
		f.t.Errorf("不该调用 %s", method)
		fail(w, 404, "Not Found", 0)
	}
}

// serveUpdates 照平台的规矩:offset 之前的都算确认了;没有新的就挂着等。
func (f *fakeTG) serveUpdates(w http.ResponseWriter, r *http.Request, c call) {
	timeout := time.After(time.Duration(c.num("timeout")) * time.Second)
	for {
		f.mu.Lock()
		var left, out []map[string]any
		for _, u := range f.pending {
			if u["update_id"].(int64) >= c.num("offset") {
				left = append(left, u)
			}
		}
		f.pending = left
		if n := int(c.num("limit")); len(left) > n {
			left = left[:n]
		}
		out = append(out, left...)
		wake := f.wake
		f.mu.Unlock()
		if len(out) > 0 {
			ok200(w, out)
			return
		}
		select {
		case <-wake:
		case <-timeout:
			ok200(w, []any{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// push 来了一条 update,返回它的号。
func (f *fakeTG) push(kind string, body any) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID
	f.nextID++
	f.pending = append(f.pending, map[string]any{"update_id": id, kind: body})
	close(f.wake)
	f.wake = make(chan struct{})
	return id
}

func (f *fakeTG) unconfirmed() (ids []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.pending {
		ids = append(ids, u["update_id"].(int64))
	}
	return ids
}

func (f *fakeTG) hook(method string, fn func(w http.ResponseWriter, r *http.Request, c call) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks[method] = fn
}

func (f *fakeTG) all(methods ...string) (out []call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		for _, m := range methods {
			if c.Method == m {
				out = append(out, c)
			}
		}
	}
	return out
}

func ok200(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func fail(w http.ResponseWriter, code int, desc string, retryAfter int) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func always(code int, desc string, retryAfter int) func(http.ResponseWriter, *http.Request, call) bool {
	return func(w http.ResponseWriter, _ *http.Request, _ call) bool {
		fail(w, code, desc, retryAfter)
		return true
	}
}

func user(id int64) map[string]any { return map[string]any{"id": id, "is_bot": false} }
func private(id int64) map[string]any {
	return map[string]any{"id": id, "type": "private"}
}

func textMsg(id int64, text string) map[string]any {
	return map[string]any{"message_id": 5, "from": user(id), "chat": private(id), "text": text}
}

func callback(id int64, data string) map[string]any {
	return map[string]any{"id": "cq1", "from": user(id), "data": data,
		"message": map[string]any{"message_id": 77, "chat": private(id), "from": map[string]any{"id": testBot, "is_bot": true}}}
}

// ---------------------------------------------------------------- 替身

type gotText struct{ account, chatID, text string }

type fakeHandler struct {
	mu     sync.Mutex
	texts  []gotText
	clicks []chat.Click
	text   func(gotText) string
	click  func(chat.Click) chat.ClickResult
}

func (h *fakeHandler) OnText(_ context.Context, ch chat.Channel, bot, account, chatID, text string) string {
	if ch != chat.Telegram || bot != fmt.Sprint(testBot) {
		panic("渠道或机器人不对")
	}
	g := gotText{account, chatID, text}
	h.mu.Lock()
	h.texts = append(h.texts, g)
	fn := h.text
	h.mu.Unlock()
	if fn == nil {
		return ""
	}
	return fn(g)
}

func (h *fakeHandler) OnClick(_ context.Context, c chat.Click) chat.ClickResult {
	h.mu.Lock()
	h.clicks = append(h.clicks, c)
	fn := h.click
	h.mu.Unlock()
	if fn == nil {
		return chat.ClickResult{OK: true, Toast: "ok"}
	}
	return fn(c)
}

func (h *fakeHandler) got() ([]gotText, []chat.Click) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]gotText(nil), h.texts...), append([]chat.Click(nil), h.clicks...)
}

type states struct {
	mu  sync.Mutex
	all []string
}

func (s *states) on(st chat.State, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = append(s.all, string(st)+"|"+detail)
}

func (s *states) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.all...)
}

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
	for _, bad := range []string{testToken, testSecret, "do-not-leak", "/bot", testCard, "api.telegram.org/"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s 里有 %q:%s", where, bad, s)
		}
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(3 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("等不到:" + what)
}

func (f *fakeTG) transport(opts ...Option) *Transport {
	f.t.Helper()
	tr, err := New(testToken, "", append([]Option{WithBaseURL(f.URL)}, opts...)...)
	if err != nil {
		f.t.Fatal(err)
	}
	tr.settle = 30 * time.Millisecond
	return tr
}

// run 跑起来,等它开始收。返回取消并取得结果的函数。
func (f *fakeTG) run(tr *Transport, h chat.Handler) (*states, func() error) {
	f.t.Helper()
	st := &states{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, h, st.on) }()
	stopped := false
	var err error
	stop := func() error {
		if !stopped {
			stopped = true
			cancel()
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				f.t.Error("Run 没有停下")
			}
		}
		return err
	}
	f.t.Cleanup(func() { _ = stop() })
	eventually(f.t, "连上", func() bool { return strings.Contains(strings.Join(st.list(), ","), "connected|") })
	return st, stop
}

// runOnce 跑到自己断开。
func (f *fakeTG) runOnce(tr *Transport) ([]string, error) {
	f.t.Helper()
	st := &states{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := tr.Run(ctx, &fakeHandler{}, st.on)
	if ctx.Err() != nil {
		f.t.Fatal("没有自己断开")
	}
	if err == nil {
		f.t.Fatal("断开要有原因")
	}
	noSecrets(f.t, "错误", err.Error())
	noSecrets(f.t, "状态", strings.Join(st.list(), "\n"))
	return st.list(), err
}

// ---------------------------------------------------------------- 构造

func TestNew_Rejects(t *testing.T) {
	for _, token := range []string{"", "x", "123", "abc:" + testSecret, "123456:short", testToken + "/../x", testToken + " ", "1234:" + strings.Repeat("a", 300)} {
		if _, err := New(token, ""); err == nil || token != "" && strings.Contains(err.Error(), token) {
			t.Errorf("令牌 %q → %v", token, err)
		}
	}
	for _, p := range []string{"proxy:3128", "ftp://proxy-do-not-leak:1", "socks4://proxy-do-not-leak:1", "socks5h://proxy-do-not-leak:1", "http://", "http://:80",
		"http://proxy-do-not-leak:99999", "http://proxy-do-not-leak/path", "http://proxy-do-not-leak?x=1", "http://proxy-do-not-leak/#x", "http://u:do-not-leak@h:bad", "http://a b/"} {
		if _, err := New(testToken, p); err == nil || strings.Contains(err.Error(), "do-not-leak") {
			t.Errorf("代理 %q → %v", p, err)
		}
	}
	for _, p := range []string{"", "http://10.0.0.5:3128", "https://u:p%40ss@proxy.corp", "socks5://127.0.0.1", "SOCKS5://[::1]:1080/"} {
		if _, err := New(testToken, p); err != nil {
			t.Errorf("代理 %q → %v", p, err)
		}
	}
	tr, _ := New(testToken, "")
	if tr.BotID() != "" || tr.BotName() != "" {
		t.Error("没问过 getMe 就不知道自己是谁")
	}
}

// ---------------------------------------------------------------- 启动

func TestRun_StartsUp(t *testing.T) {
	f := newFake(t)
	tr := f.transport()
	st, stop := f.run(tr, &fakeHandler{})
	if tr.BotID() != fmt.Sprint(testBot) || tr.BotName() != "aegis_bot" {
		t.Errorf("机器人 %q %q", tr.BotID(), tr.BotName())
	}
	eventually(t, "长轮询", func() bool { return len(f.all("getUpdates")) >= 2 })
	polls := f.all("getUpdates")
	for _, p := range polls {
		if got := fmt.Sprint(p.Body["allowed_updates"]); got != "[message callback_query]" {
			t.Errorf("allowed_updates = %s", got)
		}
	}
	// 第一次不等:马上知道通不通;之后才挂着
	if polls[0].num("timeout") != 0 || polls[1].num("timeout") < 30 {
		t.Errorf("timeout %d、%d", polls[0].num("timeout"), polls[1].num("timeout"))
	}
	if time.Duration(polls[1].num("timeout"))*time.Second >= pollWithin {
		t.Errorf("客户端的时限 %s 要比长轮询长", pollWithin)
	}
	if got := st.list(); got[0] != "connecting|" || got[1] != "connected|" {
		t.Errorf("状态 %v", got)
	}
	began := time.Now()
	if err := stop(); !errors.Is(err, context.Canceled) || time.Since(began) > 2*time.Second {
		t.Errorf("取消 → %v,用了 %s", err, time.Since(began))
	}
}

func TestRun_StartupFails(t *testing.T) {
	for name, c := range map[string]struct {
		hook func(http.ResponseWriter, *http.Request, call) bool
		is   error
		text string
	}{
		"令牌被撤销": {always(401, "Unauthorized", 0), chat.ErrCredentials, "401"},
		"令牌不存在": {always(404, "Not Found", 0), chat.ErrCredentials, "404"},
		"平台出错":  {always(500, "Internal Server Error: "+testToken, 0), nil, "500"},
		"不是 JSON": {func(w http.ResponseWriter, _ *http.Request, _ call) bool {
			_, _ = io.WriteString(w, "<html>"+testToken)
			return true
		}, nil, "unexpected"},
		"没有标识": {func(w http.ResponseWriter, _ *http.Request, _ call) bool {
			ok200(w, map[string]any{"id": 0})
			return true
		}, nil, "getMe"},
		"跳转": {func(w http.ResponseWriter, r *http.Request, _ call) bool {
			http.Redirect(w, r, "/elsewhere/"+testToken, http.StatusTemporaryRedirect)
			return true
		}, nil, "307"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.hook("getMe", c.hook)
			tr := f.transport()
			st, err := f.runOnce(tr)
			if c.is != nil != errors.Is(err, chat.ErrCredentials) || !strings.Contains(err.Error(), c.text) {
				t.Errorf("错误 %v", err)
			}
			if tr.BotID() != "" || len(f.all("getUpdates")) != 0 {
				t.Errorf("没认出机器人却继续了:%q", tr.BotID())
			}
			if len(st) != 1 {
				t.Errorf("状态 %v", st)
			}
		})
	}
}

// 机器人挂着 webhook:说明白,不替人删,也不去收。
func TestRun_Webhook(t *testing.T) {
	f := newFake(t)
	f.hook("getWebhookInfo", func(w http.ResponseWriter, _ *http.Request, _ call) bool {
		ok200(w, map[string]any{"url": "https://other.example.com/hook-do-not-leak"})
		return true
	})
	tr := f.transport()
	_, err := f.runOnce(tr)
	if !errors.Is(err, chat.ErrWebhook) {
		t.Errorf("错误 %v", err)
	}
	if tr.BotID() != "" {
		t.Error("收不到点击的机器人不算就绪:发出去的按钮会落到别人那里")
	}
	if n := f.all("getUpdates", "deleteWebhook", "setWebhook"); len(n) != 0 {
		t.Errorf("调用了 %v", n)
	}
	// 跑着的时候被人挂上
	f = newFake(t)
	f.hook("getUpdates", always(409, "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first", 0))
	tr = f.transport()
	if st, err := f.runOnce(tr); !errors.Is(err, chat.ErrWebhook) || strings.Contains(strings.Join(st, ","), "conflict") || tr.BotID() != "" {
		t.Errorf("%v %v %q", st, err, tr.BotID())
	}
}

func TestRun_Conflict(t *testing.T) {
	f := newFake(t)
	f.hook("getUpdates", always(409, "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", 0))
	st, err := f.runOnce(f.transport())
	if last := st[len(st)-1]; !strings.HasPrefix(last, "conflict|telegram: ") || !strings.Contains(last, "terminated by other") {
		t.Errorf("状态 %v,错误 %v", st, err)
	}
	if len(f.all("getUpdates")) != 1 {
		t.Error("不许抢")
	}
}

func TestRun_PollFails(t *testing.T) {
	for _, c := range []struct {
		code int
		is   error
	}{{401, chat.ErrCredentials}, {502, nil}} {
		f := newFake(t)
		f.hook("getUpdates", always(c.code, "boom", 0))
		tr := f.transport()
		st, err := f.runOnce(tr)
		if c.is != nil != errors.Is(err, chat.ErrCredentials) || len(st) != 1 {
			t.Errorf("%d → %v %v", c.code, st, err)
		}
		// 令牌不能用了就不算就绪:排着的卡片等换好令牌,而不是试满次数作废;只是断开则照旧
		if (tr.BotID() == "") != (c.is != nil) {
			t.Errorf("%d 之后 BotID = %q", c.code, tr.BotID())
		}
	}
}

// 嫌太频繁:等够了再来,不断开;状态如实说。
func TestRun_PollRetryAfter(t *testing.T) {
	f := newFake(t)
	var n atomic.Int32
	f.hook("getUpdates", func(w http.ResponseWriter, _ *http.Request, _ call) bool {
		if n.Add(1) == 1 {
			fail(w, 429, "Too Many Requests: retry after 1", 1)
			return true
		}
		return false
	})
	st, _ := f.run(f.transport(), &fakeHandler{})
	p := f.all("getUpdates")
	if gap := p[1].At.Sub(p[0].At); gap < 900*time.Millisecond || gap > 3*time.Second {
		t.Errorf("隔了 %s", gap)
	}
	if got := strings.Join(st.list(), ","); !strings.Contains(got, "error|telegram: getUpdates: ") {
		t.Errorf("状态 %s", got)
	}
	if got := pollWait(time.Hour); got != 5*time.Minute {
		t.Errorf("最多等 %s", got)
	}
}

// ---------------------------------------------------------------- offset

// 处理完才推进;处理得慢的挂着不推进,别人的照常处理,也不重复处理它。
func TestOffset_AdvancesOnlyWhenDone(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	h := &fakeHandler{text: func(g gotText) string {
		if g.text == "slow" {
			<-release
		}
		return ""
	}}
	tr := f.transport()
	_, stop := f.run(tr, h)
	first := f.push("message", textMsg(1, "quick"))
	eventually(t, "确认第一条", func() bool { return len(f.unconfirmed()) == 0 })
	slow := f.push("message", textMsg(2, "slow"))
	later := f.push("message", textMsg(3, "later"))
	eventually(t, "别人的照常处理", func() bool { texts, _ := h.got(); return len(texts) == 3 })
	last := f.push("message", textMsg(4, "last"))
	eventually(t, "后来的也处理", func() bool { texts, _ := h.got(); return len(texts) == 4 })
	time.Sleep(150 * time.Millisecond)
	if got := fmt.Sprint(f.unconfirmed()); got != fmt.Sprint([]int64{slow, later, last}) {
		t.Errorf("没处理完就推进了,还剩 %s", got)
	}
	if texts, _ := h.got(); len(texts) != 4 {
		t.Errorf("重复处理了:%v", texts)
	}
	if n := len(f.all("getUpdates")); n > 60 {
		t.Errorf("空转:%d 次", n)
	}
	close(release)
	eventually(t, "处理完就推进", func() bool { return len(f.unconfirmed()) == 0 })
	_ = stop()
	polls := f.all("getUpdates")
	seen := map[int64]bool{}
	for _, p := range polls {
		seen[p.num("offset")] = true
	}
	for _, want := range []int64{0, first + 1, slow, last + 1} {
		if !seen[want] {
			t.Errorf("offset %d 没用过:%v", want, seen)
		}
	}
}

// 一条处理得太久就不再等它:别的不能被它一直堵着,它自己接着做完。
func TestOffset_GivesUpHolding(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	defer close(release)
	h := &fakeHandler{text: func(gotText) string { <-release; return "" }}
	tr := f.transport()
	tr.holdFor = 150 * time.Millisecond
	began := time.Now()
	f.run(tr, h)
	f.push("message", textMsg(2, "stuck"))
	eventually(t, "放弃等它", func() bool { texts, _ := h.got(); return len(texts) == 1 && len(f.unconfirmed()) == 0 })
	if time.Since(began) < 150*time.Millisecond {
		t.Error("推进得太早")
	}
}

// 平台重放同一条:不再处理一遍 —— 点击不重复决定,输错的码不多算一次。重连之后也认得。
func TestOffset_Replay(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{text: func(gotText) string { return "no" }}
	tr := f.transport()
	_, stop := f.run(tr, h)
	f.push("message", textMsg(1, "AB2CD3EF4G"))
	f.push("callback_query", callback(1, "c:"+testCard))
	eventually(t, "处理", func() bool { a, b := h.got(); return len(a) == 1 && len(b) == 1 })
	eventually(t, "确认", func() bool { return len(f.unconfirmed()) == 0 })
	_ = stop()
	f.mu.Lock()
	f.nextID = 100
	f.mu.Unlock()
	_, _ = f.run(tr, h)
	f.push("message", textMsg(1, "AB2CD3EF4G"))
	f.push("callback_query", callback(1, "c:"+testCard))
	mark := f.push("message", textMsg(9, "new"))
	eventually(t, "新的照常", func() bool { a, _ := h.got(); return len(a) >= 2 })
	eventually(t, "确认", func() bool { return len(f.unconfirmed()) == 0 })
	if a, b := h.got(); len(a) != 2 || a[1].text != "new" || len(b) != 1 {
		t.Errorf("重放又处理了一遍:%v %v(%d)", a, b, mark)
	}
}

// 同一个人的按先后一件件来;不同的人互不等待。
func TestDispatch_PerAccountOrder(t *testing.T) {
	f := newFake(t)
	var mu sync.Mutex
	var order []string
	gate := make(chan struct{})
	h := &fakeHandler{text: func(g gotText) string {
		if g.text == "a0" {
			<-gate
		}
		mu.Lock()
		defer mu.Unlock()
		order = append(order, g.text)
		return ""
	}}
	f.run(f.transport(), h)
	for i := range 5 {
		f.push("message", textMsg(1, fmt.Sprintf("a%d", i)))
	}
	f.push("message", textMsg(2, "b"))
	eventually(t, "别人先完", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 1 })
	close(gate)
	eventually(t, "都完", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 6 })
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, " "); got != "b a0 a1 a2 a3 a4" {
		t.Errorf("次序 %s", got)
	}
}

// 一个人刷屏:排队有上限,多出来的丢掉,不占着别人的位子。
func TestDispatch_Flood(t *testing.T) {
	f := newFake(t)
	gate := make(chan struct{})
	h := &fakeHandler{text: func(g gotText) string {
		if g.account == "1" {
			<-gate
		}
		return ""
	}}
	tr := f.transport()
	f.run(tr, h)
	for range laneMax + 50 {
		f.push("message", textMsg(1, "spam"))
	}
	f.push("message", textMsg(2, "hello"))
	eventually(t, "别人的照常", func() bool {
		texts, _ := h.got()
		for _, g := range texts {
			if g.text == "hello" {
				return true
			}
		}
		return false
	})
	close(gate)
	eventually(t, "都确认", func() bool { return len(f.unconfirmed()) == 0 })
	time.Sleep(50 * time.Millisecond)
	if texts, _ := h.got(); len(texts) > laneMax+2 {
		t.Errorf("处理了 %d 条", len(texts))
	}
}

// 应答太大:少要几条再来,不能卡死在那里。
func TestPoll_TooLarge(t *testing.T) {
	f := newFake(t)
	f.hook("getUpdates", func(w http.ResponseWriter, _ *http.Request, c call) bool {
		if n := c.num("limit"); n > 10 && len(f.unconfirmed()) > 0 {
			_, _ = io.WriteString(w, `{"ok":true,"result":[`+strings.Repeat(" ", maxPollBody)+`]}`)
			return true
		}
		return false
	})
	h := &fakeHandler{}
	f.push("message", textMsg(1, "hi"))
	f.run(f.transport(), h)
	eventually(t, "少要几条", func() bool { texts, _ := h.got(); return len(texts) == 1 })
	f.hook("getUpdates", nil)
	eventually(t, "要回原来的条数", func() bool {
		f.push("message", textMsg(1, "hi"))
		p := f.all("getUpdates")
		return p[len(p)-1].num("limit") == 100
	})
}

// 有一条解不开的,不影响别的,也不会卡在它那里。
func TestPoll_BadUpdateIsSkipped(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{}
	f.push("message", map[string]any{"from": "not-an-object", "chat": 7, "text": []int{1}})
	f.push("message", textMsg(1, "hi"))
	f.run(f.transport(), h)
	eventually(t, "别的照常", func() bool { texts, _ := h.got(); return len(texts) == 1 })
	eventually(t, "确认", func() bool { return len(f.unconfirmed()) == 0 })
}

// 停下之后,还排着的不再处理:没推进 offset,重启之后平台会再给一遍。现在处理的话,回的话送不出去,
// 重来一遍时对方只看到「码无效」。
func TestRun_StopSkipsQueued(t *testing.T) {
	f := newFake(t)
	gate := make(chan struct{})
	h := &fakeHandler{text: func(gotText) string { <-gate; return "" }}
	tr := f.transport()
	_, stop := f.run(tr, h)
	f.push("message", textMsg(1, "first"))
	f.push("message", textMsg(1, "second"))
	eventually(t, "第一条在处理", func() bool { texts, _ := h.got(); return len(texts) == 1 })
	time.AfterFunc(100*time.Millisecond, func() { close(gate) })
	_ = stop()
	time.Sleep(50 * time.Millisecond)
	if texts, _ := h.got(); len(texts) != 1 {
		t.Errorf("停下之后还处理了:%v", texts)
	}
	if n := len(f.unconfirmed()); n != 2 {
		t.Errorf("还剩 %d 条没确认", n)
	}
}

// 陌生人刷屏、平台又不让回话:不拖着别人的点击,也不拖着 offset。
func TestReplies_FailingDontHoldOthers(t *testing.T) {
	f := newFake(t)
	f.hook("sendMessage", always(429, "Too Many Requests: retry after 2", 2))
	h := &fakeHandler{text: func(gotText) string { return "用法" }}
	f.run(f.transport(), h)
	for range 140 {
		f.push("message", textMsg(1, "hi"))
	}
	began := time.Now()
	f.push("callback_query", callback(2, "c:"+testCard))
	eventually(t, "别人的点击", func() bool { _, clicks := h.got(); return len(clicks) == 1 })
	eventually(t, "推进 offset", func() bool { return len(f.unconfirmed()) == 0 })
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("用了 %s", d)
	}
	// 平台不让发,就先不回了,别接着花发卡片要用的额度;每句只试一次
	if n := len(f.all("sendMessage")); n > replySlots {
		t.Errorf("试着回了 %d 次", n)
	}
}

// 回话同时只有几句在发,多出来的丢掉。
func TestReplies_Bounded(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	f.hook("sendMessage", func(http.ResponseWriter, *http.Request, call) bool { <-release; return false })
	defer close(release)
	h := &fakeHandler{text: func(gotText) string { return "x" }}
	f.run(f.transport(), h)
	for i := range int64(30) {
		f.push("message", textMsg(i+1, "hi"))
	}
	eventually(t, "都处理、都确认", func() bool { texts, _ := h.got(); return len(texts) == 30 && len(f.unconfirmed()) == 0 })
	eventually(t, "占满", func() bool { return len(f.all("sendMessage")) == replySlots })
	time.Sleep(30 * time.Millisecond)
	if n := len(f.all("sendMessage")); n != replySlots {
		t.Errorf("同时在发 %d 句", n)
	}
}

// 许多人的文字一起卡住:点击总有位子,也总看得到。
func TestDispatch_TextsLeaveRoomForClicks(t *testing.T) {
	f := newFake(t)
	gate := make(chan struct{})
	defer close(gate)
	h := &fakeHandler{text: func(gotText) string { <-gate; return "" }}
	tr := f.transport()
	f.run(tr, h)
	for i := range int64(400) {
		f.push("message", textMsg(i%10+1, "flood"))
	}
	for i := range int64(60) {
		f.push("callback_query", callback(1000+i, "c:"+testCard))
	}
	eventually(t, "全部点击", func() bool { _, clicks := h.got(); return len(clicks) == 60 })
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.texts > maxTexts {
		t.Errorf("文字占了 %d 个位子", tr.texts)
	}
}

// 同一个机器人换了连接(存一次设置就会):做完的不再做一遍 —— 输错的码不多算,刚绑上的不回「码无效」;还排着
// 没做的由新连接接着做。
func TestSeen_SharedAcrossTransports(t *testing.T) {
	f := newFake(t)
	seen := NewSeen()
	gate := make(chan struct{})
	h := &fakeHandler{text: func(g gotText) string {
		if g.text == "slow" {
			<-gate
		}
		return ""
	}}
	_, stop := f.run(f.transport(WithSeen(seen)), h)
	f.push("message", textMsg(1, "slow"))
	f.push("message", textMsg(1, "queued"))
	f.push("message", textMsg(2, "done"))
	eventually(t, "做到一半", func() bool { texts, _ := h.got(); return len(texts) == 2 })
	time.AfterFunc(50*time.Millisecond, func() { close(gate) })
	_ = stop()
	if n := len(f.unconfirmed()); n != 3 {
		t.Fatalf("还剩 %d 条没确认", n)
	}
	f.run(f.transport(WithSeen(seen)), h)
	eventually(t, "接着做", func() bool { return len(f.unconfirmed()) == 0 })
	texts, _ := h.got()
	count := map[string]int{}
	for _, g := range texts {
		count[g.text]++
	}
	if fmt.Sprint(count) != "map[done:1 queued:1 slow:1]" {
		t.Errorf("处理了 %v", count)
	}
}

// 一批里一个号都认不出来:推进不了,断开退避,不能空转。
func TestPoll_NoUsableID(t *testing.T) {
	f := newFake(t)
	f.hook("getUpdates", func(w http.ResponseWriter, _ *http.Request, _ call) bool {
		ok200(w, []any{map[string]any{"message": textMsg(1, "x")}, "junk"})
		return true
	})
	if _, err := f.runOnce(f.transport()); !strings.Contains(err.Error(), "unexpected") || len(f.all("getUpdates")) != 1 {
		t.Errorf("%v,收了 %d 次", err, len(f.all("getUpdates")))
	}
}

// 启动时就嫌太频繁:等够了再来,不断开。
func TestRun_StartupRetryAfter(t *testing.T) {
	for _, m := range []string{"getMe", "getWebhookInfo"} {
		f := newFake(t)
		var n atomic.Int32
		f.hook(m, func(w http.ResponseWriter, _ *http.Request, _ call) bool {
			if n.Add(1) == 1 {
				fail(w, 429, "Too Many Requests: retry after 4", 4)
				return true
			}
			return false
		})
		st, _ := f.run(f.transport(), &fakeHandler{})
		calls := f.all(m)
		if len(calls) != 2 || calls[1].At.Sub(calls[0].At) < 3900*time.Millisecond {
			t.Errorf("%s 试了 %d 次", m, len(calls))
		}
		if got := strings.Join(st.list(), ","); !strings.Contains(got, "error|telegram: "+m) {
			t.Errorf("状态 %s", got)
		}
	}
}

// ---------------------------------------------------------------- 文字

func TestText_BindAndReply(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{text: func(gotText) string { return "已绑定网关账号 <b>王&芳</b>。" }}
	f.run(f.transport(), h)
	f.push("message", textMsg(testUser, "/start AB2CD3EF4G"))
	eventually(t, "回复", func() bool { return len(f.all("sendMessage")) == 1 })
	if texts, _ := h.got(); texts[0] != (gotText{"4242", "4242", "/start AB2CD3EF4G"}) {
		t.Errorf("收到 %+v", texts)
	}
	r := f.all("sendMessage")[0]
	if r.num("chat_id") != testUser || r.str("text") != "已绑定网关账号 <b>王&芳</b>。" {
		t.Errorf("回复 %+v", r.Body)
	}
	// 不解析标记,整句放进 code:话里有别人起的名字,平台不会去认里面的链接与 /命令
	if _, ok := r.Body["parse_mode"]; ok || fmt.Sprint(r.Body["entities"]) != fmt.Sprintf("[map[length:%d offset:0 type:code]]", u16(r.str("text"))) {
		t.Errorf("回复 %+v", r.Body)
	}
	if fmt.Sprint(r.Body["link_preview_options"]) != "map[is_disabled:true]" {
		t.Errorf("链接预览 %v", r.Body["link_preview_options"])
	}
}

func TestIgnored(t *testing.T) {
	with := func(base map[string]any, k string, v any) map[string]any {
		out := map[string]any{}
		for a, b := range base {
			out[a] = b
		}
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	group := map[string]any{"id": -100, "type": "supergroup"}
	msg, cb := textMsg(1, "AB2CD3EF4G"), callback(1, "c:"+testCard)
	cbMsg := func(v any) map[string]any { return with(cb, "message", v) }
	for name, u := range map[string]struct {
		kind string
		body any
	}{
		"群消息":       {"message", with(msg, "chat", group)},
		"普通群":       {"message", with(msg, "chat", map[string]any{"id": -5, "type": "group"})},
		"私聊但会话不是本人": {"message", with(msg, "chat", private(2))},
		"会话类型不明":    {"message", with(msg, "chat", map[string]any{"id": 1})},
		"没有发送人":     {"message", with(msg, "from", nil)},
		"机器人发的":     {"message", with(msg, "from", map[string]any{"id": 1, "is_bot": true})},
		"以频道身份发的":   {"message", with(msg, "sender_chat", group)},
		"没有文字":      {"message", with(msg, "text", nil)},
		"全是空白":      {"message", with(msg, "text", " \n ")},
		"频道":        {"channel_post", with(msg, "chat", map[string]any{"id": -100, "type": "channel"})},
		"编辑":        {"edited_message", msg},
		"标识为 0":     {"message", textMsg(0, "AB2CD3EF4G")},
		"群里的点击":     {"callback_query", cbMsg(map[string]any{"message_id": 7, "chat": group})},
		"别人会话里的点击":  {"callback_query", cbMsg(map[string]any{"message_id": 7, "chat": private(2)})},
		"没有消息的点击":   {"callback_query", cbMsg(nil)},
		"内联消息的点击":   {"callback_query", with(cbMsg(nil), "inline_message_id", "x")},
		"机器人的点击":    {"callback_query", with(cb, "from", map[string]any{"id": 1, "is_bot": true})},
		"没有点击人":     {"callback_query", with(cb, "from", nil)},
		"没有标识的点击":   {"callback_query", with(cb, "id", nil)},
		"别的机器人的消息":  {"callback_query", cbMsg(map[string]any{"message_id": 7, "chat": private(1), "from": map[string]any{"id": 5, "is_bot": true}})},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			h := &fakeHandler{text: func(gotText) string { return "x" }}
			f.run(f.transport(), h)
			f.push(u.kind, u.body)
			f.push("message", textMsg(9, "mark"))
			eventually(t, "后面的处理完", func() bool { texts, _ := h.got(); return len(texts) >= 1 && len(f.unconfirmed()) == 0 })
			time.Sleep(20 * time.Millisecond)
			texts, clicks := h.got()
			if len(texts) != 1 || texts[0].text != "mark" || len(clicks) != 0 {
				t.Errorf("处理了:%v %v", texts, clicks)
			}
			if n := f.all("answerCallbackQuery", "editMessageText"); len(n) != 0 {
				t.Errorf("应答了:%v", n)
			}
		})
	}
}

// ---------------------------------------------------------------- 点击

// 先决定,再应答,再换卡片。
func TestClick(t *testing.T) {
	for data, act := range map[string]chat.Action{"a": chat.ActApprove, "c": chat.ActConfirm, "b": chat.ActBack, "r": chat.ActReject, "t": chat.ActTest} {
		t.Run(data, func(t *testing.T) {
			f := newFake(t)
			card := pendingCard()
			card.Outcome = "已通过"
			h := &fakeHandler{click: func(chat.Click) chat.ClickResult {
				if len(f.all("answerCallbackQuery", "editMessageText")) != 0 {
					t.Error("还没决定就应答了")
				}
				return chat.ClickResult{OK: true, Toast: "已通过", Card: &card}
			}}
			f.run(f.transport(), h)
			f.push("callback_query", callback(testUser, data+":"+testCard))
			eventually(t, "换卡片", func() bool { return len(f.all("editMessageText")) == 1 })
			_, clicks := h.got()
			if want := (chat.Click{Channel: chat.Telegram, BotID: fmt.Sprint(testBot), AccountID: "4242", Token: testCard, Action: act}); clicks[0] != want {
				t.Errorf("点击 %+v,应为 %+v", clicks[0], want)
			}
			got := f.all("answerCallbackQuery", "editMessageText")
			if got[0].Method != "answerCallbackQuery" || got[0].str("callback_query_id") != "cq1" || got[0].str("text") != "已通过" || got[0].Body["show_alert"] == true {
				t.Errorf("应答 %+v", got[0])
			}
			e := got[1]
			if e.num("chat_id") != testUser || e.num("message_id") != 77 || e.str("text") != render(card).text || e.str("parse_mode") != "HTML" ||
				fmt.Sprint(e.Body["reply_markup"]) != "map[inline_keyboard:[]]" || fmt.Sprint(e.Body["link_preview_options"]) != "map[is_disabled:true]" {
				t.Errorf("换卡片 %+v", e.Body)
			}
		})
	}
}

// 二次确认:点「通过」换成确认的样子,按钮带着同一个令牌。
func TestClick_TwoStep(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{click: func(c chat.Click) chat.ClickResult {
		card := pendingCard()
		card.Confirming = c.Action == chat.ActApprove
		return chat.ClickResult{OK: true, Card: &card}
	}}
	f.run(f.transport(), h)
	f.push("callback_query", callback(testUser, "a:"+testCard))
	eventually(t, "确认的样子", func() bool { return len(f.all("editMessageText")) == 1 })
	f.push("callback_query", callback(testUser, "b:"+testCard))
	eventually(t, "返回", func() bool { return len(f.all("editMessageText")) == 2 })
	markup := func(i int) string {
		b, _ := json.Marshal(f.all("editMessageText")[i].Body["reply_markup"])
		return string(b)
	}
	if m := markup(0); !strings.Contains(m, `"c:`+testCard) || !strings.Contains(m, `"b:`+testCard) || strings.Contains(m, `"a:`) {
		t.Errorf("确认的样子 %s", m)
	}
	if m := markup(1); !strings.Contains(m, `"a:`+testCard) || strings.Contains(m, `"c:`) {
		t.Errorf("返回 %s", m)
	}
	// 没有话要说也要应答,不然按钮上一直转圈
	if a := f.all("answerCallbackQuery"); len(a) != 2 || a[0].str("text") != "" {
		t.Errorf("应答 %+v", a)
	}
}

// 被拒的弹窗说明,不换卡片;提示最多 200 个字符。
func TestClick_Refused(t *testing.T) {
	f := newFake(t)
	h := &fakeHandler{click: func(chat.Click) chat.ClickResult { return chat.ClickResult{Toast: strings.Repeat("😀", 150)} }}
	f.run(f.transport(), h)
	f.push("callback_query", callback(testUser, "c:"+testCard))
	eventually(t, "应答", func() bool { return len(f.all("answerCallbackQuery")) == 1 })
	a := f.all("answerCallbackQuery")[0]
	if a.Body["show_alert"] != true || u16(a.str("text")) > 200 || !strings.HasSuffix(a.str("text"), "…") {
		t.Errorf("应答 %+v", a.Body)
	}
	time.Sleep(20 * time.Millisecond)
	if len(f.all("editMessageText")) != 0 {
		t.Error("没有卡片可换")
	}
}

func TestClick_BadData(t *testing.T) {
	for _, data := range []string{"", "c", "c:", "x:" + testCard, "c:" + testCard + ":1", "C:" + testCard, "confirm:" + testCard, "c:a b", "c:<x>", " c:" + testCard, "c:" + testCard + "\n"} {
		t.Run(data, func(t *testing.T) {
			f := newFake(t)
			h := &fakeHandler{}
			f.run(f.transport(WithLang(func() string { return "en" })), h)
			f.push("callback_query", callback(1, data))
			eventually(t, "应答", func() bool { return len(f.all("answerCallbackQuery")) == 1 })
			if a := f.all("answerCallbackQuery")[0]; a.str("text") != chat.Tr("en", "click.invalid") {
				t.Errorf("应答 %+v", a.Body)
			}
			if _, clicks := h.got(); len(clicks) != 0 {
				t.Errorf("交上去了:%+v", clicks)
			}
		})
	}
}

// 处理得慢:先答「正在处理」,做完再换卡片,不再应答第二次;别人的点击不受影响。
func TestClick_Slow(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	h := &fakeHandler{click: func(c chat.Click) chat.ClickResult {
		if c.AccountID == "1" {
			<-release
		}
		card := pendingCard()
		card.Outcome = "已通过"
		return chat.ClickResult{OK: true, Toast: "已通过", Card: &card}
	}}
	tr := f.transport(WithLang(func() string { return "en" }))
	tr.answerWithin = 50 * time.Millisecond
	f.run(tr, h)
	slow := f.push("callback_query", callback(1, "c:"+testCard))
	f.push("callback_query", callback(2, "c:"+testCard))
	eventually(t, "两个应答、一张卡片", func() bool {
		return len(f.all("answerCallbackQuery")) == 2 && len(f.all("editMessageText")) == 1
	})
	texts := map[string]bool{}
	for _, a := range f.all("answerCallbackQuery") {
		texts[a.str("text")] = true
		if a.Body["show_alert"] == true {
			t.Error("这两句都不用弹窗")
		}
	}
	if !texts[chat.Tr("en", "click.processing")] || !texts["已通过"] {
		t.Errorf("应答 %v", texts)
	}
	if got := f.unconfirmed(); len(got) == 0 || got[0] != slow {
		t.Errorf("没处理完就推进了:%v", got)
	}
	close(release)
	eventually(t, "做完换卡片", func() bool { return len(f.all("editMessageText")) == 2 && len(f.unconfirmed()) == 0 })
	if n := len(f.all("answerCallbackQuery")); n != 2 {
		t.Errorf("应答了 %d 次", n)
	}
	if answerWithin != 2500*time.Millisecond {
		t.Errorf("时限 %s", answerWithin)
	}
}

// 先答了「正在处理」、做完却是拒绝:原因已经弹不出来了,另发一句。换得了卡片的、没话说的不发。
func TestClick_SlowRefusal(t *testing.T) {
	card := pendingCard()
	for name, c := range map[string]struct {
		res  chat.ClickResult
		want int
	}{"拒绝": {chat.ClickResult{Toast: "你不在这张审批单的审批链上。"}, 1}, "换卡片": {chat.ClickResult{Toast: "已处理", Card: &card}, 0}, "没话说": {chat.ClickResult{OK: true}, 0}} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			release := make(chan struct{})
			h := &fakeHandler{click: func(chat.Click) chat.ClickResult { <-release; return c.res }}
			tr := f.transport()
			tr.answerWithin = 20 * time.Millisecond
			f.run(tr, h)
			f.push("callback_query", callback(testUser, "c:"+testCard))
			eventually(t, "正在处理", func() bool { return len(f.all("answerCallbackQuery")) == 1 })
			close(release)
			eventually(t, "做完", func() bool { return len(f.unconfirmed()) == 0 && len(f.all("sendMessage")) == c.want })
			time.Sleep(30 * time.Millisecond)
			s := f.all("sendMessage")
			if len(s) != c.want || c.want == 1 && (s[0].num("chat_id") != testUser || s[0].str("text") != c.res.Toast) {
				t.Errorf("另发了 %+v", s)
			}
		})
	}
}

// 换卡片有时限:过了就算了,不许再换(见 chat.CardWithin)。
func TestClick_EditIsBounded(t *testing.T) {
	f := newFake(t)
	var edits atomic.Int32
	f.hook("editMessageText", func(_ http.ResponseWriter, r *http.Request, _ call) bool {
		edits.Add(1)
		<-r.Context().Done()
		return true
	})
	h := &fakeHandler{click: func(chat.Click) chat.ClickResult {
		card := pendingCard()
		return chat.ClickResult{OK: true, Card: &card}
	}}
	tr := f.transport()
	if tr.cardWithin != chat.CardWithin {
		t.Errorf("时限 %s", tr.cardWithin)
	}
	tr.cardWithin = 100 * time.Millisecond
	logs := captureLog(t)
	f.run(tr, h)
	began := time.Now()
	f.push("callback_query", callback(1, "a:"+testCard))
	eventually(t, "放弃", func() bool { return edits.Load() == 1 && len(f.unconfirmed()) == 0 })
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("用了 %s", d)
	}
	eventually(t, "留一行", func() bool { return strings.Contains(logs.String(), "WARN") })
	noSecrets(t, "日志", logs.String())
}

// 上层 panic:停在这里,点的人得到「请再试」,后面的照常;panic 的内容不进日志。
func TestHandlerPanics(t *testing.T) {
	f := newFake(t)
	logs := captureLog(t)
	h := &fakeHandler{
		click: func(chat.Click) chat.ClickResult { panic(testToken) },
		text: func(g gotText) string {
			if g.text == "boom" {
				panic(testToken)
			}
			return "fine"
		},
	}
	f.run(f.transport(), h)
	f.push("callback_query", callback(1, "c:"+testCard))
	f.push("message", textMsg(1, "boom"))
	eventually(t, "第一句", func() bool { return len(f.all("sendMessage")) == 1 })
	f.push("message", textMsg(1, "ok"))
	eventually(t, "都处理完", func() bool { return len(f.all("sendMessage")) == 2 && len(f.unconfirmed()) == 0 })
	if a := f.all("answerCallbackQuery"); len(a) != 1 || a[0].str("text") != chat.Tr("zh", "click.retry") {
		t.Errorf("应答 %+v", a)
	}
	if s := f.all("sendMessage"); s[0].str("text") != chat.Tr("zh", "bind.failed") || s[1].str("text") != "fine" {
		t.Errorf("回复 %+v", s)
	}
	noSecrets(t, "日志", logs.String())
	if !strings.Contains(logs.String(), "panic") {
		t.Error("要留一行")
	}
}

// ---------------------------------------------------------------- 发与改

func TestSend(t *testing.T) {
	f := newFake(t)
	ref, err := f.transport().Send(context.Background(), "4242", pendingCard())
	if err != nil || ref != (chat.Ref{ChatID: "4242", MessageID: "77"}) {
		t.Fatalf("Send = %v, %v", ref, err)
	}
	b := f.all("sendMessage")[0].Body
	want := render(pendingCard())
	if b["chat_id"] != float64(4242) || b["text"] != want.text || b["parse_mode"] != "HTML" || b["protect_content"] != true ||
		fmt.Sprint(b["link_preview_options"]) != "map[is_disabled:true]" {
		t.Errorf("请求体 %v", b)
	}
	markup, _ := json.Marshal(b["reply_markup"])
	if !strings.Contains(string(markup), `"callback_data":"a:`+testCard+`"`) || !strings.Contains(string(markup), `"url":"https://db.example.com/#/approvals"`) {
		t.Errorf("按钮 %s", markup)
	}
	for _, account := range []string{"", "0", "-100", "@name", "1x", "+5", "99999999999999999999"} {
		if _, err := f.transport().Send(context.Background(), account, pendingCard()); err == nil {
			t.Errorf("账号 %q 也发了", account)
		}
	}
	if n := len(f.all("sendMessage")); n != 1 {
		t.Errorf("发了 %d 次", n)
	}
}

func TestUpdate(t *testing.T) {
	f := newFake(t)
	c := pendingCard()
	c.Outcome = "已撤回"
	tr := f.transport()
	if err := tr.Update(context.Background(), chat.Ref{ChatID: "4242", MessageID: "77"}, c); err != nil {
		t.Fatal(err)
	}
	b := f.all("editMessageText")[0].Body
	if b["chat_id"] != float64(4242) || b["message_id"] != float64(77) || b["text"] != render(c).text || fmt.Sprint(b["reply_markup"]) != "map[inline_keyboard:[]]" {
		t.Errorf("请求体 %v", b)
	}
	for _, r := range []chat.Ref{{}, {ChatID: "x", MessageID: "7"}, {ChatID: "7", MessageID: "om_1"}, {ChatID: "-7", MessageID: "1"}, {ChatID: "7", MessageID: "0"}} {
		if err := tr.Update(context.Background(), r, c); !errors.Is(err, chat.ErrGone) {
			t.Errorf("%+v → %v", r, err)
		}
	}
	if len(f.all("editMessageText")) != 1 {
		t.Error("不像消息号的不该发请求")
	}
}

func TestUpdate_Errors(t *testing.T) {
	for _, c := range []struct {
		code     int
		desc     string
		ok, gone bool
	}{
		{400, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same", true, false},
		{400, "Bad Request: message to edit not found", false, true},
		{400, "Bad Request: message can't be edited", false, true},
		{400, "Bad Request: chat not found", false, true},
		{403, "Forbidden: bot was blocked by the user", false, true},
		{403, "Forbidden: user is deactivated", false, true},
		{400, "Bad Request: can't parse entities", false, false},
		{500, "Internal Server Error", false, false},
	} {
		f := newFake(t)
		f.hook("editMessageText", always(c.code, c.desc, 0))
		err := f.transport().Update(context.Background(), chat.Ref{ChatID: "1", MessageID: "7"}, pendingCard())
		if (err == nil) != c.ok || errors.Is(err, chat.ErrGone) != c.gone {
			t.Errorf("%s → %v", c.desc, err)
		}
		if err != nil && (!strings.Contains(err.Error(), c.desc[10:]) || !strings.HasPrefix(err.Error(), "telegram: update: ")) {
			t.Errorf("原因 %v", err)
		}
		// 发送失败不是「消息没了」
		f.hook("sendMessage", always(c.code, c.desc, 0))
		if _, err := f.transport().Send(context.Background(), "1", pendingCard()); err == nil || errors.Is(err, chat.ErrGone) {
			t.Errorf("Send %s → %v", c.desc, err)
		}
	}
}

// 嫌太频繁:等得起就等一下再试一次,等不起就把要等多久交给上层。
func TestSend_RetryAfter(t *testing.T) {
	f := newFake(t)
	var n atomic.Int32
	f.hook("sendMessage", func(w http.ResponseWriter, _ *http.Request, _ call) bool {
		if n.Add(1) == 1 {
			fail(w, 429, "Too Many Requests: retry after 1", 1)
			return true
		}
		return false
	})
	if _, err := f.transport().Send(context.Background(), "1", pendingCard()); err != nil {
		t.Fatal(err)
	}
	if s := f.all("sendMessage"); len(s) != 2 || s[1].At.Sub(s[0].At) < 900*time.Millisecond {
		t.Errorf("试了 %d 次", len(s))
	}

	f = newFake(t)
	f.hook("sendMessage", always(429, "Too Many Requests: retry after 1", 1))
	if _, err := f.transport().Send(context.Background(), "1", pendingCard()); chat.RetryAfter(err) != time.Second || len(f.all("sendMessage")) != 2 {
		t.Errorf("只多试一次:%v", err)
	}

	f = newFake(t)
	f.hook("sendMessage", always(429, "Too Many Requests: retry after 300", 300))
	began := time.Now()
	_, err := f.transport().Send(context.Background(), "1", pendingCard())
	if chat.RetryAfter(err) != 5*time.Minute || time.Since(began) > time.Second || len(f.all("sendMessage")) != 1 {
		t.Errorf("%v,用了 %s", err, time.Since(began))
	}
	if err := f.transport().Update(context.Background(), chat.Ref{}, pendingCard()); chat.RetryAfter(err) != 0 {
		t.Errorf("没有要求等的:%v", err)
	}
	// 时限内等不完的不等
	f = newFake(t)
	f.hook("sendMessage", always(429, "Too Many Requests: retry after 2", 2))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := f.transport().Send(ctx, "1", pendingCard()); chat.RetryAfter(err) != 2*time.Second || len(f.all("sendMessage")) != 1 {
		t.Errorf("%v", err)
	}
}

// 「查看详情」的地址平台不认(内网主机名这类):去掉它再发,别因为一个按钮整张发不出去。
func TestSend_BadDetailURL(t *testing.T) {
	for _, desc := range []string{"Bad Request: inline keyboard button URL 'http://aegis/' is invalid: Wrong HTTP URL", "Bad Request: BUTTON_URL_INVALID"} {
		f := newFake(t)
		refuse := func(w http.ResponseWriter, _ *http.Request, c call) bool {
			if b, _ := json.Marshal(c.Body["reply_markup"]); strings.Contains(string(b), `"url"`) {
				fail(w, 400, desc, 0)
				return true
			}
			return false
		}
		f.hook("sendMessage", refuse)
		f.hook("editMessageText", refuse)
		if _, err := f.transport().Send(context.Background(), "1", pendingCard()); err != nil || len(f.all("sendMessage")) != 2 {
			t.Errorf("%s → %v", desc, err)
		}
		if err := f.transport().Update(context.Background(), chat.Ref{ChatID: "1", MessageID: "7"}, pendingCard()); err != nil {
			t.Errorf("%s → %v", desc, err)
		}
	}
}

// ---------------------------------------------------------------- 出站与机密

// 错误里的地址带着令牌;平台的话也可能把请求念回来。
func TestErrors_CarryNoSecrets(t *testing.T) {
	logs := captureLog(t)
	f := newFake(t)
	tr := f.transport()
	echo := "Bad Request: https://api.telegram.org/bot" + testToken + "/sendMessage " + url.QueryEscape(testToken) + " " + testSecret + " " + testCard + "\nsecond line " + strings.Repeat("长", 500)
	for _, m := range []string{"sendMessage", "editMessageText", "answerCallbackQuery"} {
		f.hook(m, always(400, echo, 0))
	}
	var all []string
	_, err := tr.Send(context.Background(), "1", pendingCard())
	all = append(all, err.Error(), fmt.Sprintf("%v %+v", err, err))
	err = tr.Update(context.Background(), chat.Ref{ChatID: "1", MessageID: "7"}, pendingCard())
	all = append(all, err.Error())
	if strings.Contains(all[0], "\n") || len([]rune(all[0])) > 300 {
		t.Errorf("要一行、截短:%s", all[0])
	}
	// 点击的应答与换卡片失败,只留在日志里;回话里没有卡片令牌,平台念回来的也就没有
	f.hook("sendMessage", always(400, strings.ReplaceAll(echo, testCard, ""), 0))
	card := pendingCard()
	h := &fakeHandler{click: func(chat.Click) chat.ClickResult { return chat.ClickResult{Toast: "x", Card: &card} }, text: func(gotText) string { return "r" }}
	_, stop := f.run(tr, h)
	f.push("callback_query", callback(1, "c:"+testCard))
	f.push("message", textMsg(1, "hi"))
	eventually(t, "处理完", func() bool { return len(f.unconfirmed()) == 0 })
	eventually(t, "三种失败各留一行", func() bool { return strings.Count(logs.String(), "level=WARN") >= 3 })
	_ = stop()
	// 连不上
	f.Close()
	_, err = tr.Send(context.Background(), "1", pendingCard())
	all = append(all, err.Error())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := &states{}
	all = append(all, tr.Run(ctx, h, st.on).Error(), strings.Join(st.list(), "\n"), logs.String())
	for _, s := range all {
		noSecrets(t, "文字", s)
	}
	if !strings.Contains(all[3], "refused") {
		t.Errorf("不带代理时看得出为什么连不上:%s", all[3])
	}
}

// 没配代理:只连 api.telegram.org:443,不看环境变量里的代理,别的地址一概不拨。
func TestOutbound_DirectOnly(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(k, "http://env-proxy.invalid:1")
	}
	tr, err := New(testToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.base != "https://api.telegram.org" || tr.dialTo != "api.telegram.org:443" {
		t.Errorf("%s %s", tr.base, tr.dialTo)
	}
	ht := tr.http.Transport.(hostGuard).next.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/x", nil)
	if p, err := ht.Proxy(req); p != nil || err != nil {
		t.Errorf("代理 %v %v", p, err)
	}
	for _, addr := range []string{"127.0.0.1:443", "api.telegram.org:80", "telegram.org:443", "169.254.169.254:80"} {
		if c, err := ht.DialContext(context.Background(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("拨通了 %s: %v %v", addr, c, err)
		}
	}
	for _, u := range []string{"https://example.com/", "http://api.telegram.org/", "https://api.telegram.org:8443/", "https://api.telegram.org.evil.com/"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if _, err := tr.http.Do(req); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("%s → %v", u, err)
		}
	}
	if tr.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("不许跟跳转")
	}
}

// 不跟跳转:跟过去会把令牌带去别处。
func TestOutbound_NoRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	f := newFake(t)
	f.hook("sendMessage", func(w http.ResponseWriter, r *http.Request, _ call) bool {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
		return true
	})
	_, err := f.transport().Send(context.Background(), "1", pendingCard())
	if err == nil || hits.Load() != 0 || len(f.all("sendMessage")) != 1 {
		t.Errorf("%v,别处收到 %d 次", err, hits.Load())
	}
	noSecrets(t, "错误", err.Error())
}

// tlsFake 是走 TLS 的假平台,只认得 getMe。
func tlsFake(t *testing.T) *httptest.Server {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ok200(w, map[string]any{"message_id": 77, "chat": map[string]any{"id": 1, "type": "private"}})
	}))
	t.Cleanup(s.Close)
	return s
}

func trust(tr *Transport, s *httptest.Server) {
	pool := s.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	tr.http.Transport.(hostGuard).next.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
}

// pipe 把两头接起来。
func pipe(a, b net.Conn) {
	go func() { _, _ = io.Copy(a, b); a.Close() }()
	_, _ = io.Copy(b, a)
	b.Close()
}

func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return l.Addr().String()
}

// 配了代理:http 走 CONNECT(带上口令),socks5 走它自己的握手;目标仍是平台。
func TestProxy(t *testing.T) {
	platform := tlsFake(t)
	target := strings.TrimPrefix(platform.URL, "https://")
	var mu sync.Mutex
	var asked []string
	note := func(s string) { mu.Lock(); defer mu.Unlock(); asked = append(asked, s) }
	httpProxy := listen(t, func(c net.Conn) {
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		note(req.Method + " " + req.Host + " " + req.Header.Get("Proxy-Authorization"))
		up, err := net.Dial("tcp", target)
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
		pipe(c, up)
	})
	socks := listen(t, func(c net.Conn) {
		buf := make([]byte, 262)
		_, _ = io.ReadFull(c, buf[:2])
		_, _ = io.ReadFull(c, buf[2:2+int(buf[1])])
		_, _ = c.Write([]byte{5, 0})
		_, _ = io.ReadFull(c, buf[:4])
		host := ""
		switch buf[3] {
		case 1:
			_, _ = io.ReadFull(c, buf[:4])
			host = net.IP(buf[:4]).String()
		case 3:
			_, _ = io.ReadFull(c, buf[:1])
			n := int(buf[0])
			_, _ = io.ReadFull(c, buf[:n])
			host = string(buf[:n])
		}
		_, _ = io.ReadFull(c, buf[:2])
		note("SOCKS " + net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(buf[:2])))))
		up, err := net.Dial("tcp", target)
		if err != nil {
			return
		}
		_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		pipe(c, up)
	})
	for proxyURL, want := range map[string]string{
		"http://user:p%40ss@" + httpProxy: "CONNECT " + target + " Basic " + base64.StdEncoding.EncodeToString([]byte("user:p@ss")),
		"socks5://" + socks:               "SOCKS " + target,
	} {
		asked = nil
		tr, err := New(testToken, proxyURL, WithBaseURL(platform.URL))
		if err != nil {
			t.Fatal(err)
		}
		trust(tr, platform)
		if _, err := tr.Send(context.Background(), "1", pendingCard()); err != nil {
			t.Errorf("%v", err)
		}
		mu.Lock()
		if len(asked) != 1 || asked[0] != want {
			t.Errorf("代理收到 %v,应为 %s", asked, want)
		}
		mu.Unlock()
		// 经代理也只许去平台,拨号只许拨代理
		ht := tr.http.Transport.(hostGuard).next.(*http.Transport)
		if _, err := ht.DialContext(context.Background(), "tcp", target); err == nil {
			t.Error("绕过代理直接拨通了平台")
		}
		req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		if _, err := tr.http.Do(req); err == nil {
			t.Error("经代理去了别处")
		}
	}
	tr, err := New(testToken, "http://"+httpProxy)
	if err != nil {
		t.Fatal(err)
	}
	if tr.base != "https://api.telegram.org" || tr.dialTo != httpProxy {
		t.Errorf("%s %s", tr.base, tr.dialTo)
	}
}

// 代理出错:说得出是代理的问题,说不出代理在哪、口令是什么。
func TestProxy_ErrorsHideIt(t *testing.T) {
	logs := captureLog(t)
	closed := listen(t, func(net.Conn) {})
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	status := func(line string) string {
		return listen(t, func(c net.Conn) {
			_, _ = http.ReadRequest(bufio.NewReader(c))
			_, _ = io.WriteString(c, "HTTP/1.1 "+line+"\r\nContent-Length: 0\r\n\r\n")
		})
	}
	for name, p := range map[string]string{
		"拒绝连接":     "http://pxuser:pxpass-do-not-leak@" + deadAddr,
		"socks 拒绝": "socks5://pxuser:pxpass-do-not-leak@" + deadAddr,
		"一连就断":     "http://pxuser:pxpass-do-not-leak@" + closed,
		"socks 断":  "socks5://pxuser:pxpass-do-not-leak@" + closed,
		"要口令":      "http://pxuser:pxpass-do-not-leak@" + status("407 Proxy Authentication Required pxpass-do-not-leak"),
		"代理 502":   "http://pxuser:pxpass-do-not-leak@" + status("502 cannot reach proxy-host-do-not-leak"),
		"解析不了":     "http://pxuser:pxpass-do-not-leak@proxy-host-do-not-leak.invalid:3128",
	} {
		t.Run(name, func(t *testing.T) {
			tr, err := New(testToken, p)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(p)
			_, err = tr.Send(context.Background(), "1", pendingCard())
			if err == nil {
				t.Fatal("应失败")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st := &states{}
			rerr := tr.Run(ctx, &fakeHandler{}, st.on)
			for _, s := range []string{err.Error(), rerr.Error(), strings.Join(st.list(), "\n")} {
				noSecrets(t, "文字", s)
				for _, bad := range []string{"pxuser", u.Host, u.Hostname(), u.Port(), "127.0.0.1"} {
					if strings.Contains(s, bad) {
						t.Errorf("有 %q:%s", bad, s)
					}
				}
				if !strings.Contains(s, "proxy") && !strings.HasPrefix(s, "connecting|") {
					t.Errorf("看不出是代理的问题:%s", s)
				}
			}
			if name == "要口令" && !strings.Contains(err.Error(), "407") {
				t.Errorf("看不出要口令:%v", err)
			}
		})
	}
	noSecrets(t, "日志", logs.String())
}

// 平台的话里万一带上代理的地址或口令,也遮掉。
func TestScrub_Proxy(t *testing.T) {
	p := "http://pxuser:p%40ss-do-not-leak@proxy.corp.example:3128"
	tr, err := New(testToken, p)
	if err != nil {
		t.Fatal(err)
	}
	got := tr.scrub("x " + p + " y pxuser z p@ss-do-not-leak proxy.corp.example:3128 proxy.corp.example " + testToken + " 11111111:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	for _, bad := range []string{"pxuser", "p@ss", "p%40ss", "proxy.corp", "AAAAAAAA"} {
		if strings.Contains(got, bad) {
			t.Errorf("有 %q:%s", bad, got)
		}
	}
	noSecrets(t, "文字", got)
}
