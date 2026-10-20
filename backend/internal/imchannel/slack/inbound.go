package slack

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"velagateway/internal/imchannel"
)

// interaction 是 block_actions 载荷里网关用得到的部分。
type interaction struct {
	Type string `json:"type"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Container struct {
		ChannelID string `json:"channel_id"`
	} `json:"container"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// onInteractive 处理一次按钮点击,返回要回给点击者的话,以及回到哪个会话、给谁(为空表示不回)。
//
// 认人只凭 user.id —— Slack 保证它是真正的点击者(Socket Mode 在已认证的连接上,webhook 验过签)。
// 对应哪个网关账户、能不能批,都是 sink 那一侧的事。
func (a *App) onInteractive(ctx context.Context, sink imchannel.ActionSink, raw []byte) (toast, channel, user string) {
	var p interaction
	if json.Unmarshal(raw, &p) != nil || p.Type != "block_actions" || len(p.Actions) == 0 || !userIDRe.MatchString(p.User.ID) {
		return "", "", "" // 认不出点击者:无人可回
	}
	act := p.Actions[0]
	if act.ActionID == actionOpen {
		return "", "", "" // 「前往控制台」:Slack 已经打开了链接,网关无事可做
	}
	channel = p.Container.ChannelID
	if channel == "" {
		channel = p.Channel.ID
	}
	if !convRe.MatchString(channel) {
		channel = ""
	}
	apNo, approve, ok := parseValue(act.Value)
	if !ok || (act.ActionID != actionApprove && act.ActionID != actionReject) {
		return "无法识别的操作", channel, p.User.ID
	}
	return sink.OnAction(ctx, imchannel.Action{PlatformUserID: p.User.ID, ApNo: apNo, Approve: approve}).Toast, channel, p.User.ID
}

// tell 把点击结果作为「仅你可见」的消息回给点击者 —— Slack 的按钮点击没有 toast。点击已经处理完,
// 回话不能因为 ctx 刚好被取消(Reload)而丢掉。
func (a *App) tell(ctx context.Context, channel, user, toast string) {
	if toast == "" || channel == "" || user == "" {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.postEphemeral(rctx, channel, user, toast); err != nil {
		slog.Warn("slack: telling the clicker the outcome failed", "err", err)
	}
}

// eventCallback 是 Events API 载荷里网关用得到的部分。
type eventCallback struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	Event   struct {
		Type        string `json:"type"`
		ChannelType string `json:"channel_type"`
		Subtype     string `json:"subtype"`
		BotID       string `json:"bot_id"`
		User        string `json:"user"`
		Text        string `json:"text"`
		Channel     string `json:"channel"`
	} `json:"event"`
}

// onEvent 只理用户发给机器人的私聊文本。频道里的任何文字都不触发任何逻辑 —— 绑定码发在频道里
// 就是泄露了,更不能认。subtype 非空的是编辑、删除、入群提示这类,机器人(包括网关自己)发的也不理。
func (a *App) onEvent(ctx context.Context, sink imchannel.ActionSink, raw []byte) {
	var e eventCallback
	if json.Unmarshal(raw, &e) != nil || e.Type != "event_callback" {
		return
	}
	m := e.Event
	if m.Type != "message" || m.ChannelType != "im" || m.Subtype != "" || m.BotID != "" ||
		!userIDRe.MatchString(m.User) || m.User == a.self().userID || !dmRe.MatchString(m.Channel) {
		return
	}
	text := strings.TrimSpace(m.Text)
	// Slack 没及时收到确认会重投同一个事件:绑定码第二次到达时已被消费,不去重的话用户会在
	// 「已绑定」之后又收到一句「无效或已过期」。
	if text == "" || !a.seen.First(e.EventID) {
		return
	}
	reply := sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: m.User, Text: text})
	if reply == "" {
		return
	}
	// 绑定已经落库,回复不能因为 ctx 刚好被取消(Reload)而丢掉。
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.sendText(rctx, m.Channel, reply); err != nil {
		slog.Warn("slack: reply to direct message failed", "err", err)
	}
}
