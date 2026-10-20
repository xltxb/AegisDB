package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"velagateway/internal/imchannel"
)

// maxSkew 是 webhook 请求时间戳允许的偏差(spec §6:±5 分钟)。签名只证明请求出自 Slack,时间窗
// 才挡得住把一条截获的请求原样重放。
const maxSkew = 5 * time.Minute

// challengeRe:url_verification 的 challenge 是一段随机串。只回显这种形状的。
var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// HandleWebhook 实现 imchannel.Webhooker。只在 webhook 模式、且配了 Signing Secret 时才收。
//
// 交互(表单 payload=…)与事件(JSON)走同一个地址,按 Content-Type 分辨。验签或时间窗不过 → 403,
// 每一种失败都是同一个 403;验签通过后一律 200 —— Slack 会重投非 2xx 的请求。
func (a *App) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) imchannel.WebhookReply {
	if a.mode != modeWebhook || a.signingSecret == "" || !a.verify(h, body) {
		return imchannel.Reply(http.StatusForbidden)
	}
	ok := imchannel.Reply(http.StatusOK)
	if mt, _, _ := mime.ParseMediaType(h.Get("Content-Type")); mt == "application/x-www-form-urlencoded" {
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("payload") == "" {
			return ok // 斜杠命令之类:网关不提供,确认收到即可
		}
		// 判定同步做完再回 200(只查库,很快);把结果告诉点击者要再调一次 Slack,放到协程里 ——
		// Slack 只等 3 秒。
		toast, channel, user := a.onInteractive(ctx, sink, []byte(form.Get("payload")))
		go a.tell(ctx, channel, user, toast)
		return ok
	}
	var env struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ok
	}
	if env.Type == "url_verification" {
		if !challengeRe.MatchString(env.Challenge) {
			return ok
		}
		b, _ := json.Marshal(map[string]string{"challenge": env.Challenge})
		return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
	}
	// Slack 3 秒内没拿到 200 会带着 X-Slack-Retry-Num 重投:onEvent 按 event_id 去重。
	a.onEvent(ctx, sink, body)
	return ok
}

// verify 验 Slack 的请求签名:v0=hex(HMAC-SHA256(signingSecret, "v0:<ts>:<body>")),常量时间比较;
// 时间戳与网关时钟相差超过 maxSkew 的一律不认。
func (a *App) verify(h http.Header, body []byte) bool {
	ts := h.Get("X-Slack-Request-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := a.now().Sub(time.Unix(sec, 0)); d > maxSkew || d < -maxSkew {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.signingSecret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(h.Get("X-Slack-Signature")), []byte(want)) == 1
}
