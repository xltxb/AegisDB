// Package lark 是飞书 / Lark 渠道:企业自建应用 + 官方 SDK 的长连接(收绑定码与卡片点击),加三个服务端
// 接口(换令牌、发卡片、改卡片)。不需要公网入口。
//
// 只认私聊。文字消息看事件里的 chat_type;卡片点击的回调里没有会话类型,只能要求来自消息里的卡片
// (host = im_message):卡片只私聊发给本人、不许转发,真到了别人手里,令牌与「点的人就是这张卡片的收件人」
// 两样由 service 核对。自建应用只收得到本企业的事件,所以不看 tenant_key。
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"aegisdb/internal/chat"
)

const (
	maxTokenLen = 128 // 按钮值里的令牌;真的只有 32 个字符,形状由 service 核对

	// 飞书要求 3 秒内应答点击。
	answerWithin = 2500 * time.Millisecond
	// Run 返回前等还在处理的点击与文字这么久;再久的让它自己做完。
	drainWithin = 3 * time.Second

	// 没收到应答的事件,飞书在 15 秒、5 分钟、1 小时、6 小时后各重推一次。
	seenFor = 8 * time.Hour
	seenMax = 4096
)

var _ chat.Transport = (*Transport)(nil)

// Transport 是一个飞书 / Lark 机器人。一份配置一个;同一时刻只能有一个 Run。
type Transport struct {
	appID, appSecret string
	base             string // scheme://host
	wsSuffix         string // 长连接主机必须以它结尾;空 = 测试地址,只认 base 那个主机
	lang             func() string
	http             *http.Client
	scrub            *scrubber
	answerWithin     time.Duration

	tokenLock  chan struct{}
	token      string
	tokenUntil time.Time

	seen          *seen
	texts, clicks chan struct{} // 同时在处理的条数

	events *dispatcher.EventDispatcher
	cur    atomic.Pointer[running]

	clientMu sync.Mutex
	client   *larkws.Client
}

// running 是一次 Run:事件交给谁、用哪个 ctx。
type running struct {
	ctx context.Context
	h   chat.Handler
	wg  sync.WaitGroup
}

// Option 调整一个 Transport。
type Option func(*Transport)

// WithBaseURL 换掉平台地址,只给测试用:同时把长连接限制在这个主机上,并放行 ws://。
func WithBaseURL(base string) Option {
	return func(t *Transport) { t.base, t.wsSuffix = strings.TrimRight(base, "/"), "" }
}

// WithLang 给出卡片语言(chat.cardLang),用在渠道自己要说的那几句上;不给就是 zh。它不许阻塞:赶着 3 秒应答时也要调它。
func WithLang(lang func() string) Option {
	return func(t *Transport) { t.lang = lang }
}

// New 建一个渠道。region 是 feishu(国内版)或 lark(国际版)。
func New(region, appID, appSecret string, opts ...Option) *Transport {
	t := &Transport{
		appID: appID, appSecret: appSecret,
		base: "https://open.feishu.cn", wsSuffix: ".feishu.cn",
		lang:         func() string { return "" },
		scrub:        &scrubber{fixed: []string{appSecret}},
		answerWithin: answerWithin,
		tokenLock:    make(chan struct{}, 1),
		seen:         newSeen(seenMax, seenFor),
		texts:        make(chan struct{}, 16),
		clicks:       make(chan struct{}, 32),
	}
	if region == "lark" {
		t.base, t.wsSuffix = "https://open.larksuite.com", ".larksuite.com"
	}
	for _, o := range opts {
		o(t)
	}
	t.http = newHTTPClient(t.base)
	// 长连接里的事件不加密、不签名(连接本身已按应用密钥认证),所以两个参数都空着。
	// 构造时 SDK 会往标准输出写一行「event-dispatch is ready」,那时还换不了它的日志;每个 Transport 一次。
	t.events = dispatcher.NewEventDispatcher("", "")
	t.events.Config.Logger = sdkLog{t.scrub}
	t.events.OnP2MessageReceiveV1(func(_ context.Context, e *larkim.P2MessageReceiveV1) error { return t.onText(e) })
	t.events.OnP2CardActionTrigger(func(_ context.Context, e *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		return t.onClick(e), nil
	})
	return t
}

// BotID 是应用的 App ID。
func (t *Transport) BotID() string { return t.appID }

// BotName 不去查:多一个接口、多一项权限,界面上用 App ID 就够了。
func (t *Transport) BotName() string { return "" }

// ---------------------------------------------------------------- 连接

// Run 连上长连接并收事件,断开就返回,不自己重连(SDK 的自动重连关着)。ctx 取消时返回 ctx 的错误。
// 连接数超限先报 StateConflict 再返回;其余失败只在返回的错误里,由调用方报 StateError。
func (t *Transport) Run(ctx context.Context, h chat.Handler, onState func(chat.State, string)) error {
	onState(chat.StateConnecting, "")
	run := &running{ctx: ctx, h: h}
	t.cur.Store(run)

	cli := t.wsClient()
	var connected atomic.Bool
	cli.SetOnReady(func() {
		connected.Store(true)
		onState(chat.StateConnected, "")
	})
	returned := false
	defer func() {
		if !returned { // panic 了:SDK 停在「已在运行」,接着用就再也连不上
			t.dropClient()
		}
	}()
	err := cli.Start(ctx)
	returned = true
	if connected.Load() || ctx.Err() != nil {
		t.dropClient() // 连上过或被取消的,SDK 不许再用
	}
	run.drain()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		err = errors.New("closed")
	}
	clean := t.scrub.wrap("connection", err)
	// SDK 只在「连接数超限」时把 514 报成 ClientError,别的鉴权失败是 ServerError。
	var ce *larkws.ClientError
	if errors.As(err, &ce) && ce.Code == larkws.AuthFailed {
		onState(chat.StateConflict, clean.Error())
	}
	return clean
}

// wsClient 取一个 SDK 客户端。没连上过的接着用:SDK 每建一个就留下一个停不掉的缓存清理协程(它的
// 终结器等的对象被那个协程自己引用着),连不上时反复重连不该越积越多。
func (t *Transport) wsClient() *larkws.Client {
	t.clientMu.Lock()
	defer t.clientMu.Unlock()
	if t.client == nil {
		t.client = larkws.NewClient(t.appID, t.appSecret,
			larkws.WithDomain(t.base),
			larkws.WithAutoReconnect(false),
			larkws.WithLogger(sdkLog{t.scrub}),
			larkws.WithHttpClient(bootstrap{t}),
			larkws.WithWebSocketDialer(&websocket.Dialer{
				HandshakeTimeout: apiTimeout,
				NetDialContext:   t.dialWS,
			}),
			larkws.WithEventHandler(t.events))
	}
	return t.client
}

func (t *Transport) dropClient() {
	t.clientMu.Lock()
	defer t.clientMu.Unlock()
	t.client = nil
}

// dialWS 建长连接的 TCP 连接,Run 的 ctx 一取消就关掉它:gorilla 在握手途中不看 ctx,取消要干等到握手超时。
func (t *Transport) dialWS(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{Timeout: apiTimeout}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	run := t.cur.Load()
	return &cancelConn{Conn: c, stop: context.AfterFunc(run.ctx, func() { _ = c.Close() })}, nil
}

type cancelConn struct {
	net.Conn
	stop func() bool
}

func (c *cancelConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

// drain 等这次 Run 里还在处理的做完,有上限。
func (r *running) drain() {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainWithin):
	}
}

// bootstrap 是 SDK 取长连接地址用的客户端:出站与别的接口同一个,另外在 SDK 看到应答之前核对地址。
type bootstrap struct{ t *Transport }

func (b bootstrap) Do(req *http.Request) (*http.Response, error) {
	resp, err := b.t.http.Do(req)
	if err != nil {
		return nil, b.t.scrub.wrap("bootstrap", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return nil, b.t.scrub.wrap("bootstrap", err)
	}
	if resp.StatusCode == http.StatusOK {
		var ep larkws.EndpointResp
		if json.Unmarshal(raw, &ep) != nil {
			return nil, errors.New("unexpected bootstrap response")
		}
		if ep.Code == larkws.OK && ep.Data != nil {
			u, err := b.t.checkEndpoint(ep.Data.Url)
			if err != nil {
				return nil, err
			}
			for _, vs := range u.Query() {
				for _, v := range vs {
					if len(v) >= 8 { // 票据;短的是设备号一类,遮了反而看不懂
						b.t.scrub.ticket(v)
					}
				}
			}
			if c := ep.Data.ClientConfig; c != nil {
				// 间隔为 0 时 SDK 的心跳会空转,过大则断了也察觉不到。
				c.PingInterval = min(max(c.PingInterval, 10), 300)
			}
			if raw, err = json.Marshal(ep); err != nil {
				return nil, errors.New("unexpected bootstrap response")
			}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return resp, nil
}

// checkEndpoint 核对平台下发的长连接地址:必须是 wss,主机在这个地区的域名下。地址里有票据,错误里只写主机。
func (t *Transport) checkEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("invalid websocket endpoint")
	}
	ok := u.Scheme == "wss" && strings.HasSuffix(strings.ToLower(u.Hostname()), t.wsSuffix)
	if t.wsSuffix == "" {
		base, err := url.Parse(t.base)
		ok = err == nil && u.Host == base.Host
	}
	if !ok {
		return nil, fmt.Errorf("websocket endpoint %s://%s is not allowed", u.Scheme, u.Host)
	}
	return u, nil
}

// sdkLog 把 SDK 的日志接到 slog:警告与错误都记成 Warn 并去密钥,其余不要(Debug 会写出整条事件)。
type sdkLog struct{ scrub *scrubber }

var _ larkcore.Logger = sdkLog{}

func (sdkLog) Debug(context.Context, ...any) {}
func (sdkLog) Info(context.Context, ...any)  {}
func (l sdkLog) Warn(_ context.Context, args ...any) {
	slog.Warn("Lark SDK", "msg", l.scrub.text(fmt.Sprint(args...)))
}
func (l sdkLog) Error(ctx context.Context, args ...any) { l.Warn(ctx, args...) }

// ---------------------------------------------------------------- 文字

var errBusy = errors.New("busy")

// onText 处理一条消息事件。先应答、后处理:不应答飞书会重推。忙不过来时返回错误,正是要它重推。
func (t *Transport) onText(e *larkim.P2MessageReceiveV1) error {
	run := t.cur.Load()
	if run == nil || e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil || e.Event.Sender.SenderId == nil || !t.mine(e.EventV2Base) {
		return nil
	}
	m, from := e.Event.Message, e.Event.Sender
	id, chatID, account := str(m.MessageId), str(m.ChatId), str(from.SenderId.OpenId)
	if str(m.ChatType) != "p2p" || str(m.MessageType) != "text" || str(from.SenderType) != "user" || id == "" || chatID == "" || account == "" {
		return nil
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(str(m.Content)), &content) != nil || strings.TrimSpace(content.Text) == "" {
		return nil
	}
	select {
	case t.texts <- struct{}{}:
	default:
		return errBusy
	}
	// 认重放要在占到位子之后:没占到的那次不算处理过。
	if t.seen.again(id) {
		<-t.texts
		return nil
	}
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		defer func() { <-t.texts }()
		reply := t.safeText(run, account, chatID, content.Text)
		if reply == "" {
			return
		}
		if err := t.reply(run.ctx, chatID, reply); err != nil {
			slog.Warn("Lark 回复失败", "err", err)
		}
	}()
	return nil
}

func (t *Transport) safeText(run *running, account, chatID, text string) (reply string) {
	defer func() {
		if recover() != nil {
			logPanic()
			reply = t.say("bind.failed")
		}
	}()
	return run.h.OnText(run.ctx, chat.Lark, t.appID, account, chatID, text)
}

// ---------------------------------------------------------------- 点击

// onClick 处理一次卡片点击。3 秒内必须应答,来不及就先答「正在处理」,让它在后台做完,卡片由扫描收敛。
//
// 处理用 Run 的 ctx,不用 SDK 给的:断线会取消后者,而一个已经开始的决定不该因为断线半途而废。
func (t *Transport) onClick(e *callback.CardActionTriggerEvent) *callback.CardActionTriggerResponse {
	run := t.cur.Load()
	c, ok := t.parseClick(e)
	if run == nil || !ok {
		return t.toast("error", t.say("click.invalid"))
	}
	select {
	case t.clicks <- struct{}{}:
	default:
		return t.toast("error", t.say("click.retry"))
	}
	done := make(chan chat.ClickResult, 1)
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		defer func() { <-t.clicks }()
		defer func() {
			if recover() != nil {
				logPanic()
				done <- chat.ClickResult{Toast: t.say("click.retry")}
			}
		}()
		done <- run.h.OnClick(run.ctx, c)
	}()
	timer := time.NewTimer(t.answerWithin)
	defer timer.Stop()
	select {
	case res := <-done:
		kind := "error"
		if res.OK {
			kind = "success"
		}
		out := t.toast(kind, res.Toast)
		if res.Card != nil {
			out.Card = &callback.Card{Type: "raw", Data: json.RawMessage(RenderCard(*res.Card))}
		}
		return out
	case <-timer.C:
		return t.toast("info", t.say("click.processing"))
	}
}

// parseClick 把回调变成一次点击。身份只取自 operator.open_id;按钮的值里只认令牌和动作,多一样都不收。
//
// 表单提交的回调带不带按钮的 value,没有在真平台上验证过;不带的话「拒绝」就永远点不动。所以没有值时从
// 按钮的名字里认(见 rejectName)。只认拒绝:表单提交按钮只有它一个,通过从不走这条路。
func (t *Transport) parseClick(e *callback.CardActionTriggerEvent) (chat.Click, bool) {
	ev := e.Event
	if ev == nil || !t.mine(e.EventV2Base) || ev.Host != "im_message" || ev.Operator == nil || ev.Operator.OpenID == "" ||
		ev.Action == nil || ev.Context == nil || ev.Context.OpenMessageID == "" || ev.Context.OpenChatID == "" {
		return chat.Click{}, false
	}
	value := ev.Action.Value
	if m := rejectNameShape.FindStringSubmatch(ev.Action.Name); len(value) == 0 && m != nil {
		value = obj{"t": m[1], "a": string(chat.ActReject)}
	}
	token, ok1 := value["t"].(string)
	action, ok2 := value["a"].(string)
	if !ok1 || !ok2 || len(value) != 2 || len(token) > maxTokenLen {
		return chat.Click{}, false
	}
	c := chat.Click{Channel: chat.Lark, BotID: t.appID, AccountID: ev.Operator.OpenID, Token: token, Action: chat.Action(action)}
	switch c.Action {
	case chat.ActReject:
		reason, _ := ev.Action.FormValue[reasonInput].(string)
		reason = strings.TrimSpace(strings.ToValidUTF8(reason, ""))
		if r := []rune(reason); len(r) > maxRejectRunes {
			reason = string(r[:maxRejectRunes])
		}
		c.Reason = reason
	case chat.ActApprove, chat.ActConfirm, chat.ActBack, chat.ActTest:
	default:
		return chat.Click{}, false
	}
	return c, true
}

func (t *Transport) toast(kind, text string) *callback.CardActionTriggerResponse {
	out := &callback.CardActionTriggerResponse{}
	if text != "" {
		out.Toast = &callback.Toast{Type: kind, Content: text}
	}
	return out
}

// ---------------------------------------------------------------- 零碎

func (t *Transport) say(key string) string { return chat.Tr(t.lang(), key) }

// mine 报告事件是不是这个应用的。
func (t *Transport) mine(e *larkevent.EventV2Base) bool {
	return e != nil && e.Header != nil && e.Header.AppID == t.appID
}

// logPanic 只记调用栈,不记 panic 的内容:里面可能有密钥。
func logPanic() { slog.Error("Lark 事件处理 panic", "stack", string(debug.Stack())) }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// seen 记住近期处理过的消息号,认出重推的。有界:满了先忘最早的。
type seen struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	now   func() time.Time
	at    map[string]time.Time
	order []string // 按先后
}

func newSeen(max int, ttl time.Duration) *seen {
	return &seen{max: max, ttl: ttl, now: time.Now, at: map[string]time.Time{}}
}

// again 记下一个号,报告它是不是已经见过。
func (s *seen) again(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for len(s.order) > 0 && now.Sub(s.at[s.order[0]]) >= s.ttl {
		s.forget()
	}
	if _, ok := s.at[id]; ok {
		return true
	}
	if len(s.order) >= s.max {
		s.forget()
	}
	s.at[id] = now
	s.order = append(s.order, id)
	return false
}

func (s *seen) forget() {
	delete(s.at, s.order[0])
	s.order = s.order[1:]
}
