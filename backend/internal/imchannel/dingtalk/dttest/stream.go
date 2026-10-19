package dttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// upgrade 扮演 Stream 网关的 WebSocket 入口:只认 open 发出去、还没用过的 ticket。
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
	s.mu.Lock()
	s.conn = conn
	s.connects++
	s.mu.Unlock()
	go s.readLoop(conn)
}

// readLoop 收适配器发上来的回执,交给等它的 Push。帧的编解码直接用 SDK 的 payload 类型 —— 测到
// 的是钉钉 Stream 的线上格式。WebSocket 的 ping 由 gorilla 默认处理器回 pong。
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
		var resp payload.DataFrameResponse
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[resp.GetHeader(payload.DataFrameHeaderKMessageId)]
		s.mu.Unlock()
		if ch != nil {
			// 不阻塞:适配器若对同一帧回了两次执,第二次丢掉,别把读循环卡死。
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

func (s *Server) write(conn *websocket.Conn, b []byte) error {
	if conn == nil {
		return fmt.Errorf("dttest: no stream connection")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, b)
}

func frame(typ, topic, id, data string) []byte {
	now := time.Now().UnixMilli()
	df := payload.DataFrame{SpecVersion: "1.0", Type: typ, Time: now, Data: data, Headers: payload.DataFrameHeader{
		payload.DataFrameHeaderKTopic:       topic,
		payload.DataFrameHeaderKMessageId:   id,
		payload.DataFrameHeaderKContentType: payload.DataFrameContentTypeKJson,
		payload.DataFrameHeaderKTime:        strconv.FormatInt(now, 10),
	}}
	return df.Encode()
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
	t.Fatal("dttest: the stream connection never came up")
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
	t.Fatalf("dttest: %d connections within 5s, want %d", s.ConnectCount(), n)
}

// ConnectCount 是到目前为止建立过的连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经 Stream 推一帧,等适配器回执并返回它。
func (s *Server) Push(t testing.TB, typ, topic, data string) payload.DataFrameResponse {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextFrame++
	id := fmt.Sprintf("frame-%d", s.nextFrame)
	ch := make(chan payload.DataFrameResponse, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	if err := s.write(conn, frame(typ, topic, id, data)); err != nil {
		t.Fatalf("dttest: push: %v", err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("dttest: no reply to the pushed frame within 5s")
		return payload.DataFrameResponse{}
	}
}

// CardCallback 造一次卡片按钮点击的载荷(topic /v1.0/card/instances/callback)。按钮的回传参数在
// content 这段 JSON 字符串的 cardPrivateData.params 里。
func CardCallback(userID, outTrackID, act string, userIDType int) string {
	content, _ := json.Marshal(map[string]any{"cardPrivateData": map[string]any{
		"actionIds": []string{"decide"}, "params": map[string]any{"act": act},
	}})
	b, _ := json.Marshal(map[string]any{
		"outTrackId": outTrackID, "userId": userID, "userIdType": userIDType, "type": "actionCallback",
		"corpId": "dingcorp", "spaceType": "IM_ROBOT", "spaceId": userID, "extension": "{}", "content": string(content),
	})
	return string(b)
}

// Tip 取卡片回调应答里写回卡片的 tip 变量。
func Tip(r payload.DataFrameResponse) string {
	var body struct {
		Response struct {
			CardData struct {
				CardParamMap map[string]string `json:"cardParamMap"`
			} `json:"cardData"`
		} `json:"response"`
	}
	_ = json.Unmarshal([]byte(r.Data), &body)
	return body.Response.CardData.CardParamMap["tip"]
}

// Click 推一次点击,返回卡片上更新出来的提示语。
func (s *Server) Click(t testing.TB, userID, outTrackID, act string) string {
	t.Helper()
	return Tip(s.Push(t, "CALLBACK", payload.CardInstanceCallbackTopic, CardCallback(userID, outTrackID, act, 1)))
}

// Message 造一条机器人收到的消息(topic /v1.0/im/bot/messages/get)。conversationType "1" 是单聊,
// "2" 是群聊。
func Message(staffID, conversationType, msgType, text, msgID string) string {
	now := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{
		"conversationId": "cid-" + staffID, "chatbotCorpId": "dingcorp", "chatbotUserId": "$:LWCP_v1:$bot",
		"msgId": msgID, "senderNick": "测试用户", "isAdmin": false, "senderStaffId": staffID,
		"sessionWebhookExpiredTime": now + 3_600_000, "createAt": now, "senderCorpId": "dingcorp",
		"conversationType": conversationType, "senderId": "$:LWCP_v1:$" + staffID,
		"sessionWebhook": "https://oapi.dingtalk.com/robot/sendBySession?session=test",
		"text":           map[string]any{"content": text}, "msgtype": msgType,
	})
	return string(b)
}

// SendDM 推一条文本消息并等回执。
func (s *Server) SendDM(t testing.TB, staffID, conversationType, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextMsg++
	id := fmt.Sprintf("msg-%d", s.nextMsg)
	s.mu.Unlock()
	if r := s.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, Message(staffID, conversationType, "text", text, id)); r.Code != http.StatusOK {
		t.Fatalf("dttest: message not acknowledged: %+v", r)
	}
}

// Disconnect 发一帧 SYSTEM disconnect(钉钉发布、迁移时这样要求客户端换连接),不等回执。
func (s *Server) Disconnect(t testing.TB) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.conn = nil // 之后的 Push 等新连接
	s.mu.Unlock()
	if err := s.write(conn, frame("SYSTEM", "disconnect", "disconnect-1", `{"reason":"server restart"}`)); err != nil {
		t.Fatalf("dttest: disconnect: %v", err)
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

// SetEndpoint 让之后的 open 下发这个长连接地址(扮演一个被劫持或指向内网的下发)。空串恢复默认。
func (s *Server) SetEndpoint(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoint = endpoint
}
