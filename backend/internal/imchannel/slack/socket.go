package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"velagateway/internal/imchannel"
)

// Socket Mode 协议自己实现,不用 slack-go:它的重连循环只把四个鉴权错误码当致命,token 类型填反、
// 缺权限、API 地址填错都会被无限重试,设置页上看不到原因。见 spec §22。
const (
	frameLimit  = 1 << 20          // 单帧上限:一条超长私聊不该撑爆内存
	pingEvery   = 30 * time.Second // WebSocket ping 间隔(Slack 自己也会定时 ping 过来)
	readTimeout = 90 * time.Second // 这么久什么都没收到(帧、ping 或 pong)就当连接半开,重连
	stableAfter = time.Minute      // 连上超过这么久再断,退避从头算
)

var (
	errNoAppToken      = errors.New("Socket Mode 需要配置 App-Level Token(xapp-…,带 connections:write 权限)")
	errNoSigningSecret = errors.New("webhook 模式需要配置 Signing Secret")
	// errReconnect:Slack 发来 disconnect,要求换一条连接(定期刷新、服务端发布)。不是故障。
	errReconnect = errors.New("Slack 要求重连")
	// errLinkDisabled:应用后台关掉了 Socket Mode。重连只会被同样拒绝。
	errLinkDisabled = errors.New("Slack 应用已关闭 Socket Mode:在应用后台 › Socket Mode 打开,或改用 webhook 模式")
)

// envelope 是 Socket Mode 的一帧:hello / disconnect,或带 envelope_id 的事件信封。
type envelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
	Reason     string          `json:"reason"`
}

// Run 阻塞到 ctx 取消。纯配置错(缺 App Token / Signing Secret、拿不到受控拨号器)先查,不等网络;
// 然后 auth.test —— Bot Token 错在这里就停下、交给设置页,顺带拿到机器人名;最后 Socket Mode 维持
// 长连接,webhook 模式只是待命(事件从 HandleWebhook 进来)。
func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error {
	if a.mode == modeWebhook {
		if a.signingSecret == "" {
			return errNoSigningSecret
		}
	} else {
		if a.appToken == "" {
			return errNoAppToken
		}
		if _, err := imchannel.GuardedDial(a.client); err != nil {
			return err
		}
	}
	var info botInfo
	err := imchannel.Retry(ctx, "slack", permanent, func() error {
		i, err := a.authTest(ctx)
		info = i
		return err
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	a.bot.Store(info)
	if a.mode == modeWebhook {
		slog.Info("slack: webhook mode — point both the interactivity and the event subscription request URL at the gateway", "path", WebhookPath)
		<-ctx.Done()
		return nil
	}
	slog.Info("slack: socket mode connection starting — safe with several gateway instances; Slack delivers each event to one of them")
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
			slog.Info("slack: server asked to reconnect")
		} else {
			slog.Warn("slack: socket mode connection lost, reconnecting", "err", err, "retryIn", backoff)
		}
		if !imchannel.Sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// open 换一个 Socket Mode 地址(带一次性 ticket)。
func (a *App) open(ctx context.Context) (string, error) {
	raw, err := a.request(ctx, "apps.connections.open", a.appToken, nil)
	if err != nil {
		return "", err
	}
	// 2xx 却给不出能用的地址:多半是 API 地址指错了地方,重试也一样 —— 按 errBadBase 停下。
	var r struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &r) != nil || r.URL == "" {
		return "", fmt.Errorf("%w: Slack 没有返回长连接地址", errBadBase)
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		// 不把地址带进错误:里面有 ticket。
		return "", fmt.Errorf("%w: Slack 返回的长连接地址无法使用", errBadBase)
	}
	// 长连接地址是 Slack 下发的,不是管理员填的;仍过一遍出站校验(wss 按 https、ws 按 http 查),
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
	return r.URL, nil
}

// session 是一条连接的一生:拨号、收信封、确认、分发,直到断开、Slack 要求重连或 ctx 取消。
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
	// Slack 定时发 WebSocket ping:收到就续期,再交给 gorilla 默认的处理器回 pong。
	pong := conn.PingHandler()
	conn.SetPingHandler(func(data string) error { alive(); return pong(data) })
	conn.SetPongHandler(func(string) error { alive(); return nil })
	done := make(chan struct{})
	defer close(done)
	go keepAlive(conn, done)

	w := &acker{conn: conn}
	var handlers sync.WaitGroup
	// 连接断开时等处理中的信封做完再返回:它们已经确认过,Slack 不会重投。
	defer handlers.Wait()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		alive()
		var env envelope
		if json.Unmarshal(msg, &env) != nil {
			continue
		}
		switch env.Type {
		case "hello":
			continue
		case "disconnect":
			if env.Reason == "link_disabled" {
				return errLinkDisabled
			}
			return errReconnect
		}
		if env.EnvelopeID == "" {
			continue
		}
		// 先确认再处理:Slack 要 3 秒内确认,而处理要查库、可能还要回一条消息。点击幂等、私聊按
		// event_id 去重,确认之后处理失败最多让用户再点一次。
		w.ack(env.EnvelopeID)
		handlers.Add(1)
		go func(env envelope) {
			defer handlers.Done()
			defer func() {
				if p := recover(); p != nil {
					slog.Error("slack: envelope handler panicked", "type", env.Type, "panic", p)
				}
			}()
			// 信封已确认,Slack 不会重投:处理不能跟着 Run 的 ctx 一起取消,否则 Reload 那一刻
			// 正在处理的点击就悄无声息地丢了。
			hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			switch env.Type {
			case "interactive":
				toast, channel, user := a.onInteractive(hctx, sink, env.Payload)
				a.tell(hctx, channel, user, toast)
			case "events_api":
				a.onEvent(hctx, sink, env.Payload)
			}
		}(env)
	}
}

// keepAlive 定时发 WebSocket ping;对方回的 pong 把 read deadline 往后推。
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

// acker 串行化确认的写入:gorilla 的 Conn 同一时刻只许一个写者。
type acker struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *acker) ack(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := w.conn.WriteJSON(map[string]string{"envelope_id": id}); err != nil {
		slog.Warn("slack: acknowledging an envelope failed", "err", err)
	}
}
