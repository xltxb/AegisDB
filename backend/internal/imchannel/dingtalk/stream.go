package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/card"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"velagateway/internal/imchannel"
)

// Stream 协议自己实现,只借官方 SDK 的数据类型:SDK 的 client 写死默认拨号器(绕开 SSRF 拦截)、
// 在 Debug 级把含 clientSecret 的请求体打进全局 logger、断线重连跑在 ctx 管不到的循环里(Reload
// 之后旧连接不退出,继续分走事件)。见 spec §21。
const (
	typeSystem      = "SYSTEM"
	typeCallback    = "CALLBACK"
	topicPing       = "ping"
	topicDisconnect = "disconnect"

	userAgent   = "aegisdb-gateway (dingtalk-stream)"
	frameLimit  = 1 << 20          // 单帧上限:一条超长私聊不该撑爆内存
	pingEvery   = 30 * time.Second // WebSocket ping 间隔
	readTimeout = 90 * time.Second // 这么久什么都没收到(帧或 pong)就当连接半开,重连
	stableAfter = time.Minute      // 连上超过这么久再断,退避从头算
)

// errReconnect:钉钉发来 SYSTEM disconnect,要求换一条连接(服务端发布、负载迁移)。不是故障。
var errReconnect = errors.New("钉钉要求重连")

// Run 阻塞到 ctx 取消。纯配置错(没填模板 ID、拿不到受控拨号器)先查,不等网络;然后换一次
// access token —— 凭据错在这里就停下、交给设置页;最后维持 Stream 长连接。
func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error {
	if a.template == "" {
		return errNoTemplate
	}
	if _, err := imchannel.GuardedDial(a.client); err != nil {
		return err
	}
	err := imchannel.Retry(ctx, "dingtalk", permanent, func() error {
		_, err := a.token(ctx)
		return err
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	slog.Info("dingtalk: stream connection starting — safe with several gateway instances; DingTalk delivers each event to one of them")
	return a.stream(ctx, sink)
}

// stream 一条连接断了就换下一条,直到 ctx 取消或遇到永久性错误。
func (a *App) stream(ctx context.Context, sink imchannel.ActionSink) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := a.session(ctx, sink)
		if ctx.Err() != nil {
			return nil
		}
		if permanent(err) {
			return err
		}
		if errors.Is(err, errReconnect) || time.Since(started) > stableAfter {
			backoff = time.Second
		}
		if errors.Is(err, errReconnect) {
			slog.Info("dingtalk: server asked to reconnect")
		} else {
			slog.Warn("dingtalk: stream connection lost, reconnecting", "err", err, "retryIn", backoff)
		}
		if !imchannel.Sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// open 换一个长连接地址(带一次性 ticket)。
func (a *App) open(ctx context.Context) (string, error) {
	raw, err := a.request(ctx, http.MethodPost, pathOpen, "", payload.ConnectionEndpointRequest{
		ClientId: a.clientID, ClientSecret: a.clientSecret, UserAgent: userAgent,
		// 不报 localIp:钉钉不需要它,网关的内网拓扑也不该交出去。
		Subscriptions: []*payload.SubscriptionModel{
			{Type: typeSystem, Topic: topicPing},
			{Type: typeSystem, Topic: topicDisconnect},
			{Type: typeCallback, Topic: payload.BotMessageCallbackTopic},
			{Type: typeCallback, Topic: payload.CardInstanceCallbackTopic},
		},
	})
	if err != nil {
		return "", err
	}
	// 2xx 却给不出能用的地址:多半是开放平台地址指错了地方,重试也一样 —— 按 errBadBase 停下,
	// 交给设置页,别在 WARN 里无限重连。
	var ep payload.ConnectionEndpointResponse
	if json.Unmarshal(raw, &ep) != nil || ep.Valid() != nil {
		return "", fmt.Errorf("%w: 钉钉没有返回长连接地址", errBadBase)
	}
	u, err := url.Parse(ep.Endpoint)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		return "", fmt.Errorf("%w: 钉钉返回的长连接地址无法使用: %q", errBadBase, ep.Endpoint)
	}
	// 长连接地址是钉钉下发的,不是管理员填的;仍过一遍出站校验(wss 按 https、ws 按 http 查),
	// 拨号本身也走受控拨号器 —— 两道都拦内网。
	check := url.URL{Scheme: "https", Host: u.Host}
	if u.Scheme == "ws" {
		check.Scheme = "http"
	}
	if err := a.validate(check.String()); err != nil {
		// 同 request:DNS 解析失败可能是一时的,照常重试;其余(解析到内网等)是出站校验拒绝,永久性。
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return "", err
		}
		return "", fmt.Errorf("%w: 长连接地址 %v", errBadBase, err)
	}
	q := u.Query()
	q.Set("ticket", ep.Ticket)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// session 是一条连接的一生:拨号、收帧、分发,直到断开、钉钉要求重连或 ctx 取消。
func (a *App) session(ctx context.Context, sink imchannel.ActionSink) error {
	dial, err := imchannel.GuardedDial(a.client)
	if err != nil {
		return err
	}
	endpoint, err := a.open(ctx)
	if err != nil {
		return err
	}
	d := websocket.Dialer{NetDialContext: dial, HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.DialContext(ctx, endpoint, nil)
	if err != nil {
		return a.scrub(err)
	}
	defer conn.Close()
	conn.SetReadLimit(frameLimit)
	// ctx 取消(Reload)时立刻关连接,ReadMessage 随之返回 —— 旧连接不许继续收事件。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	alive := func() { _ = conn.SetReadDeadline(time.Now().Add(readTimeout)) }
	alive()
	conn.SetPongHandler(func(string) error { alive(); return nil })
	done := make(chan struct{})
	defer close(done)
	go keepAlive(conn, done)

	w := &frameWriter{conn: conn}
	var handlers sync.WaitGroup
	defer handlers.Wait() // 先等处理中的回调写完回执,再关连接
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		alive()
		var df payload.DataFrame
		if json.Unmarshal(msg, &df) != nil {
			continue
		}
		switch {
		case df.Type == typeSystem && df.GetTopic() == topicPing:
			pong := payload.NewDataFrameAckPong(df.GetMessageId())
			pong.Data = df.Data
			w.send(pong)
		case df.Type == typeSystem && df.GetTopic() == topicDisconnect:
			return errReconnect
		case df.Type == typeCallback:
			// 处理要查库、可能还要回一条私聊:放到协程里,别让一条慢回调挡住后面的帧和 ping。
			handlers.Add(1)
			go func(df payload.DataFrame) {
				defer handlers.Done()
				defer func() {
					if p := recover(); p != nil {
						slog.Error("dingtalk: callback handler panicked", "topic", df.GetTopic(), "panic", p)
						// 照样回执:不回的话钉钉会重推同一帧,再 panic 一次。
						w.send(fallbackAck(&df))
					}
				}()
				w.send(a.handle(ctx, sink, &df))
			}(df)
		default:
			w.send(ack(&df, payload.DataFrameResponseStatusCodeKHandlerNotFound, ""))
		}
	}
}

// keepAlive 定时发 WebSocket ping;钉钉回的 pong 把 read deadline 往后推。
func keepAlive(conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

// frameWriter 串行化回执的写入:gorilla 的 Conn 同一时刻只许一个写者。
type frameWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *frameWriter) send(r *payload.DataFrameResponse) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := w.conn.WriteJSON(r); err != nil {
		slog.Warn("dingtalk: stream reply failed", "err", err)
	}
}

// ack 是对一帧的回执:带上它的 messageId,钉钉据此认定投递成功、不再重推。
func ack(df *payload.DataFrame, code int, data string) *payload.DataFrameResponse {
	r := payload.NewDataFrameResponse(code)
	r.SetHeader(payload.DataFrameHeaderKMessageId, df.GetMessageId())
	r.SetHeader(payload.DataFrameHeaderKContentType, payload.DataFrameContentTypeKJson)
	r.SetData(data)
	return r
}

// fallbackAck 是处理出错时的回执:卡片回调照常回「无法识别的操作」,其余只确认收到。
func fallbackAck(df *payload.DataFrame) *payload.DataFrameResponse {
	if df.GetTopic() == payload.CardInstanceCallbackTopic {
		return tipAck(df, "无法识别的操作")
	}
	return ack(df, payload.DataFrameResponseStatusCodeKOK, "")
}

// tipAck 是卡片回调的应答:把 tip 写回卡片(只改这个变量)。
func tipAck(df *payload.DataFrame, tip string) *payload.DataFrameResponse {
	b, _ := json.Marshal(map[string]any{"response": card.CardResponse{
		CardUpdateOptions: &card.CardUpdateOptions{UpdateCardDataByKey: true},
		CardData:          &card.CardDataDto{CardParamMap: map[string]string{paramTip: tip}},
	}})
	return ack(df, payload.DataFrameResponseStatusCodeKOK, string(b))
}

func (a *App) handle(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) *payload.DataFrameResponse {
	switch df.GetTopic() {
	case payload.CardInstanceCallbackTopic:
		return a.onCard(ctx, sink, df)
	case payload.BotMessageCallbackTopic:
		a.onMessage(ctx, sink, df)
		return ack(df, payload.DataFrameResponseStatusCodeKOK, "")
	}
	return ack(df, payload.DataFrameResponseStatusCodeKHandlerNotFound, "")
}

// onCard 处理一次按钮点击。认人只凭回调里的 userId —— 它由钉钉在已认证的长连接上给出,是真正的
// 点击者;对应哪个网关账户、能不能批,都是 sink 那一侧的事。提示语写回卡片的 tip 变量。
func (a *App) onCard(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) *payload.DataFrameResponse {
	tip := "无法识别的操作"
	var req card.CardRequest
	if json.Unmarshal([]byte(df.Data), &req) == nil {
		_ = json.Unmarshal([]byte(req.Content), &req.CardActionData)
		apNo, ok := trackApNo(req.OutTrackId)
		act := req.GetActionString("act")
		// userIdType 2 是 unionId:绑定表里存的是 userid,两者不能混着认。
		if ok && (act == "a" || act == "r") && req.UserId != "" && req.UserIdType != 2 {
			tip = sink.OnAction(ctx, imchannel.Action{PlatformUserID: req.UserId, ApNo: apNo, Approve: act == "a"}).Toast
		}
	}
	return tipAck(df, tip)
}

// onMessage 只理组织内用户发来的单聊文本。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是
// 泄露了,更不能认。
func (a *App) onMessage(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) {
	var m chatbot.BotCallbackDataModel
	if json.Unmarshal([]byte(df.Data), &m) != nil {
		return
	}
	// conversationType "1" 是单聊;没有 senderStaffId 的是组织外的人(外部联系人)。
	if m.ConversationType != "1" || m.Msgtype != "text" || m.SenderStaffId == "" || !a.seen.First(m.MsgId) {
		return
	}
	text := strings.TrimSpace(m.Text.Content)
	if text == "" {
		return
	}
	reply := sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: m.SenderStaffId, Text: text})
	if reply == "" {
		return
	}
	// 回复走机器人单聊接口(受控出站客户端),不用事件里带来的 sessionWebhook —— 那是一个从事件
	// 正文里拿到的 URL。绑定已经落库,回复不能因为 ctx 刚好被取消(Reload)而丢掉。
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.sendText(ackCtx, m.SenderStaffId, reply); err != nil {
		slog.Warn("dingtalk: reply to direct message failed", "err", err)
	}
}
