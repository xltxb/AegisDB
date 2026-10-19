package larktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// endpoint 扮演 /callback/ws/endpoint:校验应用凭据,发放 WebSocket 地址。凭据不对回 514 ——
// SDK 把它当成不可重试的 ClientError。
func (s *Server) endpoint(w http.ResponseWriter, params map[string]any) {
	if params["AppID"] != s.AppID || params["AppSecret"] != s.AppSecret {
		writeJSON(w, http.StatusOK, map[string]any{"code": 514, "msg": "auth failed"})
		return
	}
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws?device_id=dev-1&service_id=1"
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"URL": wsURL,
		// 重连快、不加随机延迟、两分钟一次 ping:测试里连接断了马上能重连上。
		"ClientConfig": map[string]any{"ReconnectCount": -1, "ReconnectInterval": 1, "ReconnectNonce": 0, "PingInterval": 120},
	}})
}

func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
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

// readLoop 收 SDK 发上来的帧:ping 回 pong,数据帧是某条事件的回执,交给等它的 Push。
// 帧的编解码直接用 SDK 的 protobuf 类型 —— 测到的是 SDK 真正的协议。
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
		var f larkws.Frame
		if f.Unmarshal(msg) != nil {
			continue
		}
		hs := larkws.Headers(f.Headers)
		if f.Method == int32(larkws.FrameTypeControl) {
			if hs.GetString(larkws.HeaderType) == string(larkws.MessageTypePing) {
				pong := larkws.Frame{Method: int32(larkws.FrameTypeControl), Service: f.Service,
					Headers: []larkws.Header{{Key: larkws.HeaderType, Value: string(larkws.MessageTypePong)}}}
				s.write(conn, &pong)
			}
			continue
		}
		var resp larkws.Response
		_ = json.Unmarshal(f.Payload, &resp)
		s.mu.Lock()
		ch := s.waiting[hs.GetString(larkws.HeaderMessageID)]
		s.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (s *Server) write(conn *websocket.Conn, f *larkws.Frame) error {
	b, err := f.Marshal()
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, b)
}

// WaitConnected 等 SDK 连上来。
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
	t.Fatal("larktest: the long connection never came up")
}

// ConnectCount 是到目前为止建立过的长连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经长连接推一条事件(type=event 的数据帧),等 SDK 回执并返回它。
func (s *Server) Push(t testing.TB, payload []byte) larkws.Response {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextFrame++
	id := fmt.Sprintf("frame-%d", s.nextFrame)
	ch := make(chan larkws.Response, 1)
	s.waiting[id] = ch
	seq := s.nextFrame
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	f := larkws.Frame{
		SeqID: uint64(seq), Service: 1, Method: int32(larkws.FrameTypeData),
		Headers: []larkws.Header{
			{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)},
			{Key: larkws.HeaderMessageID, Value: id},
			{Key: larkws.HeaderSum, Value: "1"},
			{Key: larkws.HeaderSeq, Value: "0"},
			{Key: larkws.HeaderTraceID, Value: "trace-" + id},
		},
		Payload: payload,
	}
	if err := s.write(conn, &f); err != nil {
		t.Fatalf("larktest: push: %v", err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("larktest: no reply to the pushed event within 5s")
		return larkws.Response{}
	}
}
