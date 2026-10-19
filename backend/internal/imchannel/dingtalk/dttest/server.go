// Package dttest 是一个假的钉钉开放平台:记录每次 OpenAPI 调用,按需注入失败,并扮演 Stream
// 网关把回调推给适配器(stream.go)。适配器和端到端测试都用它,不连真实的钉钉。
package dttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type Call struct {
	Method string // token | open | deliver | update | oto
	Params map[string]any
}

// Failure 让某个方法回一个钉钉错误(HTTP 为 0 时按 400)。
//
// Raw 非空时不回钉钉的 JSON,原样回这段 body(HTTP 为 0 时按 200);Location 非空时带上这个头
// —— 扮演填错的开放平台地址:一个回 200 HTML 的控制台页面,或一次重定向。
type Failure struct {
	HTTP     int
	Code     string
	Msg      string
	Raw      string
	Location string
}

type Server struct {
	*httptest.Server
	ClientID     string
	ClientSecret string

	mu         sync.Mutex
	calls      []Call
	fail       map[string]Failure
	token      string
	rotated    int
	rejectWith string
	tickets    map[string]bool
	nextTicket int
}

func New(t testing.TB) *Server {
	s := &Server{
		ClientID: "dingtestclientid", ClientSecret: "test-client-secret-0000",
		fail: map[string]Failure{}, token: "at-0", tickets: map[string]bool{},
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

// RotateToken 作废当前的 access token:之后带旧 token 的调用回 401。
func (s *Server) RotateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotated++
	s.token = fmt.Sprintf("at-%d", s.rotated)
}

// RejectDelivery 让之后的投放「接口成功、投放失败」:HTTP 200,deliverResults 里 success=false。
// 对方不在应用可见范围内时钉钉就是这么回的。
func (s *Server) RejectDelivery(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectWith = msg
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// credentialError 把收到的 secret 回显进 message:扮演一个往错误里回显请求的代理 / 私有化部署,
// 让「错误里不出现 Client Secret」的测试真的有东西可拦(否则删掉 scrub 它们也照样绿)。
func credentialError(w http.ResponseWriter, got any) {
	g, _ := got.(string)
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"code": "invalidClientIdOrSecret", "message": "无效的应用凭证: " + g, "requestid": "req-test",
	})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	if params == nil {
		params = map[string]any{}
	}
	var method string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/oauth2/accessToken":
		method = "token"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/gateway/connections/open":
		method = "open"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/card/instances/createAndDeliver":
		method = "deliver"
	case r.Method == http.MethodPut && r.URL.Path == "/v1.0/card/instances":
		method = "update"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/robot/oToMessages/batchSend":
		method = "oto"
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	token, reject := s.token, s.rejectWith
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
		writeJSON(w, status, map[string]any{"code": f.Code, "message": f.Msg, "requestid": "req-test"})
		return
	}
	switch method {
	case "token":
		if params["appKey"] != s.ClientID || params["appSecret"] != s.ClientSecret {
			credentialError(w, params["appSecret"])
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "expireIn": 7200})
		return
	case "open":
		// 换长连接地址不带 access token,凭据直接在请求体里。
		if params["clientId"] != s.ClientID || params["clientSecret"] != s.ClientSecret {
			credentialError(w, params["clientSecret"])
			return
		}
		s.mu.Lock()
		s.nextTicket++
		ticket := fmt.Sprintf("ticket-%d", s.nextTicket)
		s.tickets[ticket] = true
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"endpoint": "ws" + strings.TrimPrefix(s.URL, "http") + "/connect", "ticket": ticket})
		return
	}
	if r.Header.Get("x-acs-dingtalk-access-token") != token {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "InvalidAuthentication", "message": "不合法的access_token", "requestid": "req-test"})
		return
	}
	switch method {
	case "deliver":
		space, _ := params["openSpaceId"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": map[string]any{
			"outTrackId": params["outTrackId"],
			"deliverResults": []any{map[string]any{
				"spaceType": "IM_ROBOT", "spaceId": strings.TrimPrefix(space, "dtv1.card//IM_ROBOT."),
				"success": reject == "", "errorMsg": reject, "carrierId": "carrier-1",
			}},
		}})
	case "update":
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": true})
	case "oto":
		writeJSON(w, http.StatusOK, map[string]any{"processQueryKey": "pqk-1", "invalidStaffIdList": []any{}, "flowControlledStaffIdList": []any{}})
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
	t.Fatalf("dttest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// At 取一个(可能嵌套的)参数:At("cardData", "cardParamMap", "command")。非字符串的值按 JSON
// 返回(数字 1 → "1",false → "false"),不存在时返回 ""。
func (c Call) At(keys ...string) string {
	var v any = c.Params
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// Str 取一个顶层参数,同 At(key)。
func (c Call) Str(key string) string { return c.At(key) }
