package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"velagateway/internal/imchannel"
)

// apNoRe:按钮回调可以被伪造(webhook 模式下尤其如此),别让任意字符串流进后面的查询和提示语。
var apNoRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

type sinkKey struct{}

// withSink 把 sink 放进 ctx。分发器在 newApp 里就建好了,而 sink 要到 Run / HandleWebhook
// 才拿得到 —— SDK 会把同一个 ctx 一路传给处理函数。
func withSink(ctx context.Context, s imchannel.ActionSink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

func sinkFrom(ctx context.Context) imchannel.ActionSink {
	s, _ := ctx.Value(sinkKey{}).(imchannel.ActionSink)
	return s
}

// Run 阻塞到 ctx 取消。先查应用名(顺带验证凭据与机器人能力:失败是永久的,返回让设置页
// 看见),然后长连接模式交给 SDK,webhook 模式只是待命 —— 事件从 HandleWebhook 进来。
//
// 纯配置错(webhook 模式缺密钥、长连接拿不到受控拨号器)先查,不等网络:飞书连不上时它们
// 也该立刻出现在设置页上,而不是藏在一串重试日志后面。
func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error {
	if a.mode == modeWebhook {
		if a.encryptKey == "" || a.verifyToken == "" {
			return errors.New("webhook 模式需要配置 Encrypt Key 与 Verification Token")
		}
	} else if _, err := imchannel.GuardedDial(a.client); err != nil {
		return err
	}
	var name string
	err := imchannel.Retry(ctx, "lark", permanent, func() error {
		n, err := a.botInfo(ctx)
		name = n
		return err
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	a.bot.Store(name)
	if a.mode == modeWebhook {
		slog.Info("lark: webhook mode — point both the event and the callback request URL at the gateway", "path", WebhookPath)
		<-ctx.Done()
		return nil
	}
	return a.stream(withSink(ctx, sink))
}

// stream 用官方 SDK 维持长连接。SDK 的两条出网路径都换成网关受控的:取连接地址走 a.client,
// WebSocket 拨号走同一个 Transport 的 DialContext。
func (a *App) stream(ctx context.Context) error {
	dial, err := imchannel.GuardedDial(a.client)
	if err != nil {
		return err
	}
	if err := a.validate(a.base); err != nil {
		return err
	}
	cli := larkws.NewClient(a.appID, a.appSecret,
		larkws.WithEventHandler(a.disp),
		larkws.WithDomain(a.base),
		larkws.WithHttpClient(a.client),
		larkws.WithWebSocketDialer(&websocket.Dialer{NetDialContext: dial, HandshakeTimeout: 10 * time.Second}),
		larkws.WithLogger(sdkLogger{}),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithAutoReconnect(true),
	)
	slog.Info("lark: long connection starting — safe with several gateway instances; Lark delivers each event to one of them")
	err = cli.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return a.scrub(err)
}

// newDispatcher 建事件分发器:私聊消息与卡片回调。签名与 Token 不在这里验 —— 长连接本身
// 已认证;webhook 由 HandleWebhook 自己验。
func (a *App) newDispatcher() *dispatcher.EventDispatcher {
	d := dispatcher.NewEventDispatcher("", "")
	d.Config.Logger = sdkLogger{}
	d.OnP2MessageReceiveV1(a.onMessage)
	d.OnP2CardActionTrigger(a.onCardAction)
	return d
}

func (a *App) onCardAction(ctx context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	toast := "无法识别的操作"
	sink := sinkFrom(ctx)
	if ev != nil && ev.Event != nil && ev.Event.Operator != nil && ev.Event.Action != nil && sink != nil {
		apNo, approve, ok := parseValue(ev.Event.Action.Value)
		if ok && ev.Event.Operator.OpenID != "" {
			// 认人只凭 operator.open_id —— 飞书保证它是真正的点击者。对应哪个网关账户、
			// 能不能批,都是 sink 那一侧的事。
			toast = sink.OnAction(ctx, imchannel.Action{
				PlatformUserID: ev.Event.Operator.OpenID, ApNo: apNo, Approve: approve,
			}).Toast
		}
	}
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: toast}}, nil
}

// parseValue 读按钮的 value:{"ap": "<ApNo>", "act": "a" | "r"}。
func parseValue(v map[string]interface{}) (apNo string, approve, ok bool) {
	ap, _ := v["ap"].(string)
	act, _ := v["act"].(string)
	if !apNoRe.MatchString(ap) || (act != "a" && act != "r") {
		return "", false, false
	}
	return ap, act == "a", true
}

func (a *App) onMessage(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil || ev.Event.Sender == nil || ev.Event.Sender.SenderId == nil {
		return nil
	}
	m, s := ev.Event.Message, ev.Event.Sender
	// 只理用户发来的单聊文本。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是泄露了,
	// 更不能认。
	if str(m.ChatType) != "p2p" || str(s.SenderType) != "user" || str(m.MessageType) != "text" {
		return nil
	}
	openID := str(s.SenderId.OpenId)
	sink := sinkFrom(ctx)
	if openID == "" || sink == nil || !a.seen.First(str(m.MessageId)) {
		return nil
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(str(m.Content)), &body) != nil {
		return nil
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		return nil
	}
	reply := sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: openID, Text: text})
	if reply == "" {
		return nil
	}
	// 绑定已经落库,回复不能因为 ctx 刚好被取消(Reload)而丢掉。
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.sendText(ackCtx, openID, reply); err != nil {
		slog.Warn("lark: reply to direct message failed", "err", err)
	}
	return nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sdkLogger 把 SDK 的日志接进 slog。Debug 一律丢弃:SDK 在 Debug 级会把整条事件 payload
// 打出来,其中有用户私聊机器人发的绑定码。Info 降为 Debug:连上、重连这类事网关自己会说。
type sdkLogger struct{}

func (sdkLogger) Debug(context.Context, ...interface{}) {}
func (sdkLogger) Info(_ context.Context, args ...interface{}) {
	slog.Debug("lark sdk: " + fmt.Sprint(args...))
}
func (sdkLogger) Warn(_ context.Context, args ...interface{}) {
	slog.Warn("lark sdk: " + fmt.Sprint(args...))
}
func (sdkLogger) Error(_ context.Context, args ...interface{}) {
	slog.Error("lark sdk: " + fmt.Sprint(args...))
}
