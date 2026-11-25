// Package chat 是聊天审批卡片的渠道接口与卡片模型(.scratch/chat-approval-cards/PRD.md)。
//
// 本包与各渠道的实现只管收发,不认识审批:不引用 service / repository / model。谁能批、批了算不算数
// 全在 Handler 那一侧(service),走的是站内审批同一个决定核心。
//
// 网关主动连出去(Lark 长连接、Telegram 长轮询),不需要公网入口;只认私聊。
package chat

import (
	"context"
	"errors"
)

// Channel 是一个聊天渠道。它的值会落库(tbl_chat_*、tbl_approval.decided_via),不要改。
type Channel string

const (
	Lark     Channel = "lark"
	Telegram Channel = "telegram"
)

// Name 是给人看的渠道名:审计操作人「张伟 (Lark)」里的那个词,不随语言变。
func (c Channel) Name() string {
	switch c {
	case Lark:
		return "Lark"
	case Telegram:
		return "Telegram"
	}
	return string(c)
}

// Action 是卡片按钮带回来的动作。按钮只带令牌和动作,不带单号 —— 单号可预测。
type Action string

const (
	ActApprove Action = "approve" // 只进入二次确认,不改状态
	ActConfirm Action = "confirm" // 确认通过:真正的决定
	ActBack    Action = "back"    // 从二次确认退回
	ActReject  Action = "reject"
	ActTest    Action = "test" // 测试卡片的按钮:只回提示,不碰任何单子
)

// Ref 是平台上一条已发出的消息,更新卡片时用。
type Ref struct{ ChatID, MessageID string }

// Click 是一次按钮点击。BotID / AccountID 由渠道从平台的事件里取,不取自按钮的内容。
type Click struct {
	Channel          Channel
	BotID, AccountID string
	Token            string
	Reason           string // 拒绝时填的原因;渠道不支持就是空
	Action           Action
}

// ClickResult 是对一次点击的应答。Card 非 nil 表示把那张卡片就地换成它。
type ClickResult struct {
	Toast string
	OK    bool
	Card  *Card
}

// Handler 是渠道收到东西之后交给谁(由 service 实现)。
type Handler interface {
	// OnText 处理一条私聊文字,返回要回给对方的话(空 = 不回)。
	OnText(ctx context.Context, ch Channel, botID, accountID, chatID, text string) (reply string)
	OnClick(ctx context.Context, c Click) ClickResult
}

// State 是一个渠道此刻的连接状态。
type State string

const (
	StateOff        State = "off"
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateConflict   State = "conflict" // 同一个机器人在别处也连着
	StateError      State = "error"
)

// Transport 是一个渠道。任何错误文本里都不得出现机器人令牌、应用密钥、代理地址。
type Transport interface {
	BotID() string
	BotName() string
	// Run 连上并一直收事件,阻塞到断开;重连由调用方负责。
	Run(ctx context.Context, h Handler, onState func(State, string)) error
	// Send 私聊发一张卡片给某个账号。
	Send(ctx context.Context, accountID string, c Card) (Ref, error)
	// Update 就地更新一张已发出的卡片;消息没了或过了平台允许的期限返回 ErrGone。
	Update(ctx context.Context, r Ref, c Card) error
}

// ErrGone:那条消息再也更新不了了(被删、超期),不必重试。渠道可以用 %w 包上平台的原因。
var ErrGone = errors.New("chat: message can no longer be updated")
