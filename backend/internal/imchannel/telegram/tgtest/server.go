// Package tgtest 是一个假的 Telegram Bot API 服务器:记录每次调用,按需返回 update,
// 让适配器和端到端测试都不必连真实的 Telegram。
package tgtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel/telegram"
)

type Call struct {
	Method string
	Params map[string]any
}

// Failure 让某个方法回 ok:false。
type Failure struct {
	Code int
	Desc string
}

type Server struct {
	*httptest.Server
	Token       string
	BotUsername string

	mu      sync.Mutex
	calls   []Call
	updates []telegram.Update
	nextID  int64
	nextMsg int64
	fail    map[string]Failure
	wake    chan struct{}
}

func New(t testing.TB) *Server {
	s := &Server{
		Token: "123456:TEST-bot-token-abcdef", BotUsername: "aegis_test_bot",
		nextMsg: 100, fail: map[string]Failure{}, wake: make(chan struct{}, 1),
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + s.Token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	s.mu.Unlock()
	if failing {
		writeJSON(w, map[string]any{"ok": false, "error_code": f.Code, "description": f.Desc})
		return
	}
	switch method {
	case "getMe":
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"id": 42, "is_bot": true, "username": s.BotUsername}})
	case "getUpdates":
		writeJSON(w, map[string]any{"ok": true, "result": s.take(r.Context(), int64(num(params["offset"])))})
	case "sendMessage":
		s.mu.Lock()
		s.nextMsg++
		id := s.nextMsg
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"message_id": id, "chat": map[string]any{"id": int64(num(params["chat_id"])), "type": "private"},
		}})
	default:
		writeJSON(w, map[string]any{"ok": true, "result": true})
	}
}

// take 返回 update_id >= offset 的 update。没有就最多等 300ms —— 真实的长轮询会挂 25 秒,
// 测试里不必。offset 之前的 update 永远不再返回,这正是 Telegram「确认」的语义。
func (s *Server) take(ctx context.Context, offset int64) []telegram.Update {
	deadline := time.After(300 * time.Millisecond)
	for {
		s.mu.Lock()
		out := []telegram.Update{}
		for _, u := range s.updates {
			if u.UpdateID >= offset {
				out = append(out, u)
			}
		}
		s.mu.Unlock()
		if len(out) > 0 {
			return out
		}
		select {
		case <-s.wake:
		case <-deadline:
			return out
		case <-ctx.Done():
			return out
		}
	}
}

// Push 排进一条 update。UpdateID 为 0 时自动分配;给定的话可以用来模拟重投。
func (s *Server) Push(u telegram.Update) int64 {
	s.mu.Lock()
	if u.UpdateID == 0 {
		s.nextID++
		u.UpdateID = s.nextID
	} else if u.UpdateID > s.nextID {
		s.nextID = u.UpdateID
	}
	s.updates = append(s.updates, u)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return u.UpdateID
}

func (s *Server) PushText(fromID int64, chatType, text string) {
	chatID := fromID
	if chatType != "private" {
		chatID = -1000 - fromID
	}
	s.Push(telegram.Update{Message: &telegram.Message{
		MessageID: 1, From: &telegram.User{ID: fromID}, Chat: telegram.Chat{ID: chatID, Type: chatType}, Text: text,
	}})
}

// PushCallback 模拟一次按钮点击,返回 callback_query 的 id。
func (s *Server) PushCallback(fromID int64, data string) string {
	s.mu.Lock()
	id := fmt.Sprintf("cq-%d", s.nextID+1)
	s.mu.Unlock()
	s.Push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: id, From: telegram.User{ID: fromID}, Data: data}})
	return id
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
	t.Fatalf("tgtest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Str 取一个参数的字符串形式(chat_id 可能是数字也可能是字符串)。
func (c Call) Str(key string) string {
	switch v := c.Params[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
