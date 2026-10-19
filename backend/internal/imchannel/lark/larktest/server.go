// Package larktest 是一个假的飞书开放平台:记录每次 OpenAPI 调用,按需注入失败,并扮演
// 长连接网关把事件推给 SDK(ws.go)。适配器和端到端测试都用它,不连真实的飞书。
package larktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type Call struct {
	Method string // token | botInfo | send | patch | wsEndpoint
	Params map[string]any
}

// Failure 让某个方法回一个飞书错误。HTTP 为 0 时按 400 回。
//
// Raw 非空时不回飞书的 JSON,原样回这段 body(HTTP 为 0 时按 200),Location 非空时带上
// 这个头 —— 扮演填错的开放平台地址:一个回 200 HTML 的控制台页面,或一次重定向。
type Failure struct {
	HTTP     int
	Code     int
	Msg      string
	Raw      string
	Location string
}

type Server struct {
	*httptest.Server
	AppID             string
	AppSecret         string
	BotName           string
	VerificationToken string
	EncryptKey        string

	mu      sync.Mutex
	calls   []Call
	fail    map[string]Failure
	tenant  string
	rotated int
	nextMsg int

	// 长连接(ws.go)
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connects  int
	waiting   map[string]chan larkws.Response
	nextFrame int
	nextEvent int
}

func New(t testing.TB) *Server {
	s := &Server{
		AppID: "cli_test_app", AppSecret: "test-app-secret-0000", BotName: "AegisDB 测试机器人",
		VerificationToken: "test-verification-token", EncryptKey: "test-encrypt-key",
		fail: map[string]Failure{}, tenant: "t-tenant-0", nextMsg: 100,
		waiting: map[string]chan larkws.Response{},
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

// RotateToken 作废当前的 tenant_access_token:之后带旧 token 的调用回 99991663。
func (s *Server) RotateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotated++
	s.tenant = fmt.Sprintf("t-tenant-%d", s.rotated)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" {
		s.upgrade(w, r)
		return
	}
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	if params == nil {
		params = map[string]any{}
	}
	var method string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
		method = "token"
	case r.Method == http.MethodGet && r.URL.Path == "/open-apis/bot/v3/info":
		method = "botInfo"
	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
		method = "send"
		params["receive_id_type"] = r.URL.Query().Get("receive_id_type")
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
		method = "patch"
		params["message_id"] = strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
	case r.Method == http.MethodPost && r.URL.Path == "/callback/ws/endpoint":
		method = "wsEndpoint"
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	tenant := s.tenant
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
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"code": f.Code, "msg": f.Msg})
		return
	}
	if method == "wsEndpoint" {
		s.endpoint(w, params)
		return
	}
	if method == "token" {
		if params["app_id"] != s.AppID || params["app_secret"] != s.AppSecret {
			// 把收到的 app_secret 回显进 msg:扮演一个往错误里回显请求的代理 / 私有化部署,
			// 让「错误里不出现 App Secret」的测试真的有东西可拦(否则删掉 scrub 它们也照样绿)。
			got, _ := params["app_secret"].(string)
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 10014, "msg": "app secret invalid: " + got})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": tenant, "expire": 7200})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+tenant {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 99991663, "msg": "Invalid access token for authorization"})
		return
	}
	switch method {
	case "botInfo":
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"app_name": s.BotName, "open_id": "ou_bot"}})
	case "send":
		s.mu.Lock()
		s.nextMsg++
		id := fmt.Sprintf("om_test%d", s.nextMsg)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": id}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
	}
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
	t.Fatalf("larktest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Str 取一个参数的字符串形式;非字符串的值按 JSON 返回。
func (c Call) Str(key string) string {
	switch v := c.Params[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
