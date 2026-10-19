package larktest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
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

// Encrypt 按飞书的方式加密事件体:AES-256-CBC,key = sha256(EncryptKey),随机 IV 放在
// 密文前,PKCS#7 填充,base64。是 SDK larkevent.EventDecrypt 的逆过程。
func Encrypt(key string, plain []byte) string {
	k := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(k[:])
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, aes.BlockSize+len(buf))
	_, _ = rand.Read(out[:aes.BlockSize])
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], buf)
	return base64.StdEncoding.EncodeToString(out)
}

// Signed 把明文事件包成飞书 webhook 发来的样子:加密的 body 加三个签名头。
func (s *Server) Signed(plain []byte) (http.Header, []byte) {
	body, _ := json.Marshal(map[string]string{"encrypt": Encrypt(s.EncryptKey, plain)})
	ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), "nonce-1"
	h := http.Header{}
	h.Set("X-Lark-Request-Timestamp", ts)
	h.Set("X-Lark-Request-Nonce", nonce)
	h.Set("X-Lark-Signature", larkevent.Signature(ts, nonce, s.EncryptKey, string(body)))
	return h, body
}

// Challenge 是开发者后台保存请求地址时发来的 URL 校验(明文,发送前要 Encrypt)。
func (s *Server) Challenge(token string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "url_verification", "challenge": "c-123", "token": token})
	return b
}
