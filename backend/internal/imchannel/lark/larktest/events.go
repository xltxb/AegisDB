package larktest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// envelope 包成飞书 schema 2.0 的事件。
func (s *Server) envelope(eventType string, event any) []byte {
	s.mu.Lock()
	s.nextEvent++
	n := s.nextEvent
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id": fmt.Sprintf("ev-%d", n), "event_type": eventType, "app_id": s.AppID,
			"token": s.VerificationToken, "create_time": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
		"event": event,
	})
	return b
}

// CardAction 造一次卡片按钮点击(card.action.trigger 回调)。
func (s *Server) CardAction(openID string, value map[string]any) []byte {
	return s.envelope("card.action.trigger", map[string]any{
		"operator": map[string]any{"open_id": openID},
		"action":   map[string]any{"tag": "button", "value": value},
		"context":  map[string]any{"open_message_id": "om_card"},
	})
}

// Message 造一条 im.message.receive_v1。messageID 相同就是飞书在重投同一条消息。
func (s *Server) Message(openID, chatType, text, messageID string) []byte {
	content, _ := json.Marshal(map[string]string{"text": text})
	return s.envelope("im.message.receive_v1", map[string]any{
		"sender": map[string]any{"sender_id": map[string]any{"open_id": openID}, "sender_type": "user"},
		"message": map[string]any{
			"message_id": messageID, "chat_id": "oc_" + chatType, "chat_type": chatType,
			"message_type": "text", "content": string(content),
		},
	})
}

// Click 经长连接推一次按钮点击,返回网关回的 toast。
func (s *Server) Click(t testing.TB, openID string, value map[string]any) string {
	t.Helper()
	r := s.Push(t, s.CardAction(openID, value))
	var out struct {
		Toast struct {
			Content string `json:"content"`
		} `json:"toast"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Toast.Content
}

// SendDM 经长连接推一条消息,等网关处理完(包括它的回复)才返回。
func (s *Server) SendDM(t testing.TB, openID, chatType, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextEvent++
	id := fmt.Sprintf("om_dm%d", s.nextEvent)
	s.mu.Unlock()
	s.Push(t, s.Message(openID, chatType, text, id))
}
