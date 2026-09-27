// Package imchannel 是网关对接即时通讯平台(飞书、Telegram、钉钉、Slack、企业微信)的通用层。
//
// 它只做两件事:把消息发出去,把点击和私聊收回来。**它不判断任何人能不能批任何单** ——
// 那件事全系统只有一处(service.DecideBlockFor)。渠道若各写一份禁自审,五个渠道就是
// 五个答案,迟早会在某次只改了一边的提交里分叉。
// 见 docs/superpowers/specs/2026-09-27-im-channel-approval-design.md §1。
package imchannel

import (
	"context"
	"net/http"
)

// 渠道标识。它们落在 tbl_im_identity.channel / tbl_im_card.channel 里,也拼进配置键。
const (
	Lark     = "lark"
	Telegram = "telegram"
	DingTalk = "dingtalk"
	Slack    = "slack"
	WeCom    = "wecom"
)

// Label 是渠道给人看的名字,也是审计 operator 的后缀("李四(飞书)")。
func Label(kind string) string {
	switch kind {
	case Lark:
		return "飞书"
	case Telegram:
		return "Telegram"
	case DingTalk:
		return "钉钉"
	case Slack:
		return "Slack"
	case WeCom:
		return "企业微信"
	}
	return kind
}

// Key 拼出配置键 im.<kind>.<name>。
func Key(kind, name string) string { return "im." + kind + "." + name }

// ApprovalCard 是平台无关的工单卡片。Command 由调用方先脱敏再截断 —— 渠道拿到的
// 已是能出网关的文本,不能也不必再处理口令。转义(HTML、markdown)是渠道自己的事。
type ApprovalCard struct {
	ApNo       string
	Kind       string // sql | window | export | release
	Env        string
	Tier       string
	Instance   string
	Database   string
	Risk       string
	Initiator  string
	Reason     string
	Command    string
	ConsoleURL string
}

// CardState 是卡片收起后要显示的终态。
type CardState struct {
	Status string // approved | rejected | expired | cancelled
	Text   string // "已由 李四(飞书) 通过"
}

// Action 是一次按钮点击。它只说「谁、哪张单、什么动作」,不带任何权限信息。
type Action struct {
	Channel        string
	PlatformUserID string
	ApNo           string
	Approve        bool
}

// ActionResult 是回给点击者看的那句话。
type ActionResult struct{ Toast string }

// DM 是一条私聊文本。渠道只把**私聊**交上来,群消息在渠道里就丢掉。
type DM struct {
	Channel        string
	PlatformUserID string
	Text           string
}

type Channel interface{ Kind() string }

// Notifier 往配置的通知群发一条「新工单待审批」。不带按钮。
type Notifier interface {
	Channel
	NotifyApproval(ctx context.Context, c ApprovalCard) error
}

// Approver 私信审批卡片、收起卡片、并收回点击与私聊。
//
// Run 阻塞到 ctx 取消,期间自己负责断线重连;它返回(且 ctx 未取消)就表示这个渠道放弃了,
// Hub 把错误记进状态给设置页看。
type Approver interface {
	Channel
	SendCard(ctx context.Context, platformUserID string, c ApprovalCard) (msgRef string, err error)
	CloseCard(ctx context.Context, msgRef string, final CardState) error
	Run(ctx context.Context, sink ActionSink) error
}

// ActionSink 由 service 实现:认人、决策、绑定都在那一侧。
type ActionSink interface {
	OnAction(ctx context.Context, a Action) ActionResult
	OnDirectMessage(ctx context.Context, dm DM) string
}

// BotNamer 是可选能力:知道自己机器人叫什么的渠道实现它,绑定弹窗据此告诉用户私聊谁。
type BotNamer interface{ BotName() string }

// Settings 是渠道读配置的口子。Secret 返回解密后的值。
type Settings interface {
	String(key string) string
	Bool(key string) bool
	Secret(key string) string
}

// Deps 是构造渠道时交给它的东西。HTTP 是网关带 SSRF 防护的出站客户端,ValidateURL 是
// 同一套出站地址校验 —— 渠道不许自己 new 一个 http.Client,否则就绕开了那道防护。
type Deps struct {
	Settings    Settings
	HTTP        *http.Client
	ValidateURL func(string) error
}

// Builder 按当前配置造一个渠道实例。凭据不全时返回 nil —— 那不是错误,只是没配。
type Builder func(d Deps) Channel
