package lark

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"

	"velagateway/internal/imchannel"
)

var ackBody = []byte(`{"msg":"success"}`)

// challengeRe:飞书的 URL 校验 challenge 是一段 UUID 样的随机串。只回显这种形状的 —— 这条路
// 不验签,别把它变成「凭 Token 就能让网关回显任意内容」。
var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// HandleWebhook 实现 imchannel.Webhooker。只在 webhook 模式、且 Encrypt Key 与 Verification
// Token 都配了时才收;只收加密的请求。
//
// 顺序:解密 → 比对 Token →(URL 校验到此回 challenge)→ 验签 → 分发。URL 校验请求飞书不带
// 签名头,所以签名只能放在认出它之后;但每一种失败都回同一个 403,不让人分辨是解不开、
// Token 不对还是签名不对。鉴权通过后一律 200 —— 飞书会重投非 2xx 的请求。
//
// 不用 SDK 的 dispatcher.Handle:它的签名比较不是常量时间,验签失败回 500(飞书会重投),
// 而且允许不加密的请求只靠 Token 过关。
//
// 不查 X-Lark-Request-Timestamp 的时间窗(spec §6 的 ±5 分钟只要求 Slack):重放一条截获的
// 请求做不成新的事 —— 卡片点击靠 ErrAlreadyDecided 幂等,私聊按 message_id 去重,绑定码
// 一次性。代价只是重放能再换回一句提示语;好处是不必为飞书与网关之间的时钟偏差排障。
func (a *App) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) imchannel.WebhookReply {
	forbidden := imchannel.Reply(http.StatusForbidden)
	if a.mode != modeWebhook || a.encryptKey == "" || a.verifyToken == "" {
		return forbidden
	}
	var env struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &env) != nil || env.Encrypt == "" {
		return forbidden
	}
	plain, err := larkevent.EventDecrypt(env.Encrypt, a.encryptKey)
	if err != nil {
		return forbidden
	}
	var p struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Token     string `json:"token"`
		Schema    string `json:"schema"`
		Header    *struct {
			Token string `json:"token"`
		} `json:"header"`
		Event json.RawMessage `json:"event"`
	}
	if json.Unmarshal(plain, &p) != nil {
		return forbidden
	}
	token := p.Token
	if p.Header != nil {
		token = p.Header.Token
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.verifyToken)) != 1 {
		return forbidden
	}
	if p.Type == "url_verification" {
		// URL 校验是不验签的那条路:只认一个纯粹的校验请求。夹带 schema 2.0 的 header / event
		// 的,不是飞书发来的校验。
		if p.Header != nil || p.Schema != "" || len(p.Event) != 0 || !challengeRe.MatchString(p.Challenge) {
			return forbidden
		}
		b, _ := json.Marshal(map[string]string{"challenge": p.Challenge})
		return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
	}
	want := larkevent.Signature(h.Get(larkevent.EventRequestTimestamp), h.Get(larkevent.EventRequestNonce), a.encryptKey, string(body))
	if subtle.ConstantTimeCompare([]byte(h.Get(larkevent.EventSignature)), []byte(want)) != 1 {
		return forbidden
	}
	resp, err := a.disp.Do(withSink(ctx, sink), plain)
	if err != nil {
		slog.Warn("lark: webhook event not handled", "err", err)
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	if resp == nil {
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
}
