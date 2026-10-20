// Package slack 是 Slack 渠道:Slack 应用的机器人在私聊里发审批卡片、收起卡片,并经 Socket Mode
// 长连接(socket.go)或 Slack 推来的 webhook(webhook.go)收回点击与私聊。群通知(Incoming Webhook)
// 在第 6 期。
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"velagateway/internal/imchannel"
)

const (
	defaultBase = "https://slack.com/api"

	// WebhookPath 是 Slack 推交互与事件到网关的路径。应用后台「Interactivity & Shortcuts」与
	// 「Event Subscriptions」的 Request URL 都填 <回调根地址>+它 —— Slack 不像 Telegram 那样由网关登记。
	WebhookPath = "/api/v1/im/slack/webhook"

	modeSocket  = "socket"
	modeWebhook = "webhook"
)

// errBadBase:出站防护拒绝了 API 地址(不是 https、解析到内网),或 Slack 给的长连接地址不能用。
// 改配置之前怎么重试都一样。
var errBadBase = errors.New("Slack API 地址不可用")

// transientCodes 是 Slack 自己说「稍后再试」的错误码;其余 ok:false 的错误码(凭据被拒、token 类型
// 不对、缺权限 …)重试也一样。
var transientCodes = map[string]bool{
	"ratelimited": true, "internal_error": true, "fatal_error": true, "service_unavailable": true, "request_timeout": true,
}

// App 是装进一个工作区的 Slack 应用:私信卡片、收起卡片,并收回点击与私聊。
type App struct {
	botToken      string // xoxb-:发消息、改消息
	appToken      string // xapp-:只用来换 Socket Mode 地址
	signingSecret string // webhook 模式验签
	base          string
	mode          string
	client        *http.Client
	validate      func(string) error
	now           func() time.Time // webhook 时间窗用;测试里换掉
	bot           atomic.Value     // botInfo:Run 里 auth.test 查到的
	seen          imchannel.Recent // 已处理的事件 event_id
}

type botInfo struct{ name, userID string }

// New 在配了 Bot Token 时造一个应用,否则返回 nil。不做任何 I/O —— 它在 Hub 的锁里被调用。
// Socket Mode 缺 App Token、webhook 模式缺 Signing Secret 也照样造:Run 会把缺什么报到设置页上,
// 比渠道静悄悄地显示「未配置」更好排查。
func New(d imchannel.Deps) imchannel.Channel {
	key := func(s string) string { return imchannel.Key(imchannel.Slack, s) }
	bot := strings.TrimSpace(d.Settings.Secret(key("botToken")))
	if bot == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = defaultBase
	}
	mode := d.Settings.String(key("mode"))
	if mode != modeWebhook {
		mode = modeSocket
	}
	a := &App{
		botToken: bot, base: base, mode: mode,
		appToken:      strings.TrimSpace(d.Settings.Secret(key("appToken"))),
		signingSecret: strings.TrimSpace(d.Settings.Secret(key("signingSecret"))),
		client:        imchannel.NoRedirects(d.HTTP), validate: d.ValidateURL, now: time.Now,
	}
	a.bot.Store(botInfo{})
	return a
}

func (a *App) Kind() string { return imchannel.Slack }

// BotName 返回 Run 里查到并缓存的机器人用户名。Hub 锁内调用,所以不做 I/O。
func (a *App) BotName() string { return a.self().name }

func (a *App) self() botInfo { return a.bot.Load().(botInfo) }

// apiError 是 Slack 明确回了错误:ok:false、HTTP 非 2xx,或者回了个解析不了的响应。Code 是 Slack 的错误码。
type apiError struct {
	Method string
	HTTP   int
	Code   string
	Msg    string
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("Slack %s: HTTP %d", e.Method, e.HTTP)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Msg != "" {
		s += " " + e.Msg
	}
	return s
}

// permanent 判断重试无望的错误:Slack 明确拒绝(token 错或类型不对、缺权限 …)、API 地址填错(回了
// 3xx / 4xx、回了解析不了或没有 ok 字段的 2xx、被出站防护拒绝)。限流、429、5xx、Slack 自己说的
// 临时错误、网络错误(含 DNS 解析失败)是暂时的。
func permanent(err error) bool {
	if errors.Is(err, errBadBase) {
		return true
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.HTTP < 500 && ae.HTTP != http.StatusTooManyRequests && !transientCodes[ae.Code]
}

// request 以 token 的身份调一个 Web API 方法(表单编码的 POST,Slack 所有方法都收)。ok:false、
// HTTP 非 2xx、响应解析不了都是 *apiError,出站校验拒绝是 errBadBase。
func (a *App) request(ctx context.Context, method, token string, form url.Values) ([]byte, error) {
	if err := a.validate(a.base); err != nil {
		// DNS 解析失败可能只是一时的网络问题,照常重试;其余(不是 https、解析到内网)是配置错。
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", errBadBase, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, a.scrub(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		OK     *bool  `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
		Meta   struct {
			Messages []string `json:"messages"`
		} `json:"response_metadata"`
	}
	if json.Unmarshal(raw, &env) != nil {
		// 5xx 回 HTML 多半是 Slack 那头临时出错;5xx 以下还不是 JSON(200 的页面、3xx 重定向),就是
		// 地址指错了地方。不把响应体带进错误:那里面什么都可能有。
		msg := "响应无法解析"
		if resp.StatusCode < 500 {
			msg = "响应无法解析,检查 API 地址"
		}
		return nil, &apiError{Method: method, HTTP: resp.StatusCode, Msg: msg}
	}
	if env.OK == nil && resp.StatusCode < 300 {
		// 2xx 的 JSON 里没有 ok:不是 Slack Web API(地址填成了别的会回 JSON 的服务)。当暂时错误会
		// 无限重试且 LastError 为空,按永久错误报给设置页。
		return nil, &apiError{Method: method, HTTP: resp.StatusCode, Msg: "响应里没有 ok 字段,检查 API 地址"}
	}
	if resp.StatusCode >= 300 || env.OK == nil || !*env.OK {
		msg := strings.Join(env.Meta.Messages, "; ")
		if env.Needed != "" {
			msg = "缺少权限 " + env.Needed
		}
		return nil, &apiError{Method: method, HTTP: resp.StatusCode, Code: env.Error, Msg: a.scrub(errors.New(msg)).Error()}
	}
	return raw, nil
}

// scrub 把三个密钥从错误文本里换掉。token 只在请求头里,正常不会出现在错误里 —— 这是防一个往错误里
// 回显请求的代理。
func (a *App) scrub(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, r := range [][2]string{{a.botToken, "<bot-token>"}, {a.appToken, "<app-token>"}, {a.signingSecret, "<signing-secret>"}} {
		if r[0] != "" {
			s = strings.ReplaceAll(s, r[0], r[1])
		}
	}
	return errors.New(s)
}

// authTest 验证 Bot Token,查机器人用户名(绑定弹窗用它告诉用户私聊谁)与机器人自己的成员 ID
// (私聊里忽略自己发的消息)。没有 bot_id 说明填的是用户 token:拿它发的卡片会以那个人的名义出现。
func (a *App) authTest(ctx context.Context) (botInfo, error) {
	raw, err := a.request(ctx, "auth.test", a.botToken, nil)
	if err != nil {
		return botInfo{}, err
	}
	var r struct {
		User   string `json:"user"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.BotID == "" {
		return botInfo{}, &apiError{Method: "auth.test", HTTP: http.StatusOK, Code: "not_a_bot_token", Msg: "Bot User OAuth Token 应以 xoxb- 开头"}
	}
	return botInfo{name: r.User, userID: r.UserID}, nil
}

// openDM 打开(或取回)机器人与 userID 的私聊频道。
func (a *App) openDM(ctx context.Context, userID string) (string, error) {
	raw, err := a.request(ctx, "conversations.open", a.botToken, url.Values{"users": {userID}})
	if err != nil {
		return "", err
	}
	var r struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if json.Unmarshal(raw, &r) != nil || !dmRe.MatchString(r.Channel.ID) {
		return "", &apiError{Method: "conversations.open", HTTP: http.StatusOK, Msg: "响应里没有私聊频道,检查 API 地址"}
	}
	return r.Channel.ID, nil
}

// post 发一条消息,返回它的 ts。不展开链接预览:控制台地址是内网的,Slack 去抓也抓不到。
func (a *App) post(ctx context.Context, channel, text string, blocks []any) (string, error) {
	form := url.Values{"channel": {channel}, "text": {text}, "unfurl_links": {"false"}, "unfurl_media": {"false"}}
	if blocks != nil {
		b, err := json.Marshal(blocks)
		if err != nil {
			return "", err
		}
		form.Set("blocks", string(b))
	}
	raw, err := a.request(ctx, "chat.postMessage", a.botToken, form)
	if err != nil {
		return "", err
	}
	var r struct {
		TS string `json:"ts"`
	}
	if json.Unmarshal(raw, &r) != nil || !tsRe.MatchString(r.TS) {
		return "", &apiError{Method: "chat.postMessage", HTTP: http.StatusOK, Msg: "响应里没有消息 ts,检查 API 地址"}
	}
	return r.TS, nil
}

// sendText 往一个私聊里发一条纯文本(绑定结果的回复)。
func (a *App) sendText(ctx context.Context, channel, text string) error {
	_, err := a.post(ctx, channel, mrkdwnEscape(text), nil)
	return err
}

// postEphemeral 发一条只有 userID 看得见的消息:Slack 的按钮点击没有 toast,点击结果用它告诉点击者。
func (a *App) postEphemeral(ctx context.Context, channel, userID, text string) error {
	_, err := a.request(ctx, "chat.postEphemeral", a.botToken, url.Values{
		"channel": {channel}, "user": {userID}, "text": {mrkdwnEscape(text)},
	})
	return err
}

// SendCard 私信一张审批卡片。ref 是「私聊频道|ts|单号」:收卡要前两者,单号给取不到原卡片时用。
func (a *App) SendCard(ctx context.Context, userID string, c imchannel.ApprovalCard) (string, error) {
	if !userIDRe.MatchString(userID) {
		return "", fmt.Errorf("无法识别的 Slack 成员 ID %q", userID)
	}
	if !apNoRe.MatchString(c.ApNo) {
		return "", fmt.Errorf("无法用作卡片标识的单号 %q", c.ApNo)
	}
	ch, err := a.openDM(ctx, userID)
	if err != nil {
		return "", err
	}
	ts, err := a.post(ctx, ch, cardText(c), cardBlocks(c))
	if err != nil {
		return "", err
	}
	return cardRef(ch, ts, c.ApNo), nil
}

// CloseCard 把卡片上的按钮换成终态,其余原样保留(取不到原卡片时只留单号与终态)。
func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	ch, ts, apNo, ok := parseRef(ref)
	if !ok {
		return fmt.Errorf("无法识别的卡片引用 %q", ref)
	}
	text := closedText(apNo, final)
	err := a.update(ctx, ch, ts, text, closedBlocks(a.history(ctx, ch, ts), apNo, final))
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "invalid_blocks" {
		// Slack 不收它自己吐出来的原卡片(blocks 的格式变过):退回只留单号与终态 —— 按钮总得去掉。
		err = a.update(ctx, ch, ts, text, closedBlocks(nil, apNo, final))
	}
	if errors.As(err, &ae) && ae.Code == "message_not_found" {
		// 对方把消息删了:这张卡片不会再有能点的按钮了,算收起。
		return nil
	}
	return err
}

func (a *App) update(ctx context.Context, ch, ts, text string, blocks []any) error {
	b, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	_, err = a.request(ctx, "chat.update", a.botToken, url.Values{"channel": {ch}, "ts": {ts}, "text": {text}, "blocks": {string(b)}})
	return err
}

// history 取回卡片原来的 blocks。取不到(没给 im:history 权限、消息已删)返回 nil,收卡退回替代卡片。
func (a *App) history(ctx context.Context, ch, ts string) []json.RawMessage {
	raw, err := a.request(ctx, "conversations.history", a.botToken, url.Values{
		"channel": {ch}, "latest": {ts}, "inclusive": {"true"}, "limit": {"1"},
	})
	if err != nil {
		slog.Warn("slack: reading the card back failed; closing it without its original text", "err", err)
		return nil
	}
	var r struct {
		Messages []struct {
			TS     string            `json:"ts"`
			Blocks []json.RawMessage `json:"blocks"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	for _, m := range r.Messages {
		if m.TS == ts {
			return m.Blocks
		}
	}
	return nil
}
