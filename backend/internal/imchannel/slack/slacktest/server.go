// Package slacktest 是一个假的 Slack:记录每次 Web API 调用,按需注入失败,并扮演 Socket Mode 网关
// 把信封推给适配器(socket.go)。适配器和端到端测试都用它,不连真实的 Slack。
package slacktest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type Call struct {
	Method string // Web API 方法名,如 chat.postMessage
	Token  string // Authorization: Bearer 之后的那段
	Params url.Values
}

func (c Call) Get(key string) string { return c.Params.Get(key) }

// Failure 让某个方法回一个 Slack 错误:HTTP 为 0 时按 200 回 {"ok":false,"error":Error}(Slack 的惯例),
// Needed 非空时带上缺的权限。Raw 非空时原样回这段 body(HTTP 为 0 时按 200),Location 非空时带上这个头
// —— 扮演填错的 API 地址:一个回 200 HTML 的页面,或一次重定向。Times > 0 时只失败这么多次。
type Failure struct {
	HTTP     int
	Error    string
	Needed   string
	Raw      string
	Location string
	Times    int
}

type Server struct {
	*httptest.Server
	BotToken      string
	AppToken      string
	SigningSecret string
	BotUserID     string
	BotName       string

	mu         sync.Mutex
	calls      []Call
	fail       map[string]Failure
	nextTS     int
	messages   map[string]string // "<channel>|<ts>" → blocks(JSON)
	tickets    map[string]bool
	nextTicket int
	endpoint   string // 非空时 apps.connections.open 下发它,而不是自己的 /link

	// Socket Mode(socket.go)
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connects  int
	waiting   map[string]chan struct{}
	nextEnv   int
	nextEvent int
}

func New(t testing.TB) *Server {
	s := &Server{
		BotToken: "xoxb-1-test-bot-token-0000", AppToken: "xapp-1-test-app-token-0000",
		SigningSecret: "8f742231b10e8888abcd99edf5d7d85a", BotUserID: "U0BOT0001", BotName: "aegisbot",
		fail: map[string]Failure{}, messages: map[string]string{}, tickets: map[string]bool{},
		waiting: map[string]chan struct{}{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) SetFail(method string, f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// rejectToken 回 Slack 拒绝 token 的错误,并把收到的 token 回显进 response_metadata:扮演一个往错误里
// 回显请求的代理,让「错误里不出现 token」的测试真的有东西可拦(否则删掉 scrub 它们也照样绿)。
// 拿另一种 token 来调(Bot Token 去换 Socket Mode 地址、App Token 去发消息)是 not_allowed_token_type。
func (s *Server) rejectToken(w http.ResponseWriter, got string) {
	code := "invalid_auth"
	if got == s.BotToken || got == s.AppToken {
		code = "not_allowed_token_type"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": false, "error": code,
		"response_metadata": map[string]any{"messages": []string{"[ERROR] token rejected: " + got}},
	})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/link" {
		s.upgrade(w, r)
		return
	}
	method, isAPI := strings.CutPrefix(r.URL.Path, "/api/")
	if !isAPI || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Token: token, Params: r.PostForm})
	f, failing := s.fail[method]
	if failing && f.Times > 0 {
		if f.Times == 1 {
			delete(s.fail, method)
		} else {
			s.fail[method] = Failure{HTTP: f.HTTP, Error: f.Error, Needed: f.Needed, Raw: f.Raw, Location: f.Location, Times: f.Times - 1}
		}
	}
	s.mu.Unlock()
	if failing && (f.Raw != "" || f.Location != "") {
		status := f.HTTP
		if status == 0 {
			status = http.StatusOK
		}
		if f.Location != "" {
			w.Header().Set("Location", f.Location)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(f.Raw))
		return
	}
	if failing {
		status := f.HTTP
		if status == 0 {
			status = http.StatusOK
		}
		body := map[string]any{"ok": false, "error": f.Error}
		if f.Needed != "" {
			body["needed"] = f.Needed
		}
		writeJSON(w, status, body)
		return
	}
	if method == "apps.connections.open" {
		s.open(w, token)
		return
	}
	if token != s.BotToken {
		s.rejectToken(w, token)
		return
	}
	p := r.PostForm
	switch method {
	case "auth.test":
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "url": "https://aegis-test.slack.com/", "team": "Aegis Test", "user": s.BotName,
			"team_id": "T0TEST001", "user_id": s.BotUserID, "bot_id": "B0TEST001",
		})
	case "conversations.open":
		users := p.Get("users")
		if !strings.HasPrefix(users, "U") && !strings.HasPrefix(users, "W") {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "user_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"id": DMChannel(users)}})
	case "chat.postMessage":
		ch := p.Get("channel")
		s.mu.Lock()
		s.nextTS++
		ts := fmt.Sprintf("1700000000.%06d", s.nextTS)
		s.messages[ch+"|"+ts] = p.Get("blocks")
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch, "ts": ts})
	case "chat.update":
		key := p.Get("channel") + "|" + p.Get("ts")
		s.mu.Lock()
		_, found := s.messages[key]
		if found {
			s.messages[key] = p.Get("blocks")
		}
		s.mu.Unlock()
		if !found {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "message_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": p.Get("channel"), "ts": p.Get("ts")})
	case "conversations.history":
		// 取单条消息的标准做法:latest = 它的 ts、inclusive、limit 1。
		latest := p.Get("latest")
		s.mu.Lock()
		blocks, found := s.messages[p.Get("channel")+"|"+latest]
		s.mu.Unlock()
		msgs := []any{}
		if found && p.Get("inclusive") == "true" {
			m := map[string]any{"type": "message", "ts": latest, "user": s.BotUserID, "bot_id": "B0TEST001"}
			if blocks != "" {
				m["blocks"] = json.RawMessage(blocks)
			}
			msgs = append(msgs, m)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": msgs, "has_more": false})
	case "chat.postEphemeral":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_ts": "1700000001.000001"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

// open 扮演 apps.connections.open:App Token 换一个带一次性 ticket 的 Socket Mode 地址。
func (s *Server) open(w http.ResponseWriter, token string) {
	if token != s.AppToken {
		s.rejectToken(w, token)
		return
	}
	s.mu.Lock()
	s.nextTicket++
	ticket := fmt.Sprintf("ticket-%d", s.nextTicket)
	s.tickets[ticket] = true
	endpoint := s.endpoint
	s.mu.Unlock()
	if endpoint == "" {
		endpoint = "ws" + strings.TrimPrefix(s.URL, "http") + "/link?ticket=" + ticket
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": endpoint})
}

// DMChannel 是假 Slack 给某个成员开的私聊频道 ID(与 conversations.open 回的一致):U0OWNER → D0OWNER。
func DMChannel(userID string) string {
	if len(userID) < 2 {
		return "D0NOBODY"
	}
	return "D" + userID[1:]
}

// Blocks 取一条消息当前的 blocks(JSON);没有这条消息时返回 ""。
func (s *Server) Blocks(channel, ts string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messages[channel+"|"+ts]
}

func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// WaitCall 等到出现一次满足 match 的调用(match 为 nil 表示任意一次)。
func (s *Server) WaitCall(t testing.TB, method string, match func(Call) bool) Call {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range s.Calls(method) {
			if match == nil || match(c) {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Sign 是 Slack 给 webhook 请求的签名:v0=hex(HMAC-SHA256(secret, "v0:<ts>:<body>"))。
func Sign(secret string, ts int64, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%d:%s", ts, body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// SignedHeaders 造一组带当前时间戳与签名的 webhook 请求头。contentType 为空时按 JSON(事件);
// 交互是 application/x-www-form-urlencoded。
func (s *Server) SignedHeaders(body, contentType string) map[string]string {
	if contentType == "" {
		contentType = "application/json"
	}
	ts := time.Now().Unix()
	return map[string]string{
		"Content-Type":              contentType,
		"X-Slack-Request-Timestamp": strconv.FormatInt(ts, 10),
		"X-Slack-Signature":         Sign(s.SigningSecret, ts, body),
	}
}
