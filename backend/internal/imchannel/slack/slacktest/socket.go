package slacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// upgrade 扮演 Socket Mode 的 WebSocket 入口:只认 apps.connections.open 发出去、还没用过的 ticket;
// 连上先发一个 hello,与 Slack 一样。
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	s.mu.Lock()
	ok := s.tickets[ticket]
	delete(s.tickets, ticket)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "num_connections": 1,
		"connection_info": map[string]any{"app_id": "A0TEST001"},
		"debug_info":      map[string]any{"host": "applink-test", "approximate_connection_time": 18060},
	})
	_ = s.write(conn, hello)
	s.mu.Lock()
	s.conn = conn
	s.connects++
	s.mu.Unlock()
	go s.readLoop(conn)
}

// readLoop 收适配器回的确认({"envelope_id": …}),交给等它的 Push。WebSocket 的 ping 由 gorilla
// 默认处理器回 pong。
func (s *Server) readLoop(conn *websocket.Conn) {
	defer func() {
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.mu.Unlock()
		conn.Close()
	}()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var ack struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if json.Unmarshal(msg, &ack) != nil {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[ack.EnvelopeID]
		s.mu.Unlock()
		if ch != nil {
			// 不阻塞:适配器若对同一个信封确认了两次,第二次丢掉,别把读循环卡死。
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Server) write(conn *websocket.Conn, b []byte) error {
	if conn == nil {
		return fmt.Errorf("slacktest: no socket mode connection")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, b)
}

// WaitConnected 等适配器连上来。
func (s *Server) WaitConnected(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		up := s.conn != nil
		s.mu.Unlock()
		if up {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("slacktest: the socket mode connection never came up")
}

// WaitConnects 等到总共建立过至少 n 条连接,且当前有一条连着。
func (s *Server) WaitConnects(t testing.TB, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := s.connects >= n && s.conn != nil
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: %d connections within 5s, want %d", s.ConnectCount(), n)
}

// ConnectCount 是到目前为止建立过的连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经 Socket Mode 推一个信封,等适配器确认。Slack 要求 3 秒内确认,超时就当测试失败。
func (s *Server) Push(t testing.TB, typ string, payload any) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextEnv++
	id := fmt.Sprintf("env-%d", s.nextEnv)
	ch := make(chan struct{}, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	b, _ := json.Marshal(map[string]any{
		"envelope_id": id, "type": typ, "payload": payload,
		"accepts_response_payload": false, "retry_attempt": 0, "retry_reason": "",
	})
	if err := s.write(conn, b); err != nil {
		t.Fatalf("slacktest: push: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("slacktest: envelope %s (%s) was not acknowledged within 3s", id, typ)
	}
}

// Interaction 造一次 block_actions 载荷(按钮点击)。channel 是卡片所在的会话。
func Interaction(userID, channel, actionID, value string) map[string]any {
	return map[string]any{
		"type": "block_actions", "api_app_id": "A0TEST001", "trigger_id": "1.2.3",
		"team":         map[string]any{"id": "T0TEST001", "domain": "aegis-test"},
		"user":         map[string]any{"id": userID, "team_id": "T0TEST001", "username": "tester"},
		"container":    map[string]any{"type": "message", "message_ts": "1700000000.000001", "channel_id": channel, "is_ephemeral": false},
		"channel":      map[string]any{"id": channel, "name": "directmessage"},
		"response_url": "https://hooks.slack.com/actions/T0TEST001/1/abc",
		"actions": []any{map[string]any{
			"type": "button", "action_id": actionID, "block_id": "aegis_actions", "value": value, "action_ts": "1700000000.100000",
		}},
	}
}

// Click 推一次「通过」(act = a)或「驳回」(r),返回只回给点击者的那条消息。
func (s *Server) Click(t testing.TB, userID, apNo, act string) string {
	t.Helper()
	action := "aegis_approve"
	if act == "r" {
		action = "aegis_reject"
	}
	before := len(s.Calls("chat.postEphemeral"))
	s.Push(t, "interactive", Interaction(userID, DMChannel(userID), action, "ap:"+apNo+":"+act))
	return s.WaitEphemeral(t, before)
}

// WaitEphemeral 等到第 n+1 条「仅你可见」的消息出现(n 从 0 起),返回它的正文。
func (s *Server) WaitEphemeral(t testing.TB, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls := s.Calls("chat.postEphemeral"); len(calls) > n {
			return calls[n].Get("text")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: ephemeral message #%d never came", n+1)
	return ""
}

// MessageEvent 造一条 message.im 事件(events_api 的载荷,也是 webhook 推来的 JSON)。extra 覆盖
// event 里的字段(值为 nil 时删掉该字段):subtype、bot_id、channel_type …
func MessageEvent(userID, text, eventID string, extra map[string]any) map[string]any {
	ev := map[string]any{
		"type": "message", "channel": DMChannel(userID), "channel_type": "im", "user": userID,
		"text": text, "ts": "1700000002.000001", "client_msg_id": "c-" + eventID, "team": "T0TEST001",
	}
	for k, v := range extra {
		if v == nil {
			delete(ev, k)
		} else {
			ev[k] = v
		}
	}
	return map[string]any{
		"type": "event_callback", "token": "legacy-verification-token", "team_id": "T0TEST001", "api_app_id": "A0TEST001",
		"event": ev, "event_id": eventID, "event_time": 1700000002,
	}
}

// SendDM 推一条私聊文本(每次一个新的 event_id)。
func (s *Server) SendDM(t testing.TB, userID, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextEvent++
	id := fmt.Sprintf("Ev%06d", s.nextEvent)
	s.mu.Unlock()
	s.Push(t, "events_api", MessageEvent(userID, text, id, nil))
}

// Disconnect 发一个 disconnect 信封,不等确认:refresh_requested / warning 是 Slack 定期刷新连接,
// link_disabled 是应用后台关掉了 Socket Mode。
func (s *Server) Disconnect(t testing.TB, reason string) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.conn = nil // 之后的 Push 等新连接
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "disconnect", "reason": reason, "debug_info": map[string]any{"host": "applink-test"}})
	if err := s.write(conn, b); err != nil {
		t.Fatalf("slacktest: disconnect: %v", err)
	}
}

// DropConnection 直接断开当前连接(网络抖动)。
func (s *Server) DropConnection() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// SetEndpoint 让之后的 apps.connections.open 下发这个地址(扮演一个被劫持或指向内网的下发)。空串恢复默认。
func (s *Server) SetEndpoint(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoint = endpoint
}
