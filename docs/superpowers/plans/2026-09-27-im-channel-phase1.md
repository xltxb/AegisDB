# 多渠道 IM 审批 · 第 1 期（框架 + 删除审批魔方）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立平台无关的 IM 渠道层（`internal/imchannel` + Hub），打通「私信卡片扇出 → 点击认人 → 站内同一判定 → 所有卡片收起」与一次性绑定码，把飞书群通知迁进新框架，并整体删除审批魔方。

**Architecture:** `imchannel` 定义渠道接口（`Notifier` / `Approver`）与 `Hub`（扇出、卡片记录、收起、长连接生命周期），不依赖任何平台 SDK。`service` 实现 `ActionSink`：平台用户 ID 经绑定表 `tbl_im_identity` 映射为网关账户后，调 `DecideApprovalAs` —— 判定只走 `DecideBlockFor`。本期没有真实的交互渠道，框架由 `imchannel/imtest.Fake` 端到端验证；飞书只实现群通知。

**Tech Stack:** Go 1.25 · Gin · GORM · PostgreSQL（迁移只走 `backend/migrations/*.sql`）· React 19 + TS + TanStack Query · 本期不引入任何新的第三方依赖。

**Spec:** `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`

**范围说明：** spec §14 的第 1 期。第 2–6 期（Telegram、飞书交互、钉钉、Slack、通知补齐）各自单独成计划，写在前一期合并之后，以便对着真实接口写。

## Global Constraints

- 模块名 `velagateway`，Go 1.25；后端命令都在 `backend/` 下执行。跑后端测试需要 `createdb vela_test`（见 CLAUDE.md）。
- schema 只能由 `backend/migrations/*.sql` 定义（`TestSchemaHasOneSourceOfTruth`）。每个迁移文件按文件名顺序应用一次；本期新增 `0005_im_channel.sql`、`0006_im_lark_notify_keys.sql`、`0007_drop_approval_cube.sql`。spec 写的是单个 0005，这里拆成三个，原因是每个迁移要和依赖它的代码在同一个任务里落地，这样每个任务提交后测试都是绿的。
- 渠道标识：`lark` `telegram` `dingtalk` `slack` `wecom`；显示名：飞书 / Telegram / 钉钉 / Slack / 企业微信。
- 配置键统一 `im.<channel>.<name>`；本期用到：`im.<channel>.approval`（bool，交互审批开关）、`im.<channel>.notify`（bool）、`im.lark.notifyWebhook`、`im.lark.notifySecret`🔒。凡以 `im.` 开头且 `isSecretKey` 为真的键都加密落库。
- 所有开关默认关闭；出站一律异步、best-effort，失败只 `slog.Warn`，不阻塞建单。
- 命令文本出网关前在**通用层**先 `sqlutil.RedactSecrets` 再截断（`safeClip`），渠道拿到的已是安全文本。
- 绑定码：`BIND-` + 8 位，字母表 `ABCDEFGHJKLMNPQRSTUVWXYZ23456789`，有效期 10 分钟，一次性；库里只存 `sha256(code)` 的 hex，按哈希查找（比较的是哈希，查找本身不泄露码）。每个用户每个渠道同时只有一个有效码。
- 审计列宽：`tbl_audit_log.actor_name` 是 64，`operator` 是 128。`clip(s, n)` 超长时会**追加**一个 `…`，所以写这两列时用 `clip(s, 63)` / `clip(s, 127)`。
- 未绑定的点击也要审计：`actor` 记 `<channel>:<platformUserID>`，`operator` 记 `<显示名>(未绑定)`。
- IM 决策的 operator：`clip(user.Name+"("+显示名+")", 127)`。
- 代码注释沿用仓库风格（中文，讲清「为什么」）；提交信息用英文 conventional commits，结尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- 前端验证：`cd frontend && npm run build && npm run lint`（`build` 含 `tsc --noEmit`）。

## Review Focus

spec 没有明说、但实际使用时最可能出问题的五类输入。每一条的测试已经放进对应任务：

1. **超长或怪异的平台用户 ID 出现在审计里**：`actor_name` 只有 64 列宽，外部输入决定审计写不写得进去是之前踩过的坑 → 必须截断，照样落一行审计（Task 4，`TestIM_StrangerWithHugePlatformIDIsStillAudited`）。
2. **审批链上没有一个人绑定过渠道**：不发卡片、不报错，工单照常 pending，站内可批（Task 3，`TestIM_NoBoundMembersMeansNoCardsAndTicketStillPending`）。
3. **绑定码输入形式各异**：大小写不对、首尾空格要能认；混在一句话里（「我的码是 BIND-…」）不认；回复不能泄露码是否存在（Task 5，`TestIMBind_CodeIsNormalisedButNotFishedOutOfText`）。
4. **决策比卡片发送还快**：卡片还在往外发时工单已在控制台批掉，晚发出去的卡片也必须被收起（Task 1，`TestHub_SettledWhileSendingStillClosesLateCards`）。
5. **同一个人在两个渠道都绑了**：两边各收一张卡片，任一边决策后两边都收起（Task 3，`TestIM_DecisionClosesCardsOnEveryChannel`）。

---

## 文件结构

**新建（后端）**

| 文件 | 职责 |
|---|---|
| `backend/internal/imchannel/channel.go` | 渠道标识、显示名、卡片/动作类型、`Notifier`/`Approver`/`ActionSink` 接口、`Deps`/`Builder` |
| `backend/internal/imchannel/hub.go` | Hub：注册、Reload/长连接生命周期、扇出发卡、收卡、群通知、状态 |
| `backend/internal/imchannel/hub_test.go` | Hub 单元测试（内存 Store，无数据库） |
| `backend/internal/imchannel/imtest/fake.go` | 测试用假渠道（同时实现 Notifier + Approver，可模拟点击和私聊） |
| `backend/internal/imchannel/lark/lark.go` | 飞书群通知（自定义机器人 webhook、加签、卡片渲染） |
| `backend/internal/imchannel/lark/lark_test.go` | 飞书卡片与签名的协议层测试 |
| `backend/internal/model/im.go` | `IMIdentity` / `IMBindCode` / `IMCard` |
| `backend/internal/repository/im.go` | 三张表的读写 |
| `backend/internal/repository/im_test.go` | 仓储测试 |
| `backend/internal/service/im_approval.go` | Hub 的 Settings/Store 适配、卡片映射、建单/终态挂钩、`OnAction` |
| `backend/internal/service/im_bind.go` | 绑定码、`OnDirectMessage`、代绑/解绑、绑定列表 |
| `backend/internal/handler/im.go` | IM 相关 HTTP 接口 |
| `backend/migrations/0005_im_channel.sql` `0006_im_lark_notify_keys.sql` `0007_drop_approval_cube.sql` | 迁移 |
| `backend/internal/bootstrap/im_helpers_test.go` | 测试辅助（装假渠道、绑定、等出站收敛） |
| `backend/internal/bootstrap/im_dispatch_test.go` `im_action_test.go` `im_bind_test.go` `im_settings_test.go` `im_migration_test.go` `approval_helpers_test.go` | 端到端测试 |

**新建（前端）**：`frontend/src/api/modules/im.ts`、`frontend/src/hooks/useIm.ts`、`frontend/src/components/settings/ImChannelsPanel.tsx`、`frontend/src/components/modals/ImBindModal.tsx`、`frontend/src/components/permission/UserImPane.tsx`

**删除**：`backend/internal/service/external_approval.go`、`external_approval_redact_test.go`；`backend/internal/bootstrap/external_approval_test.go`、`external_never_dispatched_test.go`、`external_decide_race_test.go`、`callback_status_test.go`

---

### Task 1: `imchannel` 通用层与 Hub

**Files:**
- Create: `backend/internal/imchannel/channel.go`
- Create: `backend/internal/imchannel/hub.go`
- Create: `backend/internal/imchannel/imtest/fake.go`
- Test: `backend/internal/imchannel/hub_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - 常量 `imchannel.Lark|Telegram|DingTalk|Slack|WeCom`；`func Label(kind string) string`；`func Key(kind, name string) string`
  - `type ApprovalCard struct{ ApNo, Kind, Env, Tier, Instance, Database, Risk, Initiator, Reason, Command, ConsoleURL string }`
  - `type CardState struct{ Status, Text string }`；`type Action struct{ Channel, PlatformUserID, ApNo string; Approve bool }`；`type ActionResult struct{ Toast string }`；`type DM struct{ Channel, PlatformUserID, Text string }`
  - 接口 `Channel{Kind() string}`、`Notifier{Channel; NotifyApproval(ctx, ApprovalCard) error}`、`Approver{Channel; SendCard(ctx, platformUserID string, c ApprovalCard) (string, error); CloseCard(ctx, msgRef string, s CardState) error; Run(ctx, sink ActionSink) error}`、`ActionSink{OnAction(ctx, Action) ActionResult; OnDirectMessage(ctx, DM) string}`、`BotNamer{BotName() string}`、`Settings{String(k) string; Bool(k) bool; Secret(k) string}`
  - `type Deps struct{ Settings Settings; HTTP *http.Client; ValidateURL func(string) error }`；`type Builder func(Deps) Channel`；`type Caps struct{ Notify, Approve bool }`
  - `type Card struct{ ID, ApprovalID int64; Channel string; UserID int64; MsgRef string }`；`type Store interface{ PlatformIDs(channel string, userIDs []int64) (map[int64]string, error); RecordCard(Card) error; OpenCards(approvalID int64) ([]Card, error); MarkCardClosed(id int64) error }`
  - `type Status struct{ Kind, Label string; CanNotify, CanApprove, Configured, Notify, Approval, Running bool; BotName, LastError string }`（JSON 字段 camelCase）
  - `func NewHub(Deps, Store) *Hub`；方法 `Register(kind string, caps Caps, b Builder)`、`SetSink(ActionSink)`、`Start(ctx)`、`Reload()`、`Stop()`、`ApprovalCreated(approvalID int64, c ApprovalCard, recipients []int64)`、`ApprovalSettled(approvalID int64, s CardState)`、`NotifyApproval(c ApprovalCard)`、`TestNotify(ctx, kind string, c ApprovalCard) error`、`TestCard(ctx, kind, platformUserID string, c ApprovalCard) error`、`CanApprove(kind) bool`、`ApprovalEnabled(kind) bool`、`ApproveKinds() []string`、`BotName(kind) string`、`Status() []Status`、`Flush()`；`var ErrNotConfigured`
  - `imtest.New(kind string) *Fake`；`(*Fake).Builder() imchannel.Builder`、`Click(pid, apNo string, approve bool) imchannel.ActionResult`、`DirectMessage(pid, text string) string`、`WaitRunning(t testing.TB)`、`Running() bool`、`Sent() []Sent`、`Closed() []Closed`、`Notified() []imchannel.ApprovalCard`、`Runs() int`；字段 `FailSend bool`、`SendDelay time.Duration`

- [ ] **Step 1: 写 `channel.go`**

```go
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
```

- [ ] **Step 2: 写 `imtest/fake.go`**

```go
// Package imtest 提供一个假渠道,用来端到端验证 Hub 与 service 的接线,不碰任何真实平台。
package imtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
)

type Sent struct {
	PlatformUserID string
	Card           imchannel.ApprovalCard
	Ref            string
}

type Closed struct {
	Ref   string
	State imchannel.CardState
}

// Fake 同时实现 Notifier 与 Approver。
type Fake struct {
	FailSend  bool
	SendDelay time.Duration

	kind     string
	mu       sync.Mutex
	seq      int
	sent     []Sent
	closed   []Closed
	notified []imchannel.ApprovalCard
	sink     imchannel.ActionSink
	runs     int
}

func New(kind string) *Fake { return &Fake{kind: kind} }

func (f *Fake) Kind() string    { return f.kind }
func (f *Fake) BotName() string { return "fake-bot" }

// Builder 每次 Reload 都交回同一个实例,测试因此能跨 Reload 观察它。
func (f *Fake) Builder() imchannel.Builder {
	return func(imchannel.Deps) imchannel.Channel { return f }
}

func (f *Fake) NotifyApproval(_ context.Context, c imchannel.ApprovalCard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, c)
	return nil
}

func (f *Fake) SendCard(ctx context.Context, pid string, c imchannel.ApprovalCard) (string, error) {
	if f.SendDelay > 0 {
		select {
		case <-time.After(f.SendDelay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSend {
		return "", errors.New("fake: send failed")
	}
	f.seq++
	ref := fmt.Sprintf("%s-%d", f.kind, f.seq)
	f.sent = append(f.sent, Sent{PlatformUserID: pid, Card: c, Ref: ref})
	return ref, nil
}

func (f *Fake) CloseCard(_ context.Context, ref string, s imchannel.CardState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, Closed{Ref: ref, State: s})
	return nil
}

func (f *Fake) Run(ctx context.Context, sink imchannel.ActionSink) error {
	f.mu.Lock()
	f.sink = sink
	f.runs++
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.sink = nil
	f.mu.Unlock()
	return nil
}

// Click 模拟一次按钮点击。渠道没在运行时返回零值 —— 那正是真实平台上「点了没人收」。
func (f *Fake) Click(pid, apNo string, approve bool) imchannel.ActionResult {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink == nil {
		return imchannel.ActionResult{}
	}
	return sink.OnAction(context.Background(), imchannel.Action{
		Channel: f.kind, PlatformUserID: pid, ApNo: apNo, Approve: approve,
	})
}

// DirectMessage 模拟一条私聊文本,返回机器人的回复。
func (f *Fake) DirectMessage(pid, text string) string {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink == nil {
		return ""
	}
	return sink.OnDirectMessage(context.Background(), imchannel.DM{Channel: f.kind, PlatformUserID: pid, Text: text})
}

func (f *Fake) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink != nil
}

func (f *Fake) WaitRunning(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake channel %q never started running", f.kind)
}

func (f *Fake) Runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

func (f *Fake) Closed() []Closed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Closed(nil), f.closed...)
}

func (f *Fake) Notified() []imchannel.ApprovalCard {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]imchannel.ApprovalCard(nil), f.notified...)
}
```

- [ ] **Step 3: 写失败的 Hub 测试 `hub_test.go`**

```go
package imchannel_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

type memSettings struct {
	mu sync.Mutex
	kv map[string]any
}

func newSettings() *memSettings { return &memSettings{kv: map[string]any{}} }
func (s *memSettings) set(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[k] = v
}
func (s *memSettings) String(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.kv[k].(string)
	return v
}
func (s *memSettings) Bool(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.kv[k].(bool)
	return v
}
func (s *memSettings) Secret(k string) string { return s.String(k) }

type memStore struct {
	mu    sync.Mutex
	ids   map[string]map[int64]string
	cards []imchannel.Card
	seq   int64
}

func newStore() *memStore { return &memStore{ids: map[string]map[int64]string{}} }
func (m *memStore) bind(ch string, uid int64, pid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ids[ch] == nil {
		m.ids[ch] = map[int64]string{}
	}
	m.ids[ch][uid] = pid
}
func (m *memStore) PlatformIDs(ch string, uids []int64) (map[int64]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]string{}
	for _, u := range uids {
		if p, ok := m.ids[ch][u]; ok {
			out[u] = p
		}
	}
	return out, nil
}
func (m *memStore) RecordCard(c imchannel.Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	c.ID = m.seq
	m.cards = append(m.cards, c)
	return nil
}
func (m *memStore) OpenCards(apID int64) ([]imchannel.Card, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []imchannel.Card
	for _, c := range m.cards {
		if c.ApprovalID == apID && c.ID > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}
func (m *memStore) MarkCardClosed(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cards {
		if m.cards[i].ID == id {
			m.cards[i].ID = -id // 负数 = 已关闭
		}
	}
	return nil
}
func (m *memStore) open() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.cards {
		if c.ID > 0 {
			n++
		}
	}
	return n
}

var both = imchannel.Caps{Notify: true, Approve: true}

func newHub(t *testing.T, s *memSettings, st *memStore) *imchannel.Hub {
	t.Helper()
	h := imchannel.NewHub(imchannel.Deps{Settings: s}, st)
	t.Cleanup(h.Stop)
	return h
}

type nopSink struct{}

func (nopSink) OnAction(context.Context, imchannel.Action) imchannel.ActionResult {
	return imchannel.ActionResult{}
}
func (nopSink) OnDirectMessage(context.Context, imchannel.DM) string { return "" }

func TestHub_ApprovalCreatedSendsOnlyToBoundRecipientsOnEnabledChannels(t *testing.T) {
	s, st := newSettings(), newStore()
	a, b := imtest.New("a"), imtest.New("b")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("b", both, b.Builder())
	s.set("im.a.approval", true) // b 没开
	h.Reload()
	st.bind("a", 1, "pa1")
	st.bind("a", 2, "pa2")
	st.bind("b", 1, "pb1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1, 2, 3})
	h.Flush()

	if got := a.Sent(); len(got) != 2 || got[0].PlatformUserID != "pa1" || got[1].PlatformUserID != "pa2" {
		t.Fatalf("channel a sent = %+v, want pa1 then pa2", got)
	}
	if got := b.Sent(); len(got) != 0 {
		t.Fatalf("disabled channel b must send nothing, sent %+v", got)
	}
	if st.open() != 2 {
		t.Fatalf("recorded cards = %d, want 2", st.open())
	}
}

func TestHub_ApprovalSettledClosesEveryOpenCardEvenAfterChannelIsSwitchedOff(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")
	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.Flush()

	s.set("im.a.approval", false)
	h.Reload()
	h.ApprovalSettled(10, imchannel.CardState{Status: "approved", Text: "已由 李四 通过"})
	h.Flush()

	if c := a.Closed(); len(c) != 1 || c[0].State.Status != "approved" {
		t.Fatalf("closed = %+v, want one approved close", c)
	}
	if st.open() != 0 {
		t.Fatal("card should be marked closed")
	}
	h.ApprovalSettled(10, imchannel.CardState{Status: "approved"})
	h.Flush()
	if len(a.Closed()) != 1 {
		t.Fatal("a closed card must not be closed twice")
	}
}

func TestHub_SettledWhileSendingStillClosesLateCards(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.SendDelay = 100 * time.Millisecond
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.ApprovalSettled(10, imchannel.CardState{Status: "rejected"}) // 决定比发送快
	h.Flush()

	if len(a.Sent()) != 1 || len(a.Closed()) != 1 {
		t.Fatalf("sent=%d closed=%d, want the late card closed too", len(a.Sent()), len(a.Closed()))
	}
}

func TestHub_FailedSendRecordsNoCard(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.FailSend = true
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.Flush()
	if st.open() != 0 {
		t.Fatal("a failed send must not leave a card to close")
	}
}

func TestHub_ReloadStartsAndStopsRunLoops(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	h.Start(context.Background())
	a.WaitRunning(t)

	s.set("im.a.approval", false)
	h.Reload()
	if a.Running() {
		t.Fatal("switching approval off must stop the run loop")
	}
	s.set("im.a.approval", true)
	h.Reload()
	a.WaitRunning(t)
	if a.Runs() != 2 {
		t.Fatalf("runs = %d, want 2", a.Runs())
	}
	if st := h.Status(); len(st) != 1 || !st[0].Running || !st[0].Approval || st[0].BotName != "fake-bot" {
		t.Fatalf("status = %+v", st)
	}
}

func TestHub_NotifyOnlyReachesNotifyEnabledChannels(t *testing.T) {
	s, st := newSettings(), newStore()
	a, b := imtest.New("a"), imtest.New("b")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("b", both, b.Builder())
	s.set("im.a.notify", true)
	h.Reload()

	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-1"})
	h.Flush()
	if len(a.Notified()) != 1 || len(b.Notified()) != 0 {
		t.Fatalf("a=%d b=%d, want 1/0", len(a.Notified()), len(b.Notified()))
	}
}

func TestHub_UnconfiguredChannelIsSkipped(t *testing.T) {
	s, st := newSettings(), newStore()
	h := newHub(t, s, st)
	h.Register("x", both, func(imchannel.Deps) imchannel.Channel { return nil })
	s.set("im.x.approval", true)
	s.set("im.x.notify", true)
	h.Reload()
	st.bind("x", 1, "p")

	h.ApprovalCreated(1, imchannel.ApprovalCard{}, []int64{1})
	h.NotifyApproval(imchannel.ApprovalCard{})
	h.Flush()
	if h.ApprovalEnabled("x") {
		t.Fatal("an unconfigured channel is not approval-enabled")
	}
	if err := h.TestNotify(context.Background(), "x", imchannel.ApprovalCard{}); err != imchannel.ErrNotConfigured {
		t.Fatalf("TestNotify err = %v, want ErrNotConfigured", err)
	}
	if got := h.Status(); got[0].Configured || !got[0].CanApprove {
		t.Fatalf("status = %+v", got[0])
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/...`
Expected: 编译失败，`undefined: imchannel.NewHub` 等。

- [ ] **Step 5: 写 `hub.go`**

```go
package imchannel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrNotConfigured 表示这个渠道没配凭据,或者不具备被要求的能力。
var ErrNotConfigured = errors.New("im channel not configured")

const sendTimeout = 15 * time.Second

// Caps 是渠道**能**做什么(与是否打开无关)。企业微信只有 Notify。
type Caps struct {
	Notify  bool
	Approve bool
}

// Card 是一张已经发出去的交互卡片。
type Card struct {
	ID         int64
	ApprovalID int64
	Channel    string
	UserID     int64
	MsgRef     string
}

// Store 是 Hub 需要的持久化,由 service 用仓储实现。
type Store interface {
	PlatformIDs(channel string, userIDs []int64) (map[int64]string, error)
	RecordCard(c Card) error
	OpenCards(approvalID int64) ([]Card, error)
	MarkCardClosed(id int64) error
}

// Status 是设置页上一个渠道的运行状况。
type Status struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	CanNotify  bool   `json:"canNotify"`
	CanApprove bool   `json:"canApprove"`
	Configured bool   `json:"configured"`
	Notify     bool   `json:"notify"`
	Approval   bool   `json:"approval"`
	Running    bool   `json:"running"`
	BotName    string `json:"botName,omitempty"`
	LastError  string `json:"lastError,omitempty"`
}

type entry struct {
	caps    Caps
	build   Builder
	ch      Channel
	cancel  context.CancelFunc
	running bool
	lastErr string
}

// Hub 管理所有渠道:扇出、记录、收起、长连接。它不做任何判定。
type Hub struct {
	deps  Deps
	store Store

	reloadMu sync.Mutex // Reload 串行
	mu       sync.Mutex
	order    []string
	entries  map[string]*entry
	root     context.Context
	sink     ActionSink

	work sync.WaitGroup // 出站异步任务
	runs sync.WaitGroup // 长连接

	apMu   sync.Mutex
	queues map[int64]*apQueue
}

type apQueue struct{ jobs []func() }

func NewHub(deps Deps, store Store) *Hub {
	return &Hub{deps: deps, store: store, entries: map[string]*entry{}, queues: map[int64]*apQueue{}}
}

// Register 登记一个渠道。登记完要 Reload 才生效。
func (h *Hub) Register(kind string, caps Caps, b Builder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.entries[kind]; !ok {
		h.order = append(h.order, kind)
	}
	h.entries[kind] = &entry{caps: caps, build: b}
}

func (h *Hub) SetSink(s ActionSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sink = s
}

// Start 记下进程级的 ctx 并按当前配置起渠道。ctx 取消时所有长连接退出。
func (h *Hub) Start(ctx context.Context) {
	h.mu.Lock()
	h.root = ctx
	h.mu.Unlock()
	h.Reload()
}

// Reload 按当前配置重建渠道实例并重启长连接。设置保存后调用。
//
// 先停后起,而且**等旧的真正退出**再起新的:Telegram 的 getUpdates 同一时刻只认一个
// 连接,旧循环没退干净就起新的,两边会互相踢(409)。
func (h *Hub) Reload() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.stopRuns()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, kind := range h.order {
		e := h.entries[kind]
		e.ch = e.build(h.deps)
		e.lastErr = ""
		ap, ok := e.ch.(Approver)
		if !ok || !e.caps.Approve || h.root == nil || h.sink == nil || !h.deps.Settings.Bool(Key(kind, "approval")) {
			continue
		}
		ctx, cancel := context.WithCancel(h.root)
		e.cancel, e.running = cancel, true
		h.runs.Add(1)
		go h.run(kind, ap, ctx, h.sink)
	}
}

// Stop 停掉所有长连接并等出站任务收敛。进程退出与测试清理时调用。
func (h *Hub) Stop() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.stopRuns()
	h.work.Wait()
}

func (h *Hub) stopRuns() {
	h.mu.Lock()
	for _, e := range h.entries {
		if e.cancel != nil {
			e.cancel()
			e.cancel = nil
		}
	}
	h.mu.Unlock()
	h.runs.Wait()
}

func (h *Hub) run(kind string, ap Approver, ctx context.Context, sink ActionSink) {
	defer h.runs.Done()
	err := ap.Run(ctx, sink)
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[kind]
	e.running = false
	if err != nil && ctx.Err() == nil {
		e.lastErr = err.Error()
		slog.Warn("im channel stopped", "channel", kind, "err", err)
	}
}

type target struct {
	kind string
	ap   Approver
}

// approvers 返回已配置的交互渠道;enabledOnly 时只要开关打开的。
func (h *Hub) approvers(enabledOnly bool) []target {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []target
	for _, kind := range h.order {
		e := h.entries[kind]
		ap, ok := e.ch.(Approver)
		if !ok || !e.caps.Approve {
			continue
		}
		if enabledOnly && !h.deps.Settings.Bool(Key(kind, "approval")) {
			continue
		}
		out = append(out, target{kind, ap})
	}
	return out
}

func (h *Hub) approver(kind string) Approver {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.entries[kind]; e != nil {
		if ap, ok := e.ch.(Approver); ok {
			return ap
		}
	}
	return nil
}

// enqueue 把一个任务排进这张单自己的 FIFO 队列,立即返回。
//
// 同一张单的「发卡」与「收卡」必须按调用顺序执行。不排队的话,工单在控制台被秒批时,
// 收卡那一轮会先跑完,而晚一步发出去的卡片从此没人收 —— 一张已经批掉的单,在某人的
// 私聊里还挂着两个能点的按钮。
//
// 用队列而不是锁:锁得在调用方的 goroutine 里拿才能保证顺序,而调用方是控制台上那次
// 「批准」请求 —— 它不该等着十几张卡片发完才返回。
func (h *Hub) enqueue(id int64, job func()) {
	h.work.Add(1)
	h.apMu.Lock()
	if q, busy := h.queues[id]; busy {
		q.jobs = append(q.jobs, job)
		h.apMu.Unlock()
		return
	}
	h.queues[id] = &apQueue{}
	h.apMu.Unlock()
	go func() {
		for {
			job()
			h.work.Done()
			h.apMu.Lock()
			q := h.queues[id]
			if len(q.jobs) == 0 {
				delete(h.queues, id)
				h.apMu.Unlock()
				return
			}
			job, q.jobs = q.jobs[0], q.jobs[1:]
			h.apMu.Unlock()
		}
	}()
}

// ApprovalCreated 给 recipients 里每个在已开启渠道上绑定过的人私信一张卡片。
func (h *Hub) ApprovalCreated(approvalID int64, c ApprovalCard, recipients []int64) {
	if len(recipients) == 0 {
		return
	}
	targets := h.approvers(true)
	if len(targets) == 0 {
		return
	}
	h.enqueue(approvalID, func() {
		for _, t := range targets {
			ids, err := h.store.PlatformIDs(t.kind, recipients)
			if err != nil {
				slog.Warn("im: load identities failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
				continue
			}
			for _, uid := range recipients {
				pid, ok := ids[uid]
				if !ok {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
				ref, err := t.ap.SendCard(ctx, pid, c)
				cancel()
				if err != nil {
					slog.Warn("im: send card failed", "channel", t.kind, "apNo", c.ApNo, "userId", uid, "err", err)
					continue
				}
				if err := h.store.RecordCard(Card{ApprovalID: approvalID, Channel: t.kind, UserID: uid, MsgRef: ref}); err != nil {
					slog.Warn("im: record card failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
				}
			}
		}
	})
}

// ApprovalSettled 收起这张单所有还开着的卡片。
//
// 按卡片行上的渠道找适配器,**不看当前开关**:渠道被关掉之后,它发出去的旧卡片仍要收起,
// 否则会留下一批永远挂着、点了没反应的按钮。凭据也清空了就只能记一条 WARN。
func (h *Hub) ApprovalSettled(approvalID int64, final CardState) {
	h.enqueue(approvalID, func() {
		cards, err := h.store.OpenCards(approvalID)
		if err != nil {
			slog.Warn("im: load open cards failed", "approvalId", approvalID, "err", err)
			return
		}
		for _, c := range cards {
			ap := h.approver(c.Channel)
			if ap == nil {
				slog.Warn("im: card left open, channel not configured", "channel", c.Channel, "approvalId", approvalID)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			err := ap.CloseCard(ctx, c.MsgRef, final)
			cancel()
			if err != nil {
				slog.Warn("im: close card failed", "channel", c.Channel, "approvalId", approvalID, "err", err)
				continue
			}
			if err := h.store.MarkCardClosed(c.ID); err != nil {
				slog.Warn("im: mark card closed failed", "cardId", c.ID, "err", err)
			}
		}
	})
}

// NotifyApproval 往每个开了群通知的渠道发一条新工单通知。
func (h *Hub) NotifyApproval(c ApprovalCard) {
	h.mu.Lock()
	var ns []Notifier
	for _, kind := range h.order {
		e := h.entries[kind]
		if n, ok := e.ch.(Notifier); ok && e.caps.Notify && h.deps.Settings.Bool(Key(kind, "notify")) {
			ns = append(ns, n)
		}
	}
	h.mu.Unlock()
	for _, n := range ns {
		h.work.Add(1)
		go func(n Notifier) {
			defer h.work.Done()
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			defer cancel()
			if err := n.NotifyApproval(ctx, c); err != nil {
				slog.Warn("im: notify failed", "channel", n.Kind(), "apNo", c.ApNo, "err", err)
			}
		}(n)
	}
}

// TestNotify 同步发一条测试通知。不看开关 —— 管理员要能在打开之前先试。
func (h *Hub) TestNotify(ctx context.Context, kind string, c ApprovalCard) error {
	h.mu.Lock()
	e := h.entries[kind]
	var n Notifier
	if e != nil && e.caps.Notify {
		n, _ = e.ch.(Notifier)
	}
	h.mu.Unlock()
	if n == nil {
		return ErrNotConfigured
	}
	return n.NotifyApproval(ctx, c)
}

// TestCard 同步私信一张测试卡片,不记录、不需要收起。
func (h *Hub) TestCard(ctx context.Context, kind, platformUserID string, c ApprovalCard) error {
	if !h.CanApprove(kind) {
		return ErrNotConfigured
	}
	ap := h.approver(kind)
	if ap == nil {
		return ErrNotConfigured
	}
	_, err := ap.SendCard(ctx, platformUserID, c)
	return err
}

// CanApprove 说这个渠道**能**做交互审批(登记时声明),与是否配置、是否打开无关。
func (h *Hub) CanApprove(kind string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[kind]
	return e != nil && e.caps.Approve
}

// ApprovalEnabled = 能做 + 已配置 + 开关打开。
func (h *Hub) ApprovalEnabled(kind string) bool {
	return h.CanApprove(kind) && h.approver(kind) != nil && h.deps.Settings.Bool(Key(kind, "approval"))
}

// ApproveKinds 按登记顺序列出所有能做交互审批的渠道。
func (h *Hub) ApproveKinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, kind := range h.order {
		if h.entries[kind].caps.Approve {
			out = append(out, kind)
		}
	}
	return out
}

func (h *Hub) BotName(kind string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.entries[kind]; e != nil {
		if b, ok := e.ch.(BotNamer); ok {
			return b.BotName()
		}
	}
	return ""
}

func (h *Hub) Status() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Status, 0, len(h.order))
	for _, kind := range h.order {
		e := h.entries[kind]
		st := Status{
			Kind: kind, Label: Label(kind), CanNotify: e.caps.Notify, CanApprove: e.caps.Approve,
			Configured: e.ch != nil,
			Notify:     h.deps.Settings.Bool(Key(kind, "notify")),
			Approval:   h.deps.Settings.Bool(Key(kind, "approval")),
			Running:    e.running, LastError: e.lastErr,
		}
		if b, ok := e.ch.(BotNamer); ok {
			st.BotName = b.BotName()
		}
		out = append(out, st)
	}
	return out
}

// Flush 等所有已派出的出站任务结束。给测试和优雅退出用。
func (h *Hub) Flush() { h.work.Wait() }
```

注意 `Reload` 里给 `e.ch` 赋值时，`e.build` 可能返回类型为 `*T` 的 nil 指针包在接口里。所有 Builder 必须**直接 `return nil`**（接口 nil），不要返回一个 nil 的具体指针——在 `channel.go` 的 `Builder` 注释里已写明。

- [ ] **Step 6: 跑测试确认通过（带 race）**

Run: `cd backend && go test -race ./internal/imchannel/...`
Expected: PASS（7 个测试）

- [ ] **Step 7: 提交**

```bash
git add backend/internal/imchannel
git commit -m "feat(im): add the platform-neutral IM channel layer and hub

Channels only move messages; they never decide who may approve. The hub
fans cards out to bound recipients on enabled channels, records what it
sent, closes every open card when a ticket settles (even on a channel
that was switched off since), and owns the long-connection lifecycle.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: 数据表与仓储

**Files:**
- Create: `backend/migrations/0005_im_channel.sql`
- Create: `backend/internal/model/im.go`
- Create: `backend/internal/repository/im.go`
- Test: `backend/internal/repository/im_test.go`
- Modify: `backend/internal/bootstrap/automigrate_guard_test.go`（表数 37 → 40）

**Interfaces:**
- Consumes: 无
- Produces:
  - `model.IMIdentity{UserID int64; Channel, PlatformUserID, BoundBy string; CreatedAt time.Time}`（表 `tbl_im_identity`）；常量 `model.IMBoundSelf = "self"`、`model.IMBoundAdmin = "admin"`
  - `model.IMBindCode{UserID int64; Channel, CodeHash string; ExpiresAt time.Time}`（`tbl_im_bind_code`）
  - `model.IMCard{ID, ApprovalID int64; Channel string; UserID int64; MsgRef string; Closed bool; CreatedAt time.Time}`（`tbl_im_card`）
  - `repository.ErrIMIdentityTaken`
  - `(*Repo).BindIMIdentity(*model.IMIdentity) error`、`IMIdentityByPlatform(channel, pid string) (*model.IMIdentity, error)`、`IMIdentitiesOfUser(userID int64) ([]model.IMIdentity, error)`、`IMPlatformIDs(channel string, userIDs []int64) (map[int64]string, error)`、`UnbindIMIdentity(userID int64, channel string) (bool, error)`
  - `PutIMBindCode(*model.IMBindCode) error`、`ConsumeIMBindCode(channel, codeHash string, now time.Time) (userID int64, ok bool, err error)`、`PurgeExpiredIMBindCodes(now time.Time) (int64, error)`
  - `CreateIMCard(*model.IMCard) error`、`OpenIMCards(approvalID int64) ([]model.IMCard, error)`、`CloseIMCard(id int64) error`

- [ ] **Step 1: 写迁移 `0005_im_channel.sql`**

```sql
-- 多渠道 IM 审批的三张表。见 docs/superpowers/specs/2026-09-27-im-channel-approval-design.md §3。
--
-- 平台账号 → 网关账户。主键 (user_id, channel):一人每渠道一个账号,重新绑定即覆盖。
-- 唯一键 (channel, platform_user_id):一个平台账号不能挂两个网关账户 —— 否则一次点击
-- 认成谁,取决于查询先碰到哪一行。
CREATE TABLE IF NOT EXISTS tbl_im_identity (
  user_id          BIGINT       NOT NULL,
  channel          VARCHAR(16)  NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  bound_by         VARCHAR(16)  NOT NULL,
  created_at       TIMESTAMPTZ  NOT NULL,
  CONSTRAINT pk_tbl_im_identity PRIMARY KEY (user_id, channel),
  CONSTRAINT uk_im_identity_platform UNIQUE (channel, platform_user_id)
);

-- 一次性绑定码。主键保证「一人每渠道同时只有一个有效码」:重新生成是一次 upsert,
-- 旧码自然作废,不需要另写清理。只存哈希。
CREATE TABLE IF NOT EXISTS tbl_im_bind_code (
  user_id    BIGINT      NOT NULL,
  channel    VARCHAR(16) NOT NULL,
  code_hash  VARCHAR(64) NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT pk_tbl_im_bind_code PRIMARY KEY (user_id, channel)
);
CREATE INDEX IF NOT EXISTS idx_im_bind_code_hash ON tbl_im_bind_code (channel, code_hash);

-- 已发出的交互卡片。工单终态时按这里逐张收起;部分索引只覆盖还开着的那些。
CREATE TABLE IF NOT EXISTS tbl_im_card (
  id          BIGINT       GENERATED BY DEFAULT AS IDENTITY,
  approval_id BIGINT       NOT NULL,
  channel     VARCHAR(16)  NOT NULL,
  user_id     BIGINT       NOT NULL,
  msg_ref     VARCHAR(256) NOT NULL,
  closed      BOOLEAN      NOT NULL DEFAULT FALSE,
  created_at  TIMESTAMPTZ  NOT NULL,
  CONSTRAINT pk_tbl_im_card PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_im_card_open ON tbl_im_card (approval_id) WHERE NOT closed;
```

- [ ] **Step 2: 写 `model/im.go`**

```go
package model

import "time"

// IMIdentity 把一个 IM 平台账号绑定到一个网关账户。审批卡片上的点击只凭这张表认人。
type IMIdentity struct {
	UserID         int64     `gorm:"primaryKey;autoIncrement:false" json:"userId"`
	Channel        string    `gorm:"primaryKey;size:16" json:"channel"`
	PlatformUserID string    `gorm:"size:128;not null" json:"platformUserId"`
	BoundBy        string    `gorm:"size:16;not null" json:"boundBy"` // self | admin
	CreatedAt      time.Time `json:"createdAt"`
}

func (IMIdentity) TableName() string { return "tbl_im_identity" }

const (
	IMBoundSelf  = "self"
	IMBoundAdmin = "admin"
)

// IMBindCode 是一次性绑定码。只存哈希。
type IMBindCode struct {
	UserID    int64     `gorm:"primaryKey;autoIncrement:false"`
	Channel   string    `gorm:"primaryKey;size:16"`
	CodeHash  string    `gorm:"size:64;not null"`
	ExpiresAt time.Time `gorm:"not null"`
}

func (IMBindCode) TableName() string { return "tbl_im_bind_code" }

// IMCard 是一张已经私信出去的审批卡片。
type IMCard struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	ApprovalID int64     `gorm:"not null"`
	Channel    string    `gorm:"size:16;not null"`
	UserID     int64     `gorm:"not null"`
	MsgRef     string    `gorm:"size:256;not null"`
	Closed     bool      `gorm:"not null;default:false"`
	CreatedAt  time.Time
}

func (IMCard) TableName() string { return "tbl_im_card" }
```

- [ ] **Step 3: 写失败的仓储测试 `repository/im_test.go`**

```go
package repository

import (
	"errors"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

func newIMRepo(t *testing.T) (*Repo, int64, int64) {
	t.Helper()
	r := New(testsupport.NewDB(t))
	a := model.User{Name: "甲", Email: "a@x.io", RoleID: 1}
	b := model.User{Name: "乙", Email: "b@x.io", RoleID: 1}
	for _, u := range []*model.User{&a, &b} {
		if err := r.db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	return r, a.ID, b.ID
}

func TestIMIdentity_BindRebindAndConflict(t *testing.T) {
	r, a, b := newIMRepo(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.BindIMIdentity(&model.IMIdentity{UserID: a, Channel: "fake", PlatformUserID: "p1", BoundBy: model.IMBoundSelf}))
	// 同一人同渠道重新绑定 = 覆盖
	must(r.BindIMIdentity(&model.IMIdentity{UserID: a, Channel: "fake", PlatformUserID: "p2", BoundBy: model.IMBoundAdmin}))
	got, err := r.IMIdentityByPlatform("fake", "p2")
	if err != nil || got.UserID != a || got.BoundBy != model.IMBoundAdmin {
		t.Fatalf("lookup p2 = %+v, %v", got, err)
	}
	if _, err := r.IMIdentityByPlatform("fake", "p1"); err == nil {
		t.Fatal("the overwritten platform id must no longer resolve")
	}
	// 别人的平台账号不能再挂到另一个网关账户上
	if err := r.BindIMIdentity(&model.IMIdentity{UserID: b, Channel: "fake", PlatformUserID: "p2", BoundBy: model.IMBoundSelf}); !errors.Is(err, ErrIMIdentityTaken) {
		t.Fatalf("err = %v, want ErrIMIdentityTaken", err)
	}
	ids, err := r.IMPlatformIDs("fake", []int64{a, b})
	if err != nil || len(ids) != 1 || ids[a] != "p2" {
		t.Fatalf("platform ids = %v, %v", ids, err)
	}
	if ok, _ := r.UnbindIMIdentity(a, "fake"); !ok {
		t.Fatal("unbind should report a removed row")
	}
	if ok, _ := r.UnbindIMIdentity(a, "fake"); ok {
		t.Fatal("unbinding twice removes nothing")
	}
}

func TestIMBindCode_SingleUseReplaceAndExpiry(t *testing.T) {
	r, a, _ := newIMRepo(t)
	now := time.Now()
	put := func(hash string, exp time.Time) {
		t.Helper()
		if err := r.PutIMBindCode(&model.IMBindCode{UserID: a, Channel: "fake", CodeHash: hash, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	put("h1", now.Add(10*time.Minute))
	put("h2", now.Add(10*time.Minute)) // 重新生成作废 h1
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h1", now); ok {
		t.Fatal("a replaced code must not be consumable")
	}
	uid, ok, err := r.ConsumeIMBindCode("fake", "h2", now)
	if err != nil || !ok || uid != a {
		t.Fatalf("consume h2 = %d %v %v", uid, ok, err)
	}
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h2", now); ok {
		t.Fatal("a code is single-use")
	}
	put("h3", now.Add(-time.Second))
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h3", now); ok {
		t.Fatal("an expired code must not be consumable")
	}
	if n, _ := r.PurgeExpiredIMBindCodes(now); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
}

func TestIMCard_OpenAndClose(t *testing.T) {
	r, a, b := newIMRepo(t)
	for _, u := range []int64{a, b} {
		if err := r.CreateIMCard(&model.IMCard{ApprovalID: 7, Channel: "fake", UserID: u, MsgRef: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	open, _ := r.OpenIMCards(7)
	if len(open) != 2 {
		t.Fatalf("open = %d", len(open))
	}
	if err := r.CloseIMCard(open[0].ID); err != nil {
		t.Fatal(err)
	}
	if open, _ = r.OpenIMCards(7); len(open) != 1 {
		t.Fatalf("open after close = %d", len(open))
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `cd backend && go test ./internal/repository/ -run 'TestIM' -v`
Expected: 编译失败，`r.BindIMIdentity undefined`。

- [ ] **Step 5: 写 `repository/im.go`**

```go
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"velagateway/internal/model"
)

// ErrIMIdentityTaken:这个平台账号已经挂在另一个网关账户上。
var ErrIMIdentityTaken = errors.New("im identity already bound to another user")

// BindIMIdentity 写入 / 覆盖一个人在某渠道的绑定。
//
// 先查「这个平台账号是不是别人的」再写,放在同一个事务里;唯一索引是最后一道 ——
// 并发下两人同时绑同一个 ID,后到的那个会在索引上失败,而不是悄悄把前一个挤掉。
func (r *Repo) BindIMIdentity(id *model.IMIdentity) error {
	if id.CreatedAt.IsZero() {
		id.CreatedAt = time.Now()
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.IMIdentity{}).
			Where("channel = ? AND platform_user_id = ? AND user_id <> ?", id.Channel, id.PlatformUserID, id.UserID).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrIMIdentityTaken
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel"}},
			DoUpdates: clause.AssignmentColumns([]string{"platform_user_id", "bound_by", "created_at"}),
		}).Create(id).Error
	})
}

func (r *Repo) IMIdentityByPlatform(channel, pid string) (*model.IMIdentity, error) {
	var id model.IMIdentity
	if err := r.db.Where("channel = ? AND platform_user_id = ?", channel, pid).First(&id).Error; err != nil {
		return nil, err
	}
	return &id, nil
}

func (r *Repo) IMIdentitiesOfUser(userID int64) ([]model.IMIdentity, error) {
	var out []model.IMIdentity
	err := r.db.Where("user_id = ?", userID).Order("channel").Find(&out).Error
	return out, err
}

func (r *Repo) IMPlatformIDs(channel string, userIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []model.IMIdentity
	if err := r.db.Where("channel = ? AND user_id IN ?", channel, userIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.UserID] = row.PlatformUserID
	}
	return out, nil
}

func (r *Repo) UnbindIMIdentity(userID int64, channel string) (bool, error) {
	res := r.db.Where("user_id = ? AND channel = ?", userID, channel).Delete(&model.IMIdentity{})
	return res.RowsAffected > 0, res.Error
}

// PutIMBindCode 按 (user_id, channel) upsert —— 新码进来,旧码就没了。
func (r *Repo) PutIMBindCode(c *model.IMBindCode) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel"}},
		DoUpdates: clause.AssignmentColumns([]string{"code_hash", "expires_at"}),
	}).Create(c).Error
}

// ConsumeIMBindCode 原子地「找到并删掉」一个未过期的码。DELETE … RETURNING 是一条语句,
// 两条私聊同时带着同一个码进来,只有一条拿得到 user_id。
func (r *Repo) ConsumeIMBindCode(channel, codeHash string, now time.Time) (int64, bool, error) {
	var ids []int64
	err := r.db.Raw(`DELETE FROM tbl_im_bind_code
	                 WHERE channel = ? AND code_hash = ? AND expires_at > ?
	                 RETURNING user_id`, channel, codeHash, now).Scan(&ids).Error
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}
	return ids[0], true, nil
}

func (r *Repo) PurgeExpiredIMBindCodes(now time.Time) (int64, error) {
	res := r.db.Where("expires_at <= ?", now).Delete(&model.IMBindCode{})
	return res.RowsAffected, res.Error
}

func (r *Repo) CreateIMCard(c *model.IMCard) error { return r.db.Create(c).Error }

func (r *Repo) OpenIMCards(approvalID int64) ([]model.IMCard, error) {
	var out []model.IMCard
	err := r.db.Where("approval_id = ? AND NOT closed", approvalID).Order("id").Find(&out).Error
	return out, err
}

func (r *Repo) CloseIMCard(id int64) error {
	return r.db.Model(&model.IMCard{}).Where("id = ?", id).Update("closed", true).Error
}
```

- [ ] **Step 6: 更新表数守卫**

`backend/internal/bootstrap/automigrate_guard_test.go` 里把 `const want = 37` 改成 `const want = 40`。

- [ ] **Step 7: 跑测试确认通过**

Run: `cd backend && go test ./internal/repository/ -run 'TestIM' -v && go test ./internal/bootstrap/ -run 'TestMigrate_CreatesEverySchemaTable|TestSchemaHasOneSourceOfTruth'`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add backend/migrations/0005_im_channel.sql backend/internal/model/im.go backend/internal/repository/im.go backend/internal/repository/im_test.go backend/internal/bootstrap/automigrate_guard_test.go
git commit -m "feat(im): add identity, bind-code and card tables

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: service 接线 —— 建单发卡、终态收卡、`DecideApprovalAs`

**Files:**
- Create: `backend/internal/service/im_approval.go`
- Modify: `backend/internal/service/service.go`（`Services` 加 `IM` 字段；`New()` 建 Hub）
- Modify: `backend/internal/service/external_approval.go`（删掉 `decryptSetting`，它搬进 `im_approval.go`）
- Modify: `backend/internal/service/gateway.go`（`createApproval` 约 499 行、`DecideApproval` 约 560 行、`finalizeApproval` 两个分支、`SweepApprovalTimeouts` 约 1006 行）
- Modify: `backend/internal/service/pipeline.go`（约 692、1335 行）、`export_sensitive.go`（约 55 行）、`exec_window_approval.go`（约 86 行）、`approval_cancel.go`（`CancelApproval`）
- Modify: `backend/internal/handler/admin.go`（`SaveSettings` 写库成功后 `Reload`）
- Modify: `backend/cmd/server/main.go`（约 129 行后 `svc.IM.Start`）
- Modify: `backend/internal/bootstrap/harness_test.go`（`newTestApp` 里 Start + 清理）
- Create: `backend/internal/bootstrap/im_helpers_test.go`
- Test: `backend/internal/bootstrap/im_dispatch_test.go`

**Interfaces:**
- Consumes: Task 1 的 `imchannel.*`、`imtest.*`；Task 2 的 `Repo.IMPlatformIDs / CreateIMCard / OpenIMCards / CloseIMCard / BindIMIdentity`、`model.IMIdentity`
- Produces:
  - `Services.IM *imchannel.Hub`
  - `func (s *Services) DecideApprovalAs(actor *model.User, id int64, approve bool, operator string) (*dto.ExecResp, error)`
  - `func (s *Services) imCardFor(ap *model.Approval) imchannel.ApprovalCard`
  - `func (s *Services) imApprovalCreated(ap *model.Approval)`、`func (s *Services) imApprovalSettled(ap *model.Approval, status, operator string)`
  - `func (s *Services) decryptSetting(key string) string`（原样搬家）
  - 测试辅助 `(*testApp).fakeIM(kind) *imtest.Fake`、`enableIM(token, kind)`、`disableIM(token, kind)`、`bindIM(email, kind, pid) int64`、`flushIM()`、`sentPIDs([]imtest.Sent) []string`

- [ ] **Step 1: 写测试辅助 `bootstrap/im_helpers_test.go`**

```go
package bootstrap

import (
	"sort"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
	"velagateway/internal/model"
)

// fakeIM 登记一个假渠道(能通知、能审批)并立即 Reload。开关仍是关的,用 enableIM 打开。
func (a *testApp) fakeIM(kind string) *imtest.Fake {
	a.t.Helper()
	f := imtest.New(kind)
	a.svc.IM.Register(kind, imchannel.Caps{Notify: true, Approve: true}, f.Builder())
	a.svc.IM.Reload()
	return f
}

// enableIM / disableIM 走设置接口,与管理员在界面上做的一样 —— 保存会触发 Reload。
func (a *testApp) enableIM(token, kind string) {
	a.t.Helper()
	a.setSettings(token, map[string]any{imchannel.Key(kind, "approval"): true})
}

func (a *testApp) disableIM(token, kind string) {
	a.t.Helper()
	a.setSettings(token, map[string]any{imchannel.Key(kind, "approval"): false})
}

// bindIM 直接写绑定表(代绑语义),返回该用户 id。
func (a *testApp) bindIM(email, kind, pid string) int64 {
	a.t.Helper()
	u, err := a.repo.GetUserByEmail(email)
	if err != nil || u == nil {
		a.t.Fatalf("user %s: %v", email, err)
	}
	if err := a.repo.BindIMIdentity(&model.IMIdentity{
		UserID: u.ID, Channel: kind, PlatformUserID: pid, BoundBy: model.IMBoundAdmin,
	}); err != nil {
		a.t.Fatalf("bind %s on %s: %v", email, kind, err)
	}
	return u.ID
}

// flushIM 等 Hub 把已派出的发卡 / 收卡 / 通知全部做完。
func (a *testApp) flushIM() { a.svc.IM.Flush() }

func sentPIDs(sent []imtest.Sent) []string {
	out := make([]string, 0, len(sent))
	for _, s := range sent {
		out = append(out, s.PlatformUserID)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 2: 写失败的测试 `bootstrap/im_dispatch_test.go`**

链上成员（种子数据）：`zhangwei@vela.io`、`lina@vela.io` 是 owner，构成默认审批链；`linwei@vela.io` 是管理员，不在链上；`chenhao@vela.io` 不在链上。

```go
package bootstrap

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIM_CardsGoOnlyToBoundChainMembers(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.bindIM("lina@vela.io", "fake", "p-ln")
	app.bindIM("chenhao@vela.io", "fake", "p-ch") // 绑了,但不在链上

	ap := app.submitProdHighRisk(admin)
	app.flushIM()

	eq(t, fmt.Sprint(sentPIDs(f.Sent())), "[p-ln p-zw]", "only bound chain members get a card")
	for _, s := range f.Sent() {
		eq(t, s.Card.ApNo, ap.ApNo, "card carries the ticket number")
		eq(t, s.Card.Kind, "sql", "an ordinary ticket is a sql card")
	}
}

func TestIM_InitiatorOnTheChainGetsNoCard(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.bindIM("lina@vela.io", "fake", "p-ln")

	app.submitProdHighRisk(owner) // zhangwei 发起,他自己在链上
	app.flushIM()
	eq(t, fmt.Sprint(sentPIDs(f.Sent())), "[p-ln]", "the initiator is never sent their own card")
}

func TestIM_NoBoundMembersMeansNoCardsAndTicketStillPending(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(f.Sent()), 0, "nobody bound, nobody messaged")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "ticket is untouched")
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0,
		"the console path still works")
}

func TestIM_DisabledChannelSendsNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake") // 没有 enableIM
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(f.Sent()), 0, "approval switch off → no cards")
}

func TestIM_CardCommandIsRedacted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	prod := app.connIDByEnv(admin, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod,
		"sql":          `CREATE USER 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' BY '` + pwQuoted + `'`,
		"reason":       "生产加只读账号",
	})
	eq(t, r.Code, 42200, "intercepted for approval")
	app.flushIM()
	if len(f.Sent()) != 1 {
		t.Fatalf("sent %d cards, want 1", len(f.Sent()))
	}
	mustNotContainSecrets(t, "IM card command", []byte(f.Sent()[0].Card.Command))
}

func TestIM_DecisionClosesCardsOnEveryChannel(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	fa, fb := app.fakeIM("fa"), app.fakeIM("fb")
	app.enableIM(admin, "fa")
	app.enableIM(admin, "fb")
	app.bindIM("lina@vela.io", "fa", "a-ln")
	app.bindIM("lina@vela.io", "fb", "b-ln") // 同一个人两个渠道都绑了
	app.bindIM("zhangwei@vela.io", "fa", "a-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(fa.Sent()), 2, "fa: two cards")
	eq(t, len(fb.Sent()), 1, "fb: one card")

	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0, "approve in console")
	app.flushIM()

	eq(t, len(fa.Closed()), 2, "every fa card closed")
	eq(t, len(fb.Closed()), 1, "the fb card closed too")
	for _, c := range append(fa.Closed(), fb.Closed()...) {
		eq(t, c.State.Status, "approved", "closed as approved")
		if !strings.Contains(c.State.Text, "通过") {
			t.Errorf("closed text %q should say who approved", c.State.Text)
		}
	}
}

func TestIM_TimeoutClosesCards(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.setSettings(admin, map[string]any{"approval.onTimeout": "auto-reject", "approval.timeoutMinutes": 0})

	app.submitProdHighRisk(admin)
	app.flushIM()
	app.svc.SweepApprovalTimeouts()
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "expired" {
		t.Fatalf("closed = %+v, want one expired", c)
	}
}

func TestIM_CancelClosesCards(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/cancel", admin, nil).Code, 0, "cancel")
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "cancelled" {
		t.Fatalf("closed = %+v, want one cancelled", c)
	}
}

func TestIM_CardsAreClosedAfterTheChannelIsSwitchedOff(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	app.disableIM(admin, "fake")
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "reject")
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "rejected" {
		t.Fatalf("closed = %+v, want the old card closed as rejected", c)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIM_' -v`
Expected: 编译失败，`app.svc.IM undefined`。

- [ ] **Step 4: 写 `service/im_approval.go`**

```go
package service

import (
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// imSettings 把设置表交给渠道读。Secret 走解密。
type imSettings struct{ s *Services }

func (x imSettings) String(k string) string { return x.s.Repo.SettingString(k, "") }
func (x imSettings) Bool(k string) bool     { return x.s.Repo.SettingBool(k, false) }
func (x imSettings) Secret(k string) string { return x.s.decryptSetting(k) }

// imStore 是 Hub 的持久化,落在 tbl_im_identity / tbl_im_card。
type imStore struct{ repo *repository.Repo }

func (x imStore) PlatformIDs(ch string, ids []int64) (map[int64]string, error) {
	return x.repo.IMPlatformIDs(ch, ids)
}

func (x imStore) RecordCard(c imchannel.Card) error {
	return x.repo.CreateIMCard(&model.IMCard{ApprovalID: c.ApprovalID, Channel: c.Channel, UserID: c.UserID, MsgRef: c.MsgRef})
}

func (x imStore) OpenCards(approvalID int64) ([]imchannel.Card, error) {
	rows, err := x.repo.OpenIMCards(approvalID)
	if err != nil {
		return nil, err
	}
	out := make([]imchannel.Card, 0, len(rows))
	for _, r := range rows {
		out = append(out, imchannel.Card{ID: r.ID, ApprovalID: r.ApprovalID, Channel: r.Channel, UserID: r.UserID, MsgRef: r.MsgRef})
	}
	return out, nil
}

func (x imStore) MarkCardClosed(id int64) error { return x.repo.CloseIMCard(id) }

// newIMHub 造 Hub。出站客户端就是 webhook 用的那个带 SSRF 防护的客户端。
func newIMHub(s *Services) *imchannel.Hub {
	return imchannel.NewHub(imchannel.Deps{
		Settings:    imSettings{s},
		HTTP:        newOutboundClient(),
		ValidateURL: validateOutboundURL,
	}, imStore{s.Repo})
}

// decryptSetting reads a setting and decrypts it (secrets are stored encrypted at
// rest via crypto.EncryptSecret on save). DecryptSecret returns empty/legacy-
// plaintext values unchanged, so a blank setting is safe.
func (s *Services) decryptSetting(key string) string {
	raw := s.Repo.SettingString(key, "")
	if raw == "" {
		return ""
	}
	if v, err := crypto.DecryptSecret(raw); err == nil {
		return v
	}
	return raw
}

func approvalKind(ap *model.Approval) string {
	switch {
	case ap.WindowID > 0:
		return "window"
	case ap.ExportJobID > 0:
		return "export"
	case ap.ReleaseID > 0:
		return "release"
	}
	return "sql"
}

// imCardFor 把工单翻成卡片。命令在**这里**先脱敏再截断(safeClip),渠道拿到的已是能出
// 网关的文本 —— 口令的处理只有这一处,不指望五个渠道各自记得。
func (s *Services) imCardFor(ap *model.Approval) imchannel.ApprovalCard {
	console := ""
	if base := strings.TrimRight(strings.TrimSpace(s.Repo.SettingString("notify.consoleURL", "")), "/"); base != "" {
		console = base + "/#/approvals"
	}
	return imchannel.ApprovalCard{
		ApNo: ap.ApNo, Kind: approvalKind(ap), Env: ap.Env, Tier: ap.TierCode,
		Instance: ap.Instance, Database: ap.Database, Risk: ap.RiskLevel, Initiator: ap.Initiator,
		Reason: clip(ap.Reason, 200), Command: safeClip(ap.Command, 800), ConsoleURL: console,
	}
}

// imApprovalCreated:群通知 + 私信链上成员。发起人本人即使在链上也不发 —— 他点了也会被
// BlockSelf 拒,发给他只会多一个点不动的按钮。
func (s *Services) imApprovalCreated(ap *model.Approval) {
	card := s.imCardFor(ap)
	s.IM.NotifyApproval(card)
	steps, _ := s.Repo.StepsOf(ap.ID)
	seen := map[int64]bool{}
	var recipients []int64
	for _, st := range steps {
		if st.ApproverID == 0 || st.ApproverID == ap.InitiatorID || seen[st.ApproverID] {
			continue
		}
		seen[st.ApproverID] = true
		recipients = append(recipients, st.ApproverID)
	}
	s.IM.ApprovalCreated(ap.ID, card, recipients)
}

// imApprovalSettled 收起这张单在所有渠道上的卡片。status 是工单的新状态。
func (s *Services) imApprovalSettled(ap *model.Approval, status, operator string) {
	s.IM.ApprovalSettled(ap.ID, imchannel.CardState{Status: status, Text: settledText(status, operator)})
}

func settledText(status, operator string) string {
	switch status {
	case model.StatusApproved:
		return "已由 " + operator + " 通过"
	case model.StatusRejected:
		return "已由 " + operator + " 驳回"
	case model.StatusExpired:
		return "已超时作废"
	case model.StatusCancelled:
		return "已撤回"
	}
	return "已处理"
}
```

- [ ] **Step 5: 删掉 `external_approval.go` 里的 `decryptSetting`**

删除 `backend/internal/service/external_approval.go` 中 `func (s *Services) decryptSetting` 整个函数（含注释），并去掉该文件里不再使用的 `velagateway/pkg/crypto` import。

- [ ] **Step 6: `Services` 加字段并在 `New()` 里建 Hub**

`service.go` import 加 `"velagateway/internal/imchannel"`；在 `Webhook  *Dispatcher` 下面加：

```go
	// IM 是即时通讯渠道(飞书、Telegram、钉钉、Slack、企业微信)的 Hub。见 im_approval.go。
	IM *imchannel.Hub
```

在 `New()` 里 `s := &Services{...}` 之后紧接着：

```go
	s.IM = newIMHub(s)
```

- [ ] **Step 7: 抽出 `DecideApprovalAs`**

`gateway.go` 中把 `DecideApproval` 整个替换为：

```go
// DecideApproval is the console path: the operator shown to the initiator is the
// approver's own name.
func (s *Services) DecideApproval(actor *model.User, id int64, approve bool) (*dto.ExecResp, error) {
	name := ""
	if actor != nil {
		name = actor.Name
	}
	return s.DecideApprovalAs(actor, id, approve, name)
}

// DecideApprovalAs 与 DecideApproval 做同一件事,只是 operator 由调用方给 —— IM 渠道批的
// 要在审计里看得出是哪个渠道("李四(飞书)"),否则和控制台批的长得一模一样。
//
// 权限判断一处都没动:能不能决定这张单,仍然只问 DecideBlockFor。
func (s *Services) DecideApprovalAs(actor *model.User, id int64, approve bool, operator string) (*dto.ExecResp, error) {
	ap, err := s.Repo.GetApproval(id)
	if err != nil {
		return nil, ErrNotFound
	}
	// 能不能决定这张单,只有一处判断(DecideBlockFor)—— 它同时喂给待办列表,
	// 所以按钮亮不亮和点下去放不放行,永远说的是同一件事。
	switch block := s.DecideBlockFor(actor, ap); block {
	case BlockNone:
	case BlockNotPending:
		return nil, ErrAlreadyDecided
	default:
		// 理由随错误一起带出去:笼统一句"无权处理"会让人去修错的东西 ——
		// 不在链上要去找管理员,自己发起的要去找同事,两件事完全不同。
		return nil, &DecideRefusal{Block: block}
	}
	// 站内驳回目前没有理由输入框 —— 有了之后从这里传进去,不必再动下面那一层。
	return s.finalizeApproval(ap, approve, operator, "")
}
```

- [ ] **Step 8: 挂上建单与终态**

1. `gateway.go` `createApproval` 里 `s.dispatchExternalApproval(u, ap)` 之后加一行 `s.imApprovalCreated(ap)`。
2. `pipeline.go` 约 693 行 `s.dispatchExternalApproval(creator, ap)` 之后加 `s.imApprovalCreated(ap)`。
3. `export_sensitive.go` 约 56 行 `s.dispatchExternalApproval(u, ap)` 之后加 `s.imApprovalCreated(ap)`。
4. `exec_window_approval.go` 约 87 行 `s.dispatchExternalApproval(u, ap)` 之后加 `s.imApprovalCreated(ap)`。
5. `finalizeApproval` 通过分支：`_ = s.Repo.DecideActiveStep(ap.ID, model.StatusApproved, now)` 之后加 `s.imApprovalSettled(ap, model.StatusApproved, operatorName)`。
6. `finalizeApproval` 驳回分支：`_ = s.Repo.DecideActiveStep(ap.ID, model.StatusRejected, now)` 之后加 `s.imApprovalSettled(ap, model.StatusRejected, operatorName)`。
7. `SweepApprovalTimeouts` 的 `auto-reject` 分支：`s.cancelExternalApproval(a)` 之后加 `s.imApprovalSettled(&a, model.StatusExpired, "")`。
8. `pipeline.go` 约 1335 行 `s.cancelExternalApproval(*ap)` 之后加 `s.imApprovalSettled(ap, model.StatusExpired, "")`。
9. `approval_cancel.go` `CancelApproval` 里 `_ = s.Repo.DecideActiveStep(ap.ID, model.StatusCancelled, now)` 之后加 `s.imApprovalSettled(ap, model.StatusCancelled, actor.Name)`。

- [ ] **Step 9: 设置保存后 Reload；进程与测试夹具里 Start**

`handler/admin.go` `SaveSettings` 中，`h.Repo.SetSettings(prepared)` 成功之后、写审计之前加：

```go
	// IM 渠道的凭据与开关都在设置里。保存即重建渠道、重启长连接,不需要重启进程。
	h.Svc.IM.Reload()
```

`cmd/server/main.go` 在 `svc := service.New(repo, engine, jwtMgr)` 之后加（import `context`，若尚未导入）：

```go
	// IM 渠道:按当前设置建渠道、起长连接。进程活多久它们活多久。
	svc.IM.Start(context.Background())
```

`bootstrap/harness_test.go` `newTestApp` 中 `svc := service.New(...)` 之后加（import `context`）：

```go
	imCtx, imCancel := context.WithCancel(context.Background())
	svc.IM.Start(imCtx)
	// 必须比库的清理更早执行(LIFO,所以注册得更晚):长连接和出站任务都还可能碰库。
	t.Cleanup(func() { imCancel(); svc.IM.Stop() })
```

- [ ] **Step 10: 跑测试确认通过**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIM_' -v`
Expected: PASS（9 个测试）

- [ ] **Step 11: 全量回归**

Run: `cd backend && go vet ./... && go test ./...`
Expected: PASS

- [ ] **Step 12: 提交**

```bash
git add backend/internal/service backend/internal/handler/admin.go backend/cmd/server/main.go backend/internal/bootstrap/harness_test.go backend/internal/bootstrap/im_helpers_test.go backend/internal/bootstrap/im_dispatch_test.go
git commit -m "feat(im): send approval cards on ticket creation and close them on settle

Every ticket-creation path now hands the hub a redacted card and the chain
members who are not the initiator; every settle path (console decision,
timeout, cancel, release abort) closes the cards again. DecideApprovalAs
lets a channel label the operator without touching the decision rules.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: 点击入口 `OnAction` —— 认人，再交回站内判定

**Files:**
- Modify: `backend/internal/service/im_approval.go`（加 `OnAction`、`OnDirectMessage` 占位、`imUser`、`auditIMStranger`）
- Modify: `backend/internal/service/service.go`（`New()` 里 `s.IM.SetSink(s)`）
- Test: `backend/internal/bootstrap/im_action_test.go`

**Interfaces:**
- Consumes: Task 3 的 `DecideApprovalAs`、`imApprovalSettled`；Task 2 的 `Repo.IMIdentityByPlatform`
- Produces:
  - `*Services` 实现 `imchannel.ActionSink`：`OnAction(ctx, imchannel.Action) imchannel.ActionResult`、`OnDirectMessage(ctx, imchannel.DM) string`（本任务返回固定提示，Task 5 实现）
  - `func (s *Services) imUser(channel, platformUserID string) *model.User`
  - 常量 `imTestApNo = "AP-TEST"`（Task 7 的测试卡片用）；`const imBindInvalidReply = "无效或已过期的绑定码,请在控制台重新生成"`

- [ ] **Step 1: 写失败的测试 `bootstrap/im_action_test.go`**

```go
package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/imtest"
)

// runningFake 装好一个开着交互审批、长连接已在跑的假渠道。
func (a *testApp) runningFake(token string) *imtest.Fake {
	a.t.Helper()
	f := a.fakeIM("fake")
	a.enableIM(token, "fake")
	f.WaitRunning(a.t)
	return f
}

func TestIM_UnboundClickIsRefusedAndAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	ap := app.submitProdHighRisk(admin)

	res := f.Click("stranger", ap.ApNo, true)
	if !strings.Contains(res.Toast, "未绑定") {
		t.Fatalf("toast = %q, want an unbound-account message", res.Toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "a stranger's click decides nothing")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "fake:stranger" || rows[0].ApprovalNo != ap.ApNo {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestIM_StrangerWithHugePlatformIDIsStillAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	ap := app.submitProdHighRisk(admin)

	f.Click(strings.Repeat("x", 300), ap.ApNo, true)
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 {
		t.Fatalf("a huge platform id must still leave one audit row, got %d", len(rows))
	}
	if n := len([]rune(rows[0].ActorName)); n > 64 {
		t.Fatalf("actor name is %d runes, column is 64", n)
	}
}

func TestIM_BoundButNotOnChainIsRefusedWithTheChainReason(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("chenhao@vela.io", "fake", "p-ch")
	ap := app.submitProdHighRisk(admin)

	res := f.Click("p-ch", ap.ApNo, true)
	eq(t, res.Toast, "仅审批链成员可处理该工单", "BlockNotInChain reason, verbatim")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "still pending")
}

func TestIM_SelfApprovalFollowsTheSameSettingAsTheConsole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(owner)

	res := f.Click("p-zw", ap.ApNo, true)
	if !strings.Contains(res.Toast, "不能审批自己发起的工单") {
		t.Fatalf("toast = %q, want BlockSelf reason", res.Toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "self-approval blocked")

	// 打开开关后同一次点击放行 —— 证明判定走的是 DecideBlockFor,不是另写的一份。
	app.setSettings(admin, map[string]any{"approval.allowSelfApprove": true})
	f.Click("p-zw", ap.ApNo, true)
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "allowed once the setting says so")
}

func TestIM_DisabledOrServiceAccountBindingIsTreatedAsStranger(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	zw := app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ln := app.bindIM("lina@vela.io", "fake", "p-ln")
	if err := app.repo.UpdateUserFields(zw, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserFields(ln, map[string]any{"kind": "service"}); err != nil {
		t.Fatal(err)
	}
	ap := app.submitProdHighRisk(admin)

	for _, pid := range []string{"p-zw", "p-ln"} {
		if res := f.Click(pid, ap.ApNo, true); !strings.Contains(res.Toast, "未绑定") {
			t.Errorf("%s: toast = %q, want refusal as unbound", pid, res.Toast)
		}
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "still pending")
}

func TestIM_ChainMemberApprovesAndTheAuditNamesTheChannel(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin)

	res := f.Click("p-zw", ap.ApNo, true)
	eq(t, res.Toast, "已批准,请发起人回控制台执行", "approve toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved")
	rows, _ := app.repo.AuditByOperatorLike("%(fake)")
	if len(rows) != 1 || rows[0].ApprovalNo != ap.ApNo {
		t.Fatalf("audit rows with channel label = %+v", rows)
	}

	// 重投 / 连点:回显状态,不再决策一次
	again := f.Click("p-zw", ap.ApNo, true)
	if !strings.Contains(again.Toast, "已处理") {
		t.Fatalf("second click toast = %q", again.Toast)
	}
	rows, _ = app.repo.AuditByOperatorLike("%(fake)")
	eq(t, len(rows), 1, "the duplicate click wrote no second decision")
}

func TestIM_RejectClick(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")
	ap := app.submitProdHighRisk(admin)

	eq(t, f.Click("p-ln", ap.ApNo, false).Toast, "已驳回", "reject toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "rejected", "rejected")
}

func TestIM_ClickAfterConsoleDecisionEchoesState(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")
	ap := app.submitProdHighRisk(admin)
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "console reject")

	if res := f.Click("p-ln", ap.ApNo, true); !strings.Contains(res.Toast, "已处理") {
		t.Fatalf("toast = %q, want already-handled", res.Toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "rejected", "the console decision stands")
}

func TestIM_ClickOnUnknownTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-ln")

	if res := f.Click("p-ln", "AP-999999", true); !strings.Contains(res.Toast, "工单不存在") {
		t.Fatalf("toast = %q", res.Toast)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIM_' -v`
Expected: 编译通过（`OnAction` 尚未存在不影响测试编译），新测试 FAIL——`f.Click` 返回空 toast，因为 Hub 没有 sink，长连接没起来（`WaitRunning` 超时）。

- [ ] **Step 3: 实现 `OnAction` 等（追加到 `service/im_approval.go`）**

import 增加 `"context"`、`"errors"`、`"log/slog"`。

```go
// imTestApNo 是测试卡片上的单号。点它只回一句话,不碰任何工单。
const imTestApNo = "AP-TEST"

const imBindInvalidReply = "无效或已过期的绑定码,请在控制台重新生成"

// OnAction 是所有渠道按钮点击的唯一入口。
//
// 这里新增的判断只有一条:这个平台账号能不能认成一个启用的真人网关账户。认出来之后,
// 能不能批完全交给 DecideApprovalAs → DecideBlockFor,和控制台走同一条路。
func (s *Services) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	label := imchannel.Label(a.Channel)
	u := s.imUser(a.Channel, a.PlatformUserID)
	if u == nil {
		s.auditIMStranger(a)
		return imchannel.ActionResult{Toast: "你的" + label + "账号未绑定网关账户,请先在控制台「IM 账号绑定」中完成绑定"}
	}
	if a.ApNo == imTestApNo {
		return imchannel.ActionResult{Toast: "测试卡片 · 点击已送达网关,认出的账户是 " + u.Name}
	}
	ap, err := s.Repo.GetApprovalByApNo(a.ApNo)
	if err != nil || ap == nil {
		return imchannel.ActionResult{Toast: "工单不存在:" + clip(a.ApNo, 32)}
	}
	_, err = s.DecideApprovalAs(u, ap.ID, a.Approve, clip(u.Name+"("+label+")", 127))
	var refusal *DecideRefusal
	switch {
	case err == nil:
		if a.Approve {
			return imchannel.ActionResult{Toast: "已批准,请发起人回控制台执行"}
		}
		return imchannel.ActionResult{Toast: "已驳回"}
	case errors.Is(err, ErrAlreadyDecided):
		status := ap.Status
		if cur, e := s.Repo.GetApproval(ap.ID); e == nil {
			status = cur.Status
		}
		return imchannel.ActionResult{Toast: "该工单已处理(" + approvalStatusLabel(status) + ")"}
	case errors.As(err, &refusal):
		return imchannel.ActionResult{Toast: refusal.Block.Reason()}
	default:
		slog.Error("im action failed", "channel", a.Channel, "apNo", ap.ApNo, "err", err)
		return imchannel.ActionResult{Toast: "处理失败,请回控制台重试"}
	}
}

// OnDirectMessage 在 Task 5 里实现绑定;在那之前,任何私聊都只得到这句通用回复。
func (s *Services) OnDirectMessage(_ context.Context, _ imchannel.DM) string {
	return imBindInvalidReply
}

// imUser 把平台账号认成网关账户。停用的、服务账号一律认不出 —— 与登录的规则一致。
func (s *Services) imUser(channel, pid string) *model.User {
	id, err := s.Repo.IMIdentityByPlatform(channel, pid)
	if err != nil || id == nil {
		return nil
	}
	u, err := s.Repo.GetUserByID(id.UserID)
	if err != nil || u == nil || u.Status != "active" || u.Kind == model.UserKindService {
		return nil
	}
	return u
}

// auditIMStranger 记下一次认不出是谁的点击。
//
// 与失败登录把输入的邮箱记进 actor 是同一个取舍:认不出是谁,也要留下「有人试过」。一个
// 陌生账号反复点生产库工单的按钮,是需要被看见的事。平台 ID 来自外部,按列宽截断 ——
// 由外部输入决定审计写不写得进去,这件事本身就是问题。
func (s *Services) auditIMStranger(a imchannel.Action) {
	verb := "驳回"
	if a.Approve {
		verb = "通过"
	}
	actor := &model.User{Name: clip(a.Channel+":"+a.PlatformUserID, 63)}
	s.recordAuditBy(actor, imchannel.Label(a.Channel)+"(未绑定)", nil,
		"im.action "+verb+" "+clip(a.ApNo, 32), model.RiskMid, model.ResultRejected, clip(a.ApNo, 31), "")
}

func approvalStatusLabel(status string) string {
	switch status {
	case model.StatusApproved:
		return "已通过"
	case model.StatusRejected:
		return "已驳回"
	case model.StatusExpired:
		return "已超时"
	case model.StatusCancelled:
		return "已撤回"
	}
	return status
}
```

注意：`recordAuditBy` 在 `actor.ID == 0` 时是否可用，参考 `auditLoginFail` 已有同样用法（`&model.User{Name: email}`），可以。

- [ ] **Step 4: 在 `New()` 里挂上 sink**

`service.go` `New()` 中 `s.IM = newIMHub(s)` 后面加：

```go
	s.IM.SetSink(s) // 点击与私聊回到 service:认人、判定、绑定都在这一侧
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIM_' -v`
Expected: PASS（Task 3 的 9 个 + 本任务 9 个）

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service backend/internal/bootstrap/im_action_test.go
git commit -m "feat(im): resolve a click to a gateway account, then reuse the console's decision

The only new check is whether the platform account maps to an active human
account; everything else is DecideBlockFor, so a channel can never approve
what the console would refuse. Unrecognised clicks are audited with the
platform id, clipped to the column width.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 5: 一次性绑定码、代绑 / 解绑与绑定接口

**Files:**
- Create: `backend/internal/service/im_bind.go`
- Modify: `backend/internal/service/im_approval.go`（删掉 Task 4 的 `OnDirectMessage` 占位）
- Modify: `backend/internal/service/gateway.go`（`SweepApprovalTimeouts` 开头顺手清过期码）
- Create: `backend/internal/handler/im.go`
- Modify: `backend/internal/bootstrap/router.go`（6 条路由）
- Modify: `backend/docs/openapi.yaml`（6 个条目）
- Test: `backend/internal/bootstrap/im_bind_test.go`

**Interfaces:**
- Consumes: Task 2 的 `Repo.PutIMBindCode / ConsumeIMBindCode / PurgeExpiredIMBindCodes / BindIMIdentity / UnbindIMIdentity / IMIdentitiesOfUser`、`repository.ErrIMIdentityTaken`；Task 1 的 `Hub.CanApprove / ApprovalEnabled / ApproveKinds / BotName`
- Produces:
  - `var ErrIMChannelOff`（service）
  - `type IMIdentityView struct{ Channel, Label string; Enabled, Bound bool; PlatformUserID, BoundBy, BotName string; BoundAt *time.Time }`（JSON：`channel label enabled bound platformUserId boundBy botName boundAt`）
  - `func (s *Services) IMIdentities(userID int64) []IMIdentityView`
  - `func (s *Services) IssueIMBindCode(u *model.User, channel string) (code string, expiresAt time.Time, err error)`
  - `func (s *Services) UnbindOwnIM(u *model.User, channel string) error`
  - `func (s *Services) AdminBindIM(actor *model.User, userID int64, channel, platformUserID string) error`
  - `func (s *Services) AdminUnbindIM(actor *model.User, userID int64, channel string) error`
  - `(*Services).OnDirectMessage` 的真实实现
  - 路由：`GET /me/im-identities`、`POST /me/im-identities/:channel/code`、`DELETE /me/im-identities/:channel`、`GET /users/:id/im-identities`（perms）、`PUT /users/:id/im-identities/:channel`（perms · admin，body `{platformUserId}`）、`DELETE /users/:id/im-identities/:channel`（perms · admin）
  - 响应：`POST …/code` → `{code, expiresAt, botName}`

spec §10 没有列出管理员读某人绑定的 `GET /users/:id/im-identities`，但权限页要显示绑定状态，所以这里补上，guard 与 `GET /users/:id/tags` 相同。

- [ ] **Step 1: 写失败的测试 `bootstrap/im_bind_test.go`**

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

var bindCodeShape = regexp.MustCompile(`^BIND-[A-HJ-NP-Z2-9]{8}$`)

type imIdentityRow struct {
	Channel        string `json:"channel"`
	Enabled        bool   `json:"enabled"`
	Bound          bool   `json:"bound"`
	PlatformUserID string `json:"platformUserId"`
	BoundBy        string `json:"boundBy"`
	BotName        string `json:"botName"`
}

func (a *testApp) issueBindCode(token, channel string) (string, apiResp) {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/me/im-identities/"+channel+"/code", token, nil)
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Code, r
}

func (a *testApp) myIdentity(token, channel string) imIdentityRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/me/im-identities", token, nil)
	eq(a.t, r.Code, 0, "list my identities")
	var rows []imIdentityRow
	_ = json.Unmarshal(r.Data, &rows)
	for _, row := range rows {
		if row.Channel == channel {
			return row
		}
	}
	a.t.Fatalf("channel %s not listed: %s", channel, r.Data)
	return imIdentityRow{}
}

func TestIMBind_SelfServiceHappyPath(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	row := app.myIdentity(me, "fake")
	if !row.Enabled || row.Bound || row.BotName != "fake-bot" {
		t.Fatalf("before binding: %+v", row)
	}
	code, r := app.issueBindCode(me, "fake")
	eq(t, r.Code, 0, "issue code")
	if !bindCodeShape.MatchString(code) {
		t.Fatalf("code %q has the wrong shape", code)
	}

	reply := f.DirectMessage("p-zw", code)
	if !strings.Contains(reply, "zhangwei@vela.io") {
		t.Fatalf("reply = %q, want it to name the bound account", reply)
	}
	row = app.myIdentity(me, "fake")
	if !row.Bound || row.PlatformUserID != "p-zw" || row.BoundBy != model.IMBoundSelf {
		t.Fatalf("after binding: %+v", row)
	}
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "im.bind channel=fake%").Count(&n)
	eq(t, n, int64(1), "self-binding is audited")

	// 绑上以后这个账号的点击就能被认出来
	ap := app.submitProdHighRisk(admin)
	eq(t, f.Click("p-zw", ap.ApNo, true).Toast, "已批准,请发起人回控制台执行", "the bound account can now approve")
}

func TestIMBind_CodeIsSingleUseAndRegenerationInvalidatesTheOldOne(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	old, _ := app.issueBindCode(me, "fake")
	fresh, _ := app.issueBindCode(me, "fake")
	eq(t, f.DirectMessage("p-1", old), "无效或已过期的绑定码,请在控制台重新生成", "the replaced code is dead")
	if !strings.Contains(f.DirectMessage("p-1", fresh), "已绑定") {
		t.Fatal("the fresh code binds")
	}
	eq(t, f.DirectMessage("p-2", fresh), "无效或已过期的绑定码,请在控制台重新生成", "a used code is dead")
	eq(t, app.myIdentity(me, "fake").PlatformUserID, "p-1", "the second use did not rebind")
}

func TestIMBind_ExpiredCode(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	code, _ := app.issueBindCode(me, "fake")
	app.repo.DB().Model(&model.IMBindCode{}).Where("channel = ?", "fake").
		Update("expires_at", time.Now().Add(-time.Minute))
	eq(t, f.DirectMessage("p-1", code), "无效或已过期的绑定码,请在控制台重新生成", "expired")
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("an expired code must not bind")
	}
}

func TestIMBind_CodeIsNormalisedButNotFishedOutOfText(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	code, _ := app.issueBindCode(me, "fake")
	eq(t, f.DirectMessage("p-1", "我的码是 "+code), "无效或已过期的绑定码,请在控制台重新生成",
		"a code buried in a sentence is not accepted")
	eq(t, f.DirectMessage("p-1", "hello"), "无效或已过期的绑定码,请在控制台重新生成",
		"random text gets the same reply as a dead code — nothing leaks")
	if !strings.Contains(f.DirectMessage("p-1", "  "+strings.ToLower(code)+"\n"), "已绑定") {
		t.Fatal("case and surrounding whitespace are forgiven")
	}
}

func TestIMBind_PlatformAccountAlreadyTakenIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-shared")

	code, _ := app.issueBindCode(me, "fake")
	if reply := f.DirectMessage("p-shared", code); !strings.Contains(reply, "已绑定到其他网关账户") {
		t.Fatalf("reply = %q", reply)
	}
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("must not steal someone else's platform account")
	}
}

func TestIMBind_CodeRefusedWhileChannelIsOff(t *testing.T) {
	app := newTestApp(t)
	me := app.login("zhangwei@vela.io", "vela123")
	app.fakeIM("fake") // 登记了但没打开

	_, r := app.issueBindCode(me, "fake")
	if r.Code == 0 {
		t.Fatal("issuing a code for a channel nobody is listening on must fail")
	}
	_, r = app.issueBindCode(me, "nope")
	eq(t, r.Code, 40400, "unknown channel")
}

func TestIMBind_SelfUnbind(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	eq(t, app.do(http.MethodDelete, "/api/v1/me/im-identities/fake", me, nil).Code, 0, "unbind")
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("still bound after unbind")
	}
}

func TestIMBind_AdminBindAndUnbind(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	nonAdmin := app.login("zhangwei@vela.io", "vela123")
	app.fakeIM("fake") // 代绑不经过机器人,渠道关着也能做
	target := app.userIDByEmail(admin, "lina@vela.io")
	path := "/api/v1/users/" + itoa(target) + "/im-identities/fake"

	if app.do(http.MethodPut, path, nonAdmin, map[string]any{"platformUserId": "p-x"}).Code == 0 {
		t.Fatal("a non-admin must not bind someone else")
	}
	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"platformUserId": "p-ln"}).Code, 0, "admin binds")

	r := app.do(http.MethodGet, "/api/v1/users/"+itoa(target)+"/im-identities", admin, nil)
	eq(t, r.Code, 0, "admin reads")
	if !strings.Contains(string(r.Data), `"platformUserId":"p-ln"`) || !strings.Contains(string(r.Data), `"boundBy":"admin"`) {
		t.Fatalf("identities = %s", r.Data)
	}
	if app.do(http.MethodPut, path, admin, map[string]any{"platformUserId": "   "}).Code == 0 {
		t.Fatal("a blank platform id is not a binding")
	}
	eq(t, app.do(http.MethodDelete, path, admin, nil).Code, 0, "admin unbinds")
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "admin.im.%").Count(&n)
	eq(t, n, int64(2), "bind and unbind are both audited")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIMBind_' -v`
Expected: FAIL（路由 404，`OnDirectMessage` 只回通用提示）。

- [ ] **Step 3: 写 `service/im_bind.go`**

```go
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// ErrIMChannelOff:渠道的交互审批没打开(机器人没在跑),生成的码发出去没人收。
var ErrIMChannelOff = fmt.Errorf("im channel approval is off")

const (
	imBindTTL      = 10 * time.Minute
	bindCodeAlpha  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去掉 0/O、1/I:念给别人听时不出错
	bindCodePrefix = "BIND-"
)

var bindCodeRe = regexp.MustCompile(`^BIND-[A-HJ-NP-Z2-9]{8}$`)

type IMIdentityView struct {
	Channel        string     `json:"channel"`
	Label          string     `json:"label"`
	Enabled        bool       `json:"enabled"`
	Bound          bool       `json:"bound"`
	PlatformUserID string     `json:"platformUserId,omitempty"`
	BoundBy        string     `json:"boundBy,omitempty"`
	BotName        string     `json:"botName,omitempty"`
	BoundAt        *time.Time `json:"boundAt,omitempty"`
}

func newBindCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, v := range b {
		out[i] = bindCodeAlpha[int(v)%len(bindCodeAlpha)] // 256 % 32 == 0,没有取模偏差
	}
	return bindCodePrefix + string(out), nil
}

func hashBindCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// IMIdentities 列出每个能做交互审批的渠道,以及这个人在上面绑没绑。
func (s *Services) IMIdentities(userID int64) []IMIdentityView {
	rows, _ := s.Repo.IMIdentitiesOfUser(userID)
	byCh := map[string]model.IMIdentity{}
	for _, r := range rows {
		byCh[r.Channel] = r
	}
	kinds := s.IM.ApproveKinds()
	out := make([]IMIdentityView, 0, len(kinds))
	for _, k := range kinds {
		v := IMIdentityView{Channel: k, Label: imchannel.Label(k), Enabled: s.IM.ApprovalEnabled(k), BotName: s.IM.BotName(k)}
		if r, ok := byCh[k]; ok {
			at := r.CreatedAt
			v.Bound, v.PlatformUserID, v.BoundBy, v.BoundAt = true, r.PlatformUserID, r.BoundBy, &at
		}
		out = append(out, v)
	}
	return out
}

// IssueIMBindCode 生成一个一次性码,只在这里回显一次。
//
// 渠道没打开时拒绝:机器人没在跑,码发出去没人收,表现就是「按了没反应」—— 这类静默
// 失败最难排查,不如当场说清楚。
func (s *Services) IssueIMBindCode(u *model.User, channel string) (string, time.Time, error) {
	if !s.IM.CanApprove(channel) {
		return "", time.Time{}, ErrNotFound
	}
	if !s.IM.ApprovalEnabled(channel) {
		return "", time.Time{}, ErrIMChannelOff
	}
	code, err := newBindCode()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(imBindTTL)
	if err := s.Repo.PutIMBindCode(&model.IMBindCode{UserID: u.ID, Channel: channel, CodeHash: hashBindCode(code), ExpiresAt: exp}); err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

// OnDirectMessage 处理私聊里发来的绑定码。
//
// 发送者的平台 ID 由平台保证(签名 / 连接本身),伪造不了 —— 所以「用自己的账号把码发给
// 机器人」就证明了这个平台账号归申请人所有。不匹配的一律回同一句话,不透露码存不存在、
// 过没过期。
func (s *Services) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	code := strings.ToUpper(strings.TrimSpace(dm.Text))
	if !bindCodeRe.MatchString(code) {
		return imBindInvalidReply
	}
	uid, ok, err := s.Repo.ConsumeIMBindCode(dm.Channel, hashBindCode(code), time.Now())
	if err != nil || !ok {
		return imBindInvalidReply
	}
	u, err := s.Repo.GetUserByID(uid)
	if err != nil || u == nil || u.Status != "active" || u.Kind == model.UserKindService {
		return imBindInvalidReply
	}
	label := imchannel.Label(dm.Channel)
	err = s.Repo.BindIMIdentity(&model.IMIdentity{UserID: uid, Channel: dm.Channel, PlatformUserID: dm.PlatformUserID, BoundBy: model.IMBoundSelf})
	if errors.Is(err, repository.ErrIMIdentityTaken) {
		return "这个" + label + "账号已绑定到其他网关账户,请联系管理员"
	}
	if err != nil {
		return "绑定失败,请稍后重试"
	}
	s.auditAdminAction(u, "im.bind channel="+dm.Channel+" user="+u.Email+" by=self")
	return "已绑定到 " + u.Name + "(" + u.Email + ")"
}

func (s *Services) UnbindOwnIM(u *model.User, channel string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	if ok, err := s.Repo.UnbindIMIdentity(u.ID, channel); err != nil {
		return err
	} else if ok {
		s.auditAdminAction(u, "im.unbind channel="+channel+" user="+u.Email+" by=self")
	}
	return nil
}

// AdminBindIM 管理员声明「这个平台账号是这个人的」。与代绑 MFA 同一个信任假设。
func (s *Services) AdminBindIM(actor *model.User, userID int64, channel, pid string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	pid = strings.TrimSpace(pid)
	if pid == "" || len(pid) > 128 {
		return ErrBadRequest
	}
	u, err := s.Repo.GetUserByID(userID)
	if err != nil || u == nil {
		return ErrNotFound
	}
	if err := s.Repo.BindIMIdentity(&model.IMIdentity{UserID: u.ID, Channel: channel, PlatformUserID: pid, BoundBy: model.IMBoundAdmin}); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.im.bind channel="+channel+" user="+u.Email+" platformUserId="+clip(pid, 64))
	return nil
}

func (s *Services) AdminUnbindIM(actor *model.User, userID int64, channel string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	u, err := s.Repo.GetUserByID(userID)
	if err != nil || u == nil {
		return ErrNotFound
	}
	if _, err := s.Repo.UnbindIMIdentity(u.ID, channel); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.im.unbind channel="+channel+" user="+u.Email)
	return nil
}
```

同时删除 `im_approval.go` 里 Task 4 的 `OnDirectMessage` 占位（`imBindInvalidReply` 常量保留在 `im_approval.go`）。

- [ ] **Step 4: 过期码随审批超时扫描一起清**

`gateway.go` `SweepApprovalTimeouts` 函数体第一行加：

```go
	// 过期的 IM 绑定码搭这一轮清掉 —— 它们本来就不能用了,留着只是占地方。
	_, _ = s.Repo.PurgeExpiredIMBindCodes(time.Now())
```

- [ ] **Step 5: 写 `handler/im.go`**

```go
package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// imFail 把 IM 相关的错误说成人话。
func imFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeNotFound, "渠道或用户不存在")
	case errors.Is(err, service.ErrIMChannelOff):
		resp.Fail(c, resp.CodeBadRequest, "该渠道未启用交互审批,机器人不在运行,请联系管理员")
	case errors.Is(err, repository.ErrIMIdentityTaken):
		resp.Fail(c, resp.CodeBadRequest, "该平台账号已绑定到其他网关账户")
	case errors.Is(err, service.ErrBadRequest):
		resp.Fail(c, resp.CodeBadRequest, "平台账号 ID 不能为空,且不超过 128 个字符")
	default:
		resp.Fail(c, resp.CodeInternalError, "操作失败")
	}
}

func (h *Handler) MyIMIdentities(c *gin.Context) {
	resp.OK(c, h.Svc.IMIdentities(middleware.CurrentUser(c).ID))
}

func (h *Handler) IssueMyIMBindCode(c *gin.Context) {
	ch := c.Param("channel")
	code, exp, err := h.Svc.IssueIMBindCode(middleware.CurrentUser(c), ch)
	if err != nil {
		imFail(c, err)
		return
	}
	// 码只在这里出现一次。库里只有它的哈希,没有第二个接口能再取。
	resp.OK(c, gin.H{"code": code, "expiresAt": exp, "botName": h.Svc.IM.BotName(ch)})
}

func (h *Handler) UnbindMyIM(c *gin.Context) {
	if err := h.Svc.UnbindOwnIM(middleware.CurrentUser(c), c.Param("channel")); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) UserIMIdentities(c *gin.Context) {
	resp.OK(c, h.Svc.IMIdentities(pathID(c)))
}

func (h *Handler) BindUserIM(c *gin.Context) {
	var body struct {
		PlatformUserID string `json:"platformUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.AdminBindIM(middleware.CurrentUser(c), pathID(c), c.Param("channel"), strings.TrimSpace(body.PlatformUserID)); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) UnbindUserIM(c *gin.Context) {
	if err := h.Svc.AdminUnbindIM(middleware.CurrentUser(c), pathID(c), c.Param("channel")); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}
```

执行前先确认 `resp.CodeInternalError` 存在（`admin.go` 已在用）。

- [ ] **Step 6: 注册路由**

`router.go` 认证组里，`a.POST("/auth/mfa/disable", h.MFADisable)` 之后加：

```go
		// IM 账号绑定是每个人自己的事(和 MFA 自助绑定一样),不挂菜单闸。
		a.GET("/me/im-identities", h.MyIMIdentities)
		a.POST("/me/im-identities/:channel/code", h.IssueMyIMBindCode)
		a.DELETE("/me/im-identities/:channel", h.UnbindMyIM)
```

在 `a.POST("/users/:id/mfa/bind", …)` 之后加：

```go
		a.GET("/users/:id/im-identities", menu("perms"), h.UserIMIdentities)
		a.PUT("/users/:id/im-identities/:channel", menu("perms"), admin, h.BindUserIM)
		a.DELETE("/users/:id/im-identities/:channel", menu("perms"), admin, h.UnbindUserIM)
```

- [ ] **Step 7: 补 OpenAPI**

`backend/docs/openapi.yaml`：在 `/auth/mfa/disable` 条目之后加

```yaml
  /me/im-identities:
    get:
      tags: [auth]
      summary: 我在各交互审批渠道上的绑定状态
      responses: { '200': { $ref: '#/components/responses/Ok' } }
  /me/im-identities/{channel}/code:
    post:
      tags: [auth]
      summary: 生成一次性绑定码(10 分钟,只回显这一次;渠道未启用时拒绝)
      parameters: [{ name: channel, in: path, required: true, schema: { type: string } }]
      responses: { '200': { $ref: '#/components/responses/Ok' } }
  /me/im-identities/{channel}:
    delete:
      tags: [auth]
      summary: 解绑自己在该渠道的账号
      parameters: [{ name: channel, in: path, required: true, schema: { type: string } }]
      responses: { '200': { $ref: '#/components/responses/Ok' } }
```

在 `/users/{id}/mfa/bind` 条目之后加

```yaml
  /users/{id}/im-identities:
    get:
      tags: [users]
      summary: '[perms] 某人在各交互审批渠道上的绑定状态'
      parameters: [{ name: id, in: path, required: true, schema: { type: integer } }]
      responses: { '200': { $ref: '#/components/responses/Ok' } }
  /users/{id}/im-identities/{channel}:
    put:
      tags: [users]
      summary: '[perms · admin] 代绑平台账号 {platformUserId}(写审计)'
      parameters:
        - { name: id, in: path, required: true, schema: { type: integer } }
        - { name: channel, in: path, required: true, schema: { type: string } }
      responses: { '200': { $ref: '#/components/responses/Ok' } }
    delete:
      tags: [users]
      summary: '[perms · admin] 解绑(写审计)'
      parameters:
        - { name: id, in: path, required: true, schema: { type: integer } }
        - { name: channel, in: path, required: true, schema: { type: string } }
      responses: { '200': { $ref: '#/components/responses/Ok' } }
```

执行前先 `grep -n "/auth/mfa/disable:\|/users/{id}/mfa/bind:" backend/docs/openapi.yaml` 定位，并照抄邻近条目的 `tags` 取值（若不是 `auth`/`users` 就用邻居的）。

- [ ] **Step 8: 跑测试确认通过**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIMBind_|TestIM_|TestOpenAPI' -v`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add backend/internal/service/im_bind.go backend/internal/service/im_approval.go backend/internal/service/gateway.go backend/internal/handler/im.go backend/internal/bootstrap/router.go backend/docs/openapi.yaml backend/internal/bootstrap/im_bind_test.go
git commit -m "feat(im): bind platform accounts with one-time codes sent to the bot

A user proves a platform account is theirs by sending a short-lived code
to the bot from it; the platform vouches for the sender. Codes are hashed,
single-use and replaced on regeneration, and every mismatch gets the same
reply. Admins can bind or unbind on someone's behalf, both audited.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: 飞书群通知迁入新框架（`imchannel/lark` + 配置键迁移 + 测试通知接口）

**Files:**
- Create: `backend/internal/imchannel/lark/lark.go`
- Test: `backend/internal/imchannel/lark/lark_test.go`
- Create: `backend/migrations/0006_im_lark_notify_keys.sql`
- Modify: `backend/internal/service/service.go`（`New()` 里登记飞书渠道）
- Modify: `backend/internal/service/webhook.go`（删除 `SendLarkApproval`、`TestLark`、`postLark`、`larkSign`、`larkSafe`、`dbOrDefault`、`larkApprovalCard` 及只被它们用到的 import）
- Modify: `backend/internal/service/sanitize_test.go`（删除 `TestLarkSafe_NeutralizesBackticks`，它搬进 lark 包）
- Modify: `backend/internal/service/gateway.go`、`pipeline.go`、`export_sensitive.go`、`exec_window_approval.go`（删除 4 处 `s.Webhook.SendLarkApproval(ap)`）
- Modify: `backend/internal/service/im_approval.go`（加 `TestIMNotify`）
- Modify: `backend/internal/handler/admin.go`（设置校验与加密规则、删除 `TestLark` handler）
- Modify: `backend/internal/handler/im.go`（加 `TestIMNotify` handler）
- Modify: `backend/internal/bootstrap/router.go`（删 `/settings/lark/test`，加 `/settings/im/:channel/test-notify`）
- Modify: `backend/internal/bootstrap/seed.go`（删掉死键 `notify.larkChannel`）
- Modify: `backend/docs/openapi.yaml`
- Rewrite: `backend/internal/bootstrap/lark_test.go`、`backend/internal/bootstrap/settings_secret_test.go`
- Modify: `backend/internal/bootstrap/settings_persist_test.go`（非法 URL 那条改用 `im.lark.notifyWebhook`）
- Test: `backend/internal/bootstrap/im_migration_test.go`

**Interfaces:**
- Consumes: Task 1 的 `imchannel.Deps / Notifier / ApprovalCard / Key`；Task 3 的 `imCardFor`
- Produces:
  - `lark.New(d imchannel.Deps) imchannel.Channel`（`im.lark.notifyWebhook` 为空时返回 nil）；`*lark.Channel` 实现 `Notifier`
  - `func (s *Services) TestIMNotify(channel string) (ok bool, msg string)`
  - 路由 `POST /settings/im/:channel/test-notify`（settings · admin），响应 `{ok, message}`
  - 测试辅助 `rerunMigration(t *testing.T, db *gorm.DB, name string)`（`im_migration_test.go`，Task 8 复用）

- [ ] **Step 1: 写失败的协议层测试 `imchannel/lark/lark_test.go`**

```go
package lark

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{Settings: s, HTTP: http.DefaultClient, ValidateURL: func(string) error { return nil }}
}

func TestNew_UnconfiguredIsNil(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("no webhook → nil channel, got %#v", ch)
	}
}

func TestNotifyApproval_PostsASignedInteractiveCard(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	ch := New(deps(kv{"im.lark.notifyWebhook": srv.URL, "im.lark.notifySecret": "s"})).(imchannel.Notifier)
	err := ch.NotifyApproval(context.Background(), imchannel.ApprovalCard{
		ApNo: "AP-1", Instance: "order", Env: "prod", Risk: "high", Initiator: "张三",
		Command: "SELECT * FROM `t`; ``` <x>", Reason: "r`x", ConsoleURL: "https://gw/#/approvals",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"msg_type":"interactive"`, `"sign"`, "AP-1", "https://gw/#/approvals"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "``` <x>") || strings.Contains(body, "r`x") {
		t.Errorf("backticks must be neutralised so the code fence cannot be closed: %s", body)
	}
}

func TestNotifyApproval_ReportsLarkErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer srv.Close()
	ch := New(deps(kv{"im.lark.notifyWebhook": srv.URL})).(imchannel.Notifier)
	err := ch.NotifyApproval(context.Background(), imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "sign match fail") {
		t.Fatalf("err = %v, want the lark message", err)
	}
}

func TestNotifyApproval_HonoursTheOutboundGuard(t *testing.T) {
	d := deps(kv{"im.lark.notifyWebhook": "https://169.254.169.254/hook"})
	d.ValidateURL = func(string) error { return errors.New("blocked") }
	err := New(d).(imchannel.Notifier).NotifyApproval(context.Background(), imchannel.ApprovalCard{})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/lark/`
Expected: 编译失败，`undefined: New`。

- [ ] **Step 3: 写 `imchannel/lark/lark.go`**

卡片渲染、签名、反引号中和都是从 `service/webhook.go` 原样搬来的；输入从 `*model.Approval` 换成 `imchannel.ApprovalCard`（命令已由通用层脱敏截断）。

```go
// Package lark 是飞书渠道。第 1 期只做群通知(自定义机器人 webhook);交互审批在第 3 期
// 接飞书开放平台应用 + 长连接。
package lark

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/imchannel"
)

type Channel struct {
	webhook  string
	secret   string
	client   *http.Client
	validate func(string) error
}

// New 在配置了群通知 webhook 时返回渠道,否则返回 nil(接口 nil,不是类型化的 nil 指针)。
func New(d imchannel.Deps) imchannel.Channel {
	webhook := strings.TrimSpace(d.Settings.String(imchannel.Key(imchannel.Lark, "notifyWebhook")))
	if webhook == "" {
		return nil
	}
	return &Channel{
		webhook:  webhook,
		secret:   d.Settings.Secret(imchannel.Key(imchannel.Lark, "notifySecret")),
		client:   d.HTTP,
		validate: d.ValidateURL,
	}
}

func (c *Channel) Kind() string { return imchannel.Lark }

func (c *Channel) NotifyApproval(ctx context.Context, card imchannel.ApprovalCard) error {
	return c.post(ctx, approvalCard(card))
}

// post 把 {msg_type:interactive, card} 发给自定义机器人;配了密钥就加签。
func (c *Channel) post(ctx context.Context, card any) error {
	if err := c.validate(c.webhook); err != nil {
		return err
	}
	payload := map[string]any{"msg_type": "interactive", "card": card}
	if c.secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = sign(ts, c.secret)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// 飞书成功时回 HTTP 200 + {"code":0};code 非零就是失败。
	var lr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &lr)
	if resp.StatusCode < 300 && lr.Code == 0 {
		return nil
	}
	if lr.Msg != "" {
		return fmt.Errorf("飞书返回 %d: %s", lr.Code, lr.Msg)
	}
	return fmt.Errorf("飞书返回 %s", resp.Status)
}

// sign 是自定义机器人的签名:base64(HMAC-SHA256(key, "")),key = "<timestamp>\n<secret>"。
func sign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// safe 中和反引号,免得嵌在 lark_md 代码块里的命令 / 原因把代码块闭合、注入富文本
// (假按钮、假链接)。代码块里没有转义机制,所以换成外观相同的 U+02CB(C10)。
func safe(s string) string { return strings.ReplaceAll(s, "`", "ˋ") }

func dbOrDefault(db string) string {
	if strings.TrimSpace(db) == "" {
		return "默认"
	}
	return db
}

func approvalCard(c imchannel.ApprovalCard) map[string]any {
	tmpl, riskLabel := "orange", "🟠 中危"
	if c.Risk == "high" {
		tmpl, riskLabel = "red", "🔴 高危"
	}
	shortField := func(k, v string) map[string]any {
		return map[string]any{"is_short": true, "text": map[string]any{"tag": "lark_md", "content": "**" + k + "**\n" + safe(v)}}
	}
	elements := []any{
		map[string]any{"tag": "div", "fields": []any{
			shortField("实例", c.Instance),
			shortField("数据库", dbOrDefault(c.Database)),
			shortField("环境", strings.ToUpper(c.Env)),
			shortField("风险", riskLabel),
			shortField("发起人", c.Initiator),
		}},
		map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**命令**\n```sql\n" + safe(c.Command) + "\n```"}},
	}
	if strings.TrimSpace(c.Reason) != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**原因**\n" + safe(c.Reason)}})
	}
	if c.ConsoleURL != "" {
		elements = append(elements, map[string]any{"tag": "action", "actions": []any{
			map[string]any{"tag": "button", "type": "primary",
				"text": map[string]any{"tag": "plain_text", "content": "前往审批"},
				"url":  c.ConsoleURL},
		}})
	}
	elements = append(elements, map[string]any{"tag": "note", "elements": []any{
		map[string]any{"tag": "plain_text", "content": "AegisDB 数据库网关 · " + c.ApNo}}})
	return map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": "高危命令待审批 · " + c.ApNo}},
		"elements": elements,
	}
}
```

- [ ] **Step 4: 跑协议层测试确认通过**

Run: `cd backend && go test ./internal/imchannel/lark/ -v`
Expected: PASS（4 个）

- [ ] **Step 5: 写迁移 `0006_im_lark_notify_keys.sql`**

```sql
-- 飞书群通知的配置键从 notify.lark* 搬到 im.lark.*(多渠道 IM 框架,见 spec §9 / §13)。
--
-- 值原样搬,不解密也不重新加密:notify.larkSecret 以前是明文存的,新键 im.lark.notifySecret
-- 读取时走 decryptSetting,它对解不开的旧明文原样返回,所以照样能用;下一次在界面上保存
-- 就会被加密。
--
-- 开关要特别处理:旧代码读 notify.lark 时默认 **true**,而生产环境不播种,所以很多库里根本
-- 没有这一行 —— 行为是「配了 webhook 就发」。新键默认 false,若只搬存在的行,这些部署升级
-- 后群通知会悄悄停掉。于是:只要配了 webhook,就写一个开关,值取旧开关,没有旧开关就是 true。

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notify', COALESCE((SELECT v FROM tbl_setting WHERE k = 'notify.lark'), 'true')
WHERE EXISTS (SELECT 1 FROM tbl_setting WHERE k = 'notify.larkWebhook' AND v NOT IN ('', '""'))
ON CONFLICT (k) DO NOTHING;

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notifyWebhook', v FROM tbl_setting WHERE k = 'notify.larkWebhook'
ON CONFLICT (k) DO NOTHING;

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notifySecret', v FROM tbl_setting WHERE k = 'notify.larkSecret'
ON CONFLICT (k) DO NOTHING;

-- notify.larkChannel 是个从来没有代码读过的种子键,顺手清掉。
DELETE FROM tbl_setting WHERE k IN ('notify.lark', 'notify.larkWebhook', 'notify.larkSecret', 'notify.larkChannel');
```

- [ ] **Step 6: 写迁移测试 `bootstrap/im_migration_test.go`（先失败）**

```go
package bootstrap

import (
	"io/fs"
	"testing"

	"gorm.io/gorm"

	"velagateway/internal/testsupport"
	"velagateway/migrations"
)

// rerunMigration 在一个已迁移到最新的库上把某个迁移文件再执行一遍。
//
// testsupport.NewDB 已经跑过全部迁移,此刻库里没有任何旧数据可搬。测试的做法是:先按
// 旧版本的样子塞进旧数据,再单独重放这个文件 —— 这要求迁移本身可重入(本期三个迁移都是)。
func rerunMigration(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	raw, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	for _, stmt := range splitSQLStatements(string(raw)) {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v\n%s", name, err, stmt)
		}
	}
}

func settingRows(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var rows []struct{ K, V string }
	if err := db.Raw(`SELECT k, v FROM tbl_setting`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.K] = r.V
	}
	return out
}

func TestMigration0006_MovesLarkNotifyKeys(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Exec(`INSERT INTO tbl_setting (k, v) VALUES
		('notify.lark', 'false'), ('notify.larkWebhook', '"https://open.feishu.cn/hook/x"'),
		('notify.larkSecret', '"plain-secret"'), ('notify.larkChannel', '""')`)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")

	got := settingRows(t, db)
	eq(t, got["im.lark.notify"], "false", "the old switch value is kept")
	eq(t, got["im.lark.notifyWebhook"], `"https://open.feishu.cn/hook/x"`, "webhook moved")
	eq(t, got["im.lark.notifySecret"], `"plain-secret"`, "secret moved as-is")
	for _, k := range []string{"notify.lark", "notify.larkWebhook", "notify.larkSecret", "notify.larkChannel"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be gone", k)
		}
	}
}

func TestMigration0006_MissingSwitchWithWebhookMeansOn(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Exec(`INSERT INTO tbl_setting (k, v) VALUES ('notify.larkWebhook', '"https://open.feishu.cn/hook/x"')`)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")
	eq(t, settingRows(t, db)["im.lark.notify"], "true",
		"old default was on: a deployment with only a webhook must keep notifying")
}

func TestMigration0006_NoWebhookWritesNoSwitch(t *testing.T) {
	db := testsupport.NewDB(t)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")
	if _, ok := settingRows(t, db)["im.lark.notify"]; ok {
		t.Fatal("nothing configured → nothing switched on")
	}
}
```

Run: `cd backend && go test ./internal/bootstrap/ -run TestMigration0006 -v`
Expected（在 Step 5 的迁移文件存在后）：PASS。若先写测试再写迁移，则 FAIL 于 `read 0006_im_lark_notify_keys.sql`。

- [ ] **Step 7: 改写端到端测试 `bootstrap/lark_test.go`（整文件替换）**

```go
package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 飞书群通知:配置好 webhook 后,测试接口发一张样例卡片;真实的高危工单推一张带单号的卡片。
func TestLark_NotifyCardOnNewApproval(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"im.lark.notify": true, "im.lark.notifyWebhook": lark.URL, "notify.consoleURL": "https://gw.corp.io",
	})

	tr := app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil)
	eq(t, tr.Code, 0, "test-notify code")
	var tres struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(tr.Data, &tres)
	if !tres.Ok {
		t.Fatalf("test notify should succeed, got %q", tres.Message)
	}
	if body := waitCard(t, received); !strings.Contains(body, `"interactive"`) || !strings.Contains(body, "AP-TEST") {
		t.Errorf("test card is not an interactive approval card: %s", body)
	}

	prod := app.connIDByEnv(token, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders_2024_q3;", "reason": "q3 cleanup",
	})
	eq(t, r.Code, 42200, "high-risk intercepted")
	var er struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &er)
	body := waitCard(t, received)
	if !strings.Contains(body, er.ApprovalNo) || !strings.Contains(body, "https://gw.corp.io/#/approvals") {
		t.Errorf("approval card missing apNo %s or console link: %s", er.ApprovalNo, body)
	}
}

func TestLark_NotifySwitchOffSendsNothing(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- "hit"
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"im.lark.notify": false, "im.lark.notifyWebhook": lark.URL})
	app.submitProdHighRisk(token)
	app.flushIM()
	select {
	case <-received:
		t.Fatal("switch is off, nothing should be posted")
	case <-time.After(200 * time.Millisecond):
	}
}

func waitCard(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no card delivered to the Lark webhook")
		return ""
	}
}
```

- [ ] **Step 8: 改写 `bootstrap/settings_secret_test.go`（整文件替换）**

```go
package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 已配置的飞书签名密钥,不能被一次普通的设置保存(回传空密钥字段)抹掉;GET 不能回显它;
// 它在库里必须是密文。可观察的一面:配了密钥时卡片带 "sign",密钥被抹掉 "sign" 就消失。
func TestSettings_SavingDoesNotWipeLarkSecret(t *testing.T) {
	received := make(chan string, 4)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const secret = "lark-signing-secret-123"
	app.setSettings(token, map[string]any{
		"im.lark.notify": true, "im.lark.notifyWebhook": lark.URL,
		"im.lark.notifySecret": secret, "notify.consoleURL": "https://gw.corp.io",
	})
	eq(t, app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil).Code, 0, "test #1")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Fatalf("expected a signed card while secret is set, got: %s", body)
	}

	if raw := app.repo.SettingString("im.lark.notifySecret", ""); raw == "" || strings.Contains(raw, secret) {
		t.Errorf("secret must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", token, nil); strings.Contains(string(get.Data), secret) {
		t.Error("GET /settings leaked the raw lark secret")
	}

	app.setSettings(token, map[string]any{"im.lark.notifySecret": "", "notify.consoleURL": "https://gw.corp.io/changed"})
	eq(t, app.do(http.MethodPost, "/api/v1/settings/im/lark/test-notify", token, nil).Code, 0, "test #2")
	if body := waitCard(t, received); !strings.Contains(body, `"sign"`) {
		t.Errorf("lark secret was wiped by the settings save — card no longer signed: %s", body)
	}
}
```

- [ ] **Step 9: `settings_persist_test.go` 里那条非法 URL 换键**

把 `"approval.external.baseURL": "://not-a-url",` 改成 `"im.lark.notifyWebhook": "://not-a-url",`（注释不变）。

- [ ] **Step 10: 跑新测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestLark_|TestSettings_SavingDoesNotWipeLarkSecret|TestSettings' -v`
Expected: FAIL（`/settings/im/lark/test-notify` 404；飞书渠道未登记）。

- [ ] **Step 11: 登记飞书渠道、加测试通知服务**

`service.go` import 加 `"velagateway/internal/imchannel/lark"`；`New()` 里 `s.IM.SetSink(s)` 之后加：

```go
	// 第 1 期飞书只有群通知;交互审批在第 3 期把 Approve 打开。
	s.IM.Register(imchannel.Lark, imchannel.Caps{Notify: true}, lark.New)
```

`im_approval.go` 追加：

```go
// imSampleCard 是测试通知 / 测试卡片用的样例工单。
func (s *Services) imSampleCard() imchannel.ApprovalCard {
	return s.imCardFor(&model.Approval{
		ApNo: imTestApNo, Instance: "order-cluster", Env: "prod", TierCode: "prod", RiskLevel: model.RiskHigh,
		Initiator: "Vela", Command: "DROP TABLE orders_2024_q3;", Reason: "测试审批卡片推送",
	})
}

// TestIMNotify 同步发一条测试群通知,把结果原话告诉管理员。
func (s *Services) TestIMNotify(channel string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.IM.TestNotify(ctx, channel, s.imSampleCard())
	switch {
	case errors.Is(err, imchannel.ErrNotConfigured):
		return false, "未配置" + imchannel.Label(channel) + "群通知 Webhook"
	case err != nil:
		return false, "发送失败 · " + err.Error()
	}
	return true, "测试消息已发送到" + imchannel.Label(channel)
}
```

（`im_approval.go` import 补 `"time"`。）

- [ ] **Step 12: 删除旧的飞书通知代码**

1. `webhook.go`：删除 `// ---- Lark (飞书) cards` 分隔注释及其后的 `SendLarkApproval`、`TestLark`、`postLark`、`larkSign`、`larkSafe`、`dbOrDefault`、`larkApprovalCard` 全部函数；删掉因此不再使用的 import（`crypto/hmac`、`crypto/sha256`、`encoding/base64`、`strconv` 等——以 `go build` 报错为准）。
2. `sanitize_test.go`：删除 `TestLarkSafe_NeutralizesBackticks`（同等断言已在 `lark_test.go`）。
3. 删除 4 处 `s.Webhook.SendLarkApproval(ap)`：`gateway.go` `createApproval`、`pipeline.go` 约 692 行、`export_sensitive.go` 约 55 行、`exec_window_approval.go` 约 86 行。群通知现在由 `imApprovalCreated` → `IM.NotifyApproval` 发。

- [ ] **Step 13: 设置保存的校验与加密规则**

`handler/admin.go`：

1. 在 `encryptedSettingKeys` 下面加：

```go
// encryptedSetting 决定一个设置键是否加密落库。im.* 下凡是看起来像密钥的键一律加密 ——
// 每接一个渠道就要新增几个密钥(appSecret、botToken、signingSecret…),靠手工往上面那张表
// 里添,迟早有一个被漏掉,以明文躺进库里。
func encryptedSetting(k string) bool {
	return encryptedSettingKeys[k] || (strings.HasPrefix(k, "im.") && isSecretKey(k))
}
```

2. `SaveSettings` 里 `if encryptedSettingKeys[k] {` 改成 `if encryptedSetting(k) {`。
3. URL 校验的 `switch k` 里 `case "approval.external.baseURL", "notify.larkWebhook":` 改成 `case "approval.external.baseURL":`，并在 `switch` 前加：

```go
			if strings.HasPrefix(k, "im.") && strings.HasSuffix(k, "Webhook") {
				verr = service.ValidateOutboundURL(sv)
			}
```

（`verr` 的声明要挪到这段之前。）
4. 删除 `TestLark` handler。

- [ ] **Step 14: 测试通知 handler 与路由**

`handler/im.go` 追加：

```go
// TestIMNotify 往某渠道的通知群发一条样例。只用已保存的配置 —— 界面先保存再测。
func (h *Handler) TestIMNotify(c *gin.Context) {
	ok, msg := h.Svc.TestIMNotify(c.Param("channel"))
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}
```

`router.go`：删掉 `a.POST("/settings/lark/test", menu("settings"), admin, h.TestLark)`，在同一位置加：

```go
		a.POST("/settings/im/:channel/test-notify", menu("settings"), admin, h.TestIMNotify)
```

`openapi.yaml`：删除 `/settings/lark/test:` 条目，换成

```yaml
  /settings/im/{channel}/test-notify:
    post:
      tags: [settings]
      summary: '[settings · admin] 往该渠道的通知群发一条样例(用已保存的配置)'
      parameters: [{ name: channel, in: path, required: true, schema: { type: string } }]
      responses: { '200': { $ref: '#/components/responses/Ok' } }
```

`seed.go`：删除 `"notify.larkChannel": "",` 一行。

- [ ] **Step 15: 跑测试确认通过**

Run: `cd backend && go vet ./... && go test ./internal/imchannel/... ./internal/service/ ./internal/bootstrap/`
Expected: PASS

- [ ] **Step 16: 提交**

```bash
git add backend
git commit -m "feat(im): move Lark group notifications onto the IM channel layer

The custom-bot card, signing and backtick neutralisation move verbatim
into imchannel/lark; settings move from notify.lark* to im.lark.* in
migration 0006. A deployment that relied on the old default-on switch
keeps notifying. Any im.* key that looks like a secret is now encrypted
at rest without having to be listed by hand.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: 渠道状态与测试卡片接口

**Files:**
- Modify: `backend/internal/service/im_approval.go`（加 `TestIMCard`）
- Modify: `backend/internal/handler/im.go`（加 `IMStatus`、`TestIMCard`）
- Modify: `backend/internal/bootstrap/router.go`、`backend/docs/openapi.yaml`
- Test: `backend/internal/bootstrap/im_settings_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Hub.Status / TestCard`；Task 4 的 `imTestApNo`、`OnAction` 对测试单号的处理；Task 6 的 `imSampleCard`
- Produces:
  - `func (s *Services) TestIMCard(actor *model.User, channel string) (bool, string)`
  - `GET /settings/im/status`（settings）→ `[]imchannel.Status`
  - `POST /settings/im/:channel/test-card`（settings · admin）→ `{ok, message}`

- [ ] **Step 1: 写失败的测试 `bootstrap/im_settings_test.go`**

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIMSettings_StatusListsChannels(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.runningFake(admin)

	r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
	eq(t, r.Code, 0, "status")
	var rows []struct {
		Kind       string `json:"kind"`
		CanApprove bool   `json:"canApprove"`
		Approval   bool   `json:"approval"`
		Running    bool   `json:"running"`
	}
	_ = json.Unmarshal(r.Data, &rows)
	var sawLark, sawFake bool
	for _, row := range rows {
		switch row.Kind {
		case "lark":
			sawLark = true
			if row.CanApprove {
				t.Error("phase 1 lark is notify-only")
			}
		case "fake":
			sawFake = row.Approval && row.Running
		}
	}
	if !sawLark || !sawFake {
		t.Fatalf("status = %s", r.Data)
	}
}

func TestIMSettings_TestCardGoesToTheAdminsOwnAccountAndTouchesNoTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)

	var out struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	r := app.do(http.MethodPost, "/api/v1/settings/im/fake/test-card", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if out.Ok || !strings.Contains(out.Message, "绑定") {
		t.Fatalf("unbound admin: %+v — should be told to bind first", out)
	}

	app.bindIM("linwei@vela.io", "fake", "p-lw")
	r = app.do(http.MethodPost, "/api/v1/settings/im/fake/test-card", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if !out.Ok {
		t.Fatalf("test card: %+v", out)
	}
	app.flushIM()
	sent := f.Sent()
	if len(sent) != 1 || sent[0].PlatformUserID != "p-lw" || sent[0].Card.ApNo != "AP-TEST" {
		t.Fatalf("sent = %+v", sent)
	}
	var cards int64
	app.repo.DB().Table("tbl_im_card").Count(&cards)
	eq(t, cards, int64(0), "a test card is not recorded — there is nothing to close")

	if res := f.Click("p-lw", "AP-TEST", true); !strings.Contains(res.Toast, "测试卡片") {
		t.Fatalf("toast = %q", res.Toast)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run TestIMSettings_ -v`
Expected: FAIL（404）。

- [ ] **Step 3: 服务与 handler**

`im_approval.go` 追加：

```go
// TestIMCard 给管理员**自己**私信一张测试卡片。点它只回一句话(见 OnAction 对 AP-TEST
// 的处理),顺带验证了「这个账号认得出是我」。
func (s *Services) TestIMCard(actor *model.User, channel string) (bool, string) {
	ids, err := s.Repo.IMPlatformIDs(channel, []int64{actor.ID})
	pid, ok := ids[actor.ID]
	if err != nil || !ok {
		return false, "你还没有绑定" + imchannel.Label(channel) + "账号,请先在账户菜单「IM 账号绑定」中绑定"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.IM.TestCard(ctx, channel, pid, s.imSampleCard()); err != nil {
		if errors.Is(err, imchannel.ErrNotConfigured) {
			return false, imchannel.Label(channel) + "交互审批未配置"
		}
		return false, "发送失败 · " + err.Error()
	}
	return true, "测试卡片已私信给你"
}
```

`handler/im.go` 追加：

```go
func (h *Handler) IMStatus(c *gin.Context) { resp.OK(c, h.Svc.IM.Status()) }

func (h *Handler) TestIMCard(c *gin.Context) {
	ok, msg := h.Svc.TestIMCard(middleware.CurrentUser(c), c.Param("channel"))
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}
```

`router.go` 在 `test-notify` 那行旁边加：

```go
		a.GET("/settings/im/status", menu("settings"), h.IMStatus)
		a.POST("/settings/im/:channel/test-card", menu("settings"), admin, h.TestIMCard)
```

`openapi.yaml` 在 `test-notify` 条目旁加：

```yaml
  /settings/im/status:
    get:
      tags: [settings]
      summary: '[settings] 各 IM 渠道的能力、开关、运行状态与最近错误'
      responses: { '200': { $ref: '#/components/responses/Ok' } }
  /settings/im/{channel}/test-card:
    post:
      tags: [settings]
      summary: '[settings · admin] 给当前管理员自己私信一张测试卡片(需已绑定;点击不触及任何工单)'
      parameters: [{ name: channel, in: path, required: true, schema: { type: string } }]
      responses: { '200': { $ref: '#/components/responses/Ok' } }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIMSettings_|TestOpenAPI' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend
git commit -m "feat(im): expose channel status and a self-addressed test card

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 8: 删除审批魔方

**Files:**
- Delete: `backend/internal/service/external_approval.go`、`backend/internal/service/external_approval_redact_test.go`
- Delete: `backend/internal/bootstrap/external_approval_test.go`、`external_never_dispatched_test.go`、`external_decide_race_test.go`、`callback_status_test.go`
- Create: `backend/internal/bootstrap/approval_helpers_test.go`（接住被删文件里仍被别处使用的辅助函数）
- Create: `backend/migrations/0007_drop_approval_cube.sql`
- Modify: `backend/internal/service/webhook.go`、`gateway.go`、`pipeline.go`、`export_sensitive.go`、`exec_window_approval.go`
- Modify: `backend/internal/dto/dto.go`、`backend/internal/model/model.go`、`backend/internal/repository/repository.go`、`backend/internal/bootstrap/seed.go`
- Modify: `backend/internal/handler/admin.go`、`backend/internal/bootstrap/router.go`、`backend/cmd/server/main.go`（注释）、`backend/docs/openapi.yaml`
- Modify: `backend/internal/bootstrap/dual_snapshot_test.go`、`config_audit_test.go`、`webhook_secret_test.go`（注释）
- Test: `backend/internal/bootstrap/im_migration_test.go`（追加 0007 用例）、`backend/internal/bootstrap/approval_cube_gone_test.go`

**Interfaces:**
- Consumes: Task 6 的 `rerunMigration`、`settingRows`、`encryptedSetting`；Task 3 的 `fakeIM`、`bindIM`、`enableIM`、`flushIM`
- Produces: 测试辅助 `(*testApp).approvalRow(token, apNo) apStatusRow`、`setSettings(token, kv)`、`approvalIDByNo(apNo) int64`（签名不变，只是换了文件；`apStatusRow` 去掉 `ExternalTaskID` 字段）

- [ ] **Step 1: 写失败的测试**

`bootstrap/approval_cube_gone_test.go`：

```go
package bootstrap

import (
	"bytes"
	"net/http"
	"testing"
)

// 回调端点能驱动一张生产工单走向终态。整条对接删掉之后,这扇门必须真的不在了 ——
// 不是「鉴权失败」,是「没有这个路由」。
func TestApprovalCube_CallbackRouteIsGone(t *testing.T) {
	app := newTestApp(t)
	res, err := http.Post(app.srv.URL+"/api/v1/approvals/lark/callback", "application/json",
		bytes.NewReader([]byte(`{"external_task_id":"AP-1","approved":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	eq(t, res.StatusCode, http.StatusNotFound, "the callback route no longer exists")
}

// 旧版前端(或缓存着旧页面的浏览器)保存设置时还会带着 approval.external.* —— 静默丢弃,
// 不能让它们重新长回库里。
func TestSettings_LegacyApprovalCubeKeysAreDropped(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{
		"approval.external.enabled": true, "approval.external.token": "tok",
	})
	for k := range settingRows(t, app.repo.DB()) {
		if len(k) >= 18 && k[:18] == "approval.external." {
			t.Errorf("legacy key %s was stored", k)
		}
	}
}
```

`bootstrap/im_migration_test.go` 追加：

```go
func TestMigration0007_RemovesApprovalCubeLeftovers(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Exec(`INSERT INTO tbl_setting (k, v) VALUES
		('approval.external.enabled', 'true'), ('approval.external.token', '"enc:xxx"'),
		('approval.external.callbackSecret', '"enc:yyy"'), ('approval.onTimeout', '"auto-reject"')`)
	rerunMigration(t, db, "0007_drop_approval_cube.sql")

	got := settingRows(t, db)
	for k := range got {
		if len(k) >= 18 && k[:18] == "approval.external." {
			t.Errorf("%s should be gone", k)
		}
	}
	eq(t, got["approval.onTimeout"], `"auto-reject"`, "unrelated approval.* keys are untouched")

	var n int64
	db.Raw(`SELECT count(*) FROM information_schema.columns
	        WHERE table_schema = current_schema() AND table_name = 'tbl_approval' AND column_name = 'external_task_id'`).Scan(&n)
	eq(t, n, int64(0), "external_task_id column dropped")
}
```

`dual_snapshot_test.go`：删除 `TestDualSnapshot_ExternalPayloadCarriesEnvAndTier` 整个函数（含它上方的注释），在原位置换成：

```go
// 出站的卡片同样要带上两个快照:env 说「在哪儿跑」,tier 说「按什么级别判的」——
// 后者才解释得了这张单为什么需要审批。
func TestDualSnapshot_IMCardCarriesEnvAndTier(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	app.interceptOn(admin, conn, "DROP TABLE orders;")
	app.flushIM()
	sent := f.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d cards, want 1", len(sent))
	}
	eq(t, sent[0].Card.Env, "prod-hk", "card env = environment code")
	eq(t, sent[0].Card.Tier, "prod", "card tier = control tier")
}
```

（若 `hkSetup` 建出的链上没有 zhangwei，改绑该链上的任一 owner——先读 `hkSetup` 确认。删掉该文件里因此不再使用的 import，如 `io`、`net/http/httptest`、`sync`。）

`config_audit_test.go` `TestConfigAudit_SettingsAuditNeverContainsTheSecretValue` 里两处 `"approval.external.callbackSecret"` 改成 `"im.lark.notifySecret"`。

- [ ] **Step 2: 接住仍被使用的测试辅助**

新建 `bootstrap/approval_helpers_test.go`：

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
)

// apStatusRow mirrors the GET /approvals view fields the suite asserts on.
type apStatusRow struct {
	ApNo   string `json:"apNo"`
	Status string `json:"status"`
}

func (a *testApp) approvalRow(token, apNo string) apStatusRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	eq(a.t, r.Code, 0, "list approvals code")
	// The listing is paged: unwrap the envelope before decoding the rows.
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("approvals envelope decode: %v", err)
	}
	var aps []apStatusRow
	_ = json.Unmarshal(page.Items, &aps)
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap
		}
	}
	a.t.Fatalf("approval %q not found", apNo)
	return apStatusRow{}
}

func (a *testApp) setSettings(token string, kv map[string]any) {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", token, kv).Code, 0, "save settings")
}

// approvalIDByNo resolves an ApNo to its row id for direct-store assertions.
func (a *testApp) approvalIDByNo(apNo string) int64 {
	a.t.Helper()
	ap, err := a.repo.GetApprovalByApNo(apNo)
	if err != nil || ap == nil {
		a.t.Fatalf("approval %q not found: %v", apNo, err)
	}
	return ap.ID
}
```

然后删除 `external_approval_test.go`、`external_never_dispatched_test.go`、`external_decide_race_test.go`、`callback_status_test.go`，以及 `backend/internal/service/external_approval.go`、`external_approval_redact_test.go`。

被删测试守护的三类性质都已在新渠道上有同等强度的断言：脱敏 → `TestIM_CardCommandIsRedacted`；竞态 / 幂等 → `TestIM_ChainMemberApprovesAndTheAuditNamesTheChannel`、`TestIM_ClickAfterConsoleDecisionEchoesState`、`TestHub_SettledWhileSendingStillClosesLateCards`；禁自审 → `TestIM_SelfApprovalFollowsTheSameSettingAsTheConsole`。

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestApprovalCube_|TestSettings_LegacyApprovalCubeKeysAreDropped|TestMigration0007|TestDualSnapshot' 2>&1 | head -30`
Expected: 编译失败（`dispatchExternalApproval` 等在已删文件里定义，调用点还在）。

- [ ] **Step 4: 写迁移 `0007_drop_approval_cube.sql`**

```sql
-- 删除审批魔方对接(ADR-0003,被 ADR-0020 取代)留下的一切。
--
-- 设置里有两条加密的凭据(token、callbackSecret)。不在库里留死凭据:它们不再有任何用途,
-- 只剩下被人从备份里拿走的那一种可能。
DELETE FROM tbl_setting WHERE k LIKE 'approval.external.%';

-- external_task_id 的两个用途(回调关联、超时回写取消)都随魔方消失,卡片追踪改由
-- tbl_im_card 承担。删列不能与旧二进制共存:升级顺序见 README(备份 → 停旧进程 → migrate)。
DROP INDEX IF EXISTS idx_approval_ext;
ALTER TABLE tbl_approval DROP COLUMN IF EXISTS external_task_id;
```

- [ ] **Step 5: 删除生产代码**

1. `service/webhook.go`：删除 `SendExternalApproval`、`PatchExternalStatus` 两个函数及不再使用的 import（如 `net/url`，以编译报错为准）。
2. `service/gateway.go`：删除 `createApproval` 里的 `s.dispatchExternalApproval(u, ap) // (审批魔方) …` 一行；删除 `SweepApprovalTimeouts` 里的 `s.cancelExternalApproval(a) // (审批魔方) …` 一行；`finalizeApproval` 上方注释里 `and the external审批魔方 callback (DecideApprovalExternal)` 改为 `and the IM channels (DecideApprovalAs via OnAction)`。
3. `service/pipeline.go`：删除 `s.dispatchExternalApproval(creator, ap)` 与 `s.cancelExternalApproval(*ap) // …` 两行。
4. `service/export_sensitive.go`、`service/exec_window_approval.go`：各删除 `s.dispatchExternalApproval(u, ap)` 一行。
5. `dto/dto.go`：删除 `LarkApprovalCallbackReq` 类型及其注释。
6. `model/model.go`：删除 `Approval.ExternalTaskID` 字段及其上方三行注释。
7. `repository/repository.go`：删除 `SetApprovalExternalTask` 及其注释。
8. `bootstrap/seed.go`：删除 `// External 飞书审批(审批魔方)对接 …` 两行注释和 7 个 `approval.external.*` 键。

- [ ] **Step 6: handler、路由、文档**

1. `handler/admin.go`：
   - 删除 `LarkApprovalCallback` 和 `bearerToken`（`bearerToken` 只被它用）及其上方注释。
   - 删除 `encryptedSettingKeys` 这张表，把 `encryptedSetting` 改成：

```go
// encryptedSetting 决定一个设置键是否加密落库:im.* 下凡是看起来像密钥的键一律加密。
// 这些键的值能冒充网关去发卡片、收回调 —— 一份库备份不该交出能用的凭据。每接一个渠道
// 都会新增几个密钥,靠一张手写的表迟早会漏掉一个。
func encryptedSetting(k string) bool {
	return strings.HasPrefix(k, "im.") && isSecretKey(k)
}
```

   - `SaveSettings` 循环开头 `if k == "strictMode" { continue }` 之后加：

```go
		// 审批魔方的对接已经删除(ADR-0020)。旧前端还可能带着这些键来,静默丢弃 ——
		// 存进去就是一堆看着像配置、其实什么也不控制的东西,其中还有凭据。
		if strings.HasPrefix(k, "approval.external.") {
			continue
		}
```

   - URL 校验 `switch` 里删掉 `case "approval.external.baseURL":` 与 `case "approval.external.callbackBaseURL":` 两个分支；`case "security.ipAllowlist", "approval.external.callbackAllowIPs":` 改成 `case "security.ipAllowlist":`。若 `service.ValidateCallbackBaseURL` 因此再无调用方，**保留**它——第 2 期 webhook 模式的 `im.callbackBaseURL` 会用到。
   - `SaveWebhook` 里注释 `同类的 approval.external.token 一直是这么存的` 改成 `同类的 IM 渠道密钥一直是这么存的`。
2. `bootstrap/router.go`：删除 `v1.POST("/approvals/lark/callback", …)` 及上方 4 行注释；`secretQueryKeys` 上方注释里 `(审批魔方 registers a callback URL and nothing else)` 改为 `(some webhook senders register a URL and nothing else)`。
3. `cmd/server/main.go` 约 200 行注释 `the 飞书 card, the 审批魔方 callback and the` 改为 `the IM channels and the`（按原句通顺调整）。
4. `docs/openapi.yaml`：删除 `/approvals/lark/callback:` 整个条目，以及文件开头说明表里 `| 回调 | /api/v1/approvals/lark/callback | … |` 那一行。
5. `bootstrap/webhook_secret_test.go` 头注释 `同类的 approval.external.token 是加密落库的` 改为 `同类的 IM 渠道密钥是加密落库的`。

- [ ] **Step 7: 确认没有残留**

Run: `cd backend && grep -rnE 'approval\.external|审批魔方|ExternalTaskID|externalTaskId|DecideApprovalExternal|SendLarkApproval|lark/callback' --include='*.go' --include='*.yaml' . | grep -v 'migrations/'`
Expected: 只剩 `approval_cube_gone_test.go`、`im_migration_test.go` 里的断言文本，以及 `admin.go` 里丢弃旧键的那一行。

- [ ] **Step 8: 全量测试**

Run: `cd backend && go vet ./... && go test ./...`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add -A backend
git commit -m "refactor(approval): remove the approval cube integration

The vendor callback, its outbound dispatch and cancel write-back, the
external_task_id column and every approval.external.* setting are gone;
migration 0007 drops the stored credentials rather than leaving them in
the database. The properties its tests guarded — redaction, race safety,
idempotency, the self-approval net — are asserted on the IM path instead.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: 前端 · 设置页「IM 渠道」分区

**Files:**
- Create: `frontend/src/api/modules/im.ts`
- Create: `frontend/src/hooks/useIm.ts`
- Create: `frontend/src/components/settings/ImChannelsPanel.tsx`
- Modify: `frontend/src/pages/settings/index.tsx`
- Modify: `frontend/src/api/modules/settings.ts`（删 `testLark`）
- Modify: `frontend/src/hooks/useSettings.ts`（删 `useTestLark`）
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`
- Modify: `frontend/src/styles/theme.css`（追加 `.im-*` 样式）

**Interfaces:**
- Consumes: 后端 `GET /settings/im/status`、`POST /settings/im/:channel/test-notify`、`POST /settings/im/:channel/test-card`、`PUT /settings`（Task 6/7）
- Produces（Task 10 也用）：
  - `im.ts`：类型 `ImStatus`、`ImIdentity`；`imApi.{status, testNotify, testCard, myIdentities, issueCode, unbindMine, userIdentities, bindUser, unbindUser}`；`imStatusQueryOptions()`、`myImIdentitiesQueryOptions(poll?: boolean)`、`userImIdentitiesQueryOptions(id)`
  - `useIm.ts`：`useImStatus`、`useTestImNotify`、`useTestImCard`（Task 10 再加绑定相关 hooks）
  - `ImChannelsPanel.tsx`：默认导出组件；具名导出 `IM_CHANNELS`、`imInitialValues(g)`、`imSettingsPayload(v)`、类型 `ImValues`

- [ ] **Step 1: 写 `api/modules/im.ts`**

```ts
import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'

/** 一个 IM 渠道的运行状况。能力(canX)是登记时声明的,开关(notify/approval)是设置里的。 */
export interface ImStatus {
  kind: string
  label: string
  canNotify: boolean
  canApprove: boolean
  configured: boolean
  notify: boolean
  approval: boolean
  running: boolean
  botName?: string
  lastError?: string
}

/** 某人在一个交互审批渠道上的绑定。 */
export interface ImIdentity {
  channel: string
  label: string
  enabled: boolean
  bound: boolean
  platformUserId?: string
  boundBy?: 'self' | 'admin'
  botName?: string
  boundAt?: string
}

type TestResult = { ok: boolean; message: string }

export const imApi = {
  status: () => http.get<any, Envelope<ImStatus[]>>('/settings/im/status').then(ok),
  testNotify: (kind: string) =>
    http.post<any, Envelope<TestResult>>(`/settings/im/${kind}/test-notify`).then(ok),
  testCard: (kind: string) =>
    http.post<any, Envelope<TestResult>>(`/settings/im/${kind}/test-card`).then(ok),

  myIdentities: () => http.get<any, Envelope<ImIdentity[]>>('/me/im-identities').then(ok),
  // 码只在这一次响应里出现。服务端只存哈希,没有第二个接口能再取。
  issueCode: (channel: string) =>
    http.post<any, Envelope<{ code: string; expiresAt: string; botName?: string }>>(
      `/me/im-identities/${channel}/code`).then(ok),
  unbindMine: (channel: string) =>
    http.delete<any, Envelope<{ ok: boolean }>>(`/me/im-identities/${channel}`).then(ok),

  userIdentities: (id: number) =>
    http.get<any, Envelope<ImIdentity[]>>(`/users/${id}/im-identities`).then(ok),
  bindUser: (id: number, channel: string, platformUserId: string) =>
    http.put<any, Envelope<{ ok: boolean }>>(`/users/${id}/im-identities/${channel}`, { platformUserId }).then(ok),
  unbindUser: (id: number, channel: string) =>
    http.delete<any, Envelope<{ ok: boolean }>>(`/users/${id}/im-identities/${channel}`).then(ok),
}

export const imStatusQueryOptions = () =>
  queryOptions({
    queryKey: ['im-status'] as const,
    queryFn: imApi.status,
    // 长连接会自己断、自己重连;状态点要跟得上,但不必实时。
    refetchInterval: 10_000,
  })

export const myImIdentitiesQueryOptions = (poll = false) =>
  queryOptions({
    queryKey: ['im-identities', 'me'] as const,
    queryFn: imApi.myIdentities,
    // 绑定码弹出后,用户去 IM 里发码;这边轮询,绑上了自己变。
    refetchInterval: poll ? 3_000 : false,
  })

export const userImIdentitiesQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['im-identities', id] as const,
    queryFn: () => imApi.userIdentities(id),
  })
```

- [ ] **Step 2: 写 `hooks/useIm.ts`（本任务部分）**

```ts
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { imApi, imStatusQueryOptions } from '@/api/modules/im'
import { settingsApi } from '@/api/modules/settings'
import { useUIStore } from '@/stores/ui'

export function useImStatus() {
  return useQuery(imStatusQueryOptions())
}

/**
 * 测试前先把当前表单里的 IM 配置存下来 —— 服务端只用已保存的配置去发,不存的话试的是
 * 上一次保存的那一份,而管理员以为试的是眼前这一份。
 */
function useImTest(fn: (kind: string) => Promise<{ ok: boolean; message: string }>) {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (v: { kind: string; settings: Record<string, unknown> }) => {
      await settingsApi.saveSettings(v.settings)
      return fn(v.kind)
    },
    onSuccess: (r) => {
      notify(r.message || t('imTestOk'), r.ok ? 'ok' : 'error')
      qc.invalidateQueries({ queryKey: ['im-status'] })
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => notify(e.message || t('imTestFail'), 'error'),
  })
}

export function useTestImNotify() {
  return useImTest(imApi.testNotify)
}

export function useTestImCard() {
  return useImTest(imApi.testCard)
}
```

- [ ] **Step 3: 写 `components/settings/ImChannelsPanel.tsx`**

```tsx
import { useTranslation } from 'react-i18next'
import { CardRow } from '@/components/common/Card'
import { Switch } from '@/components/common/Switch'
import { Button } from '@/components/common/Button'
import { useImStatus, useTestImCard, useTestImNotify } from '@/hooks/useIm'
import { parseSetting } from '@/lib/settingOptions'
import type { ImStatus } from '@/api/modules/im'

type FieldDef = {
  key: string
  label: string
  /** 字面的占位符(URL 这类不翻译的) */
  placeholder?: string
  /** 需要翻译的占位符 */
  placeholderKey?: string
  secret?: boolean
}
type ChannelDef = { notify?: FieldDef[]; approval?: FieldDef[] }

/**
 * 每个渠道自己的配置项。表单初值、保存载荷、渲染都从这张表派生 —— 接一个新渠道只在
 * 这里加一项,不必再去动设置页。第 1 期只有飞书群通知。
 */
export const IM_CHANNELS: Record<string, ChannelDef> = {
  lark: {
    notify: [
      { key: 'im.lark.notifyWebhook', label: 'imLarkWebhook', placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx' },
      { key: 'im.lark.notifySecret', label: 'imLarkSecret', placeholderKey: 'imLarkSecretPh', secret: true },
    ],
  },
}

export type ImValues = Record<string, string | boolean>

export function imInitialValues(g: Record<string, string>): ImValues {
  const v: ImValues = {}
  for (const [kind, def] of Object.entries(IM_CHANNELS)) {
    if (def.notify) v[`im.${kind}.notify`] = parseSetting(g[`im.${kind}.notify`], false)
    if (def.approval) v[`im.${kind}.approval`] = parseSetting(g[`im.${kind}.approval`], false)
    for (const f of [...(def.notify ?? []), ...(def.approval ?? [])]) {
      // 密钥从不回传。输入框留空 = 保持不变,所以初值只能是空串。
      v[f.key] = f.secret ? '' : parseSetting<string>(g[f.key], '')
    }
  }
  return v
}

export function imSettingsPayload(v: ImValues): Record<string, string | boolean> {
  const out: Record<string, string | boolean> = {}
  for (const [k, val] of Object.entries(v)) out[k] = typeof val === 'string' ? val.trim() : val
  return out
}

export default function ImChannelsPanel({
  values, onChange, secretsSet,
}: {
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
}) {
  const { data: status } = useImStatus()
  const testNotify = useTestImNotify()
  const testCard = useTestImCard()
  const settings = () => imSettingsPayload(values)

  // 以服务端登记过的渠道为准:前端认得、服务端却没登记的渠道不显示 —— 显示出来也配不了。
  const rows = (status ?? []).filter((s) => IM_CHANNELS[s.kind])
  return (
    <div className="im-list">
      {rows.map((s) => {
        const def = IM_CHANNELS[s.kind]
        return (
          <div key={s.kind} className="im-ch">
            <div className="im-ch-head">
              <span className="im-ch-name">{s.label}</span>
              <StateTag s={s} />
            </div>
            {s.canNotify && def.notify && (
              <Capability
                title="imNotify" hint="imNotifyD" switchKey={`im.${s.kind}.notify`} fields={def.notify}
                values={values} onChange={onChange} secretsSet={secretsSet}
                testLabel="imTestNotify" busy={testNotify.isPending}
                onTest={() => testNotify.mutate({ kind: s.kind, settings: settings() })}
              />
            )}
            {s.canApprove && def.approval && (
              <Capability
                title="imApproval" hint="imApprovalD" switchKey={`im.${s.kind}.approval`} fields={def.approval}
                values={values} onChange={onChange} secretsSet={secretsSet}
                testLabel="imTestCard" busy={testCard.isPending}
                onTest={() => testCard.mutate({ kind: s.kind, settings: settings() })}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

function Capability({
  title, hint, switchKey, fields, values, onChange, secretsSet, testLabel, busy, onTest,
}: {
  title: string
  hint: string
  switchKey: string
  fields: FieldDef[]
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
  testLabel: string
  busy: boolean
  onTest: () => void
}) {
  const { t } = useTranslation()
  const on = values[switchKey] === true
  return (
    <>
      <CardRow title={t(title)} hint={t(hint)}>
        <Switch checked={on} onChange={(v) => onChange(switchKey, v)} />
      </CardRow>
      {on && (
        <div className="set-sub">
          {fields.map((f) => (
            <label key={f.key} className="set-field">
              <span>{t(f.label)}</span>
              <input
                className="set-in"
                type={f.secret ? 'password' : 'text'}
                value={String(values[f.key] ?? '')}
                placeholder={
                  f.secret && secretsSet?.[f.key] ? t('secretConfigured')
                    : f.placeholderKey ? t(f.placeholderKey) : f.placeholder
                }
                onChange={(e) => onChange(f.key, e.target.value)}
              />
            </label>
          ))}
          <div className="set-sub-foot">
            <Button disabled={busy} onClick={onTest}>{busy ? t('imSending') : t(testLabel)}</Button>
          </div>
        </div>
      )}
    </>
  )
}

function StateTag({ s }: { s: ImStatus }) {
  const { t } = useTranslation()
  if (s.lastError) return <span className="im-state err" title={s.lastError}>{t('imStateError', { msg: s.lastError })}</span>
  if (s.running) return <span className="im-state on">{t('imStateRunning')}</span>
  if (!s.configured) return <span className="im-state">{t('imStateUnconfigured')}</span>
  return <span className="im-state">{s.notify || s.approval ? t('imStateReady') : t('imStateIdle')}</span>
}
```

- [ ] **Step 4: 改设置页 `pages/settings/index.tsx`**

1. import：从 `lucide-react` 加 `MessagesSquare`；`@/hooks/useSettings` 的导入去掉 `useTestLark`；加
   `import ImChannelsPanel, { imInitialValues, type ImValues } from '@/components/settings/ImChannelsPanel'`。
2. `SECTIONS` 在 `approval` 之后加 `{ id: 'im', icon: MessagesSquare, label: 'setIm' },`，并把上方注释「九个分区」改成「十个分区」。
3. `SettingsForm`：删 `const testLark = useTestLark()`；在 `const [f, setF] = useState(...)` 之后加

```tsx
  // IM 渠道的配置是一张按渠道派生的键值表(见 ImChannelsPanel 的 IM_CHANNELS),
  // 不塞进上面那个平铺的表单对象 —— 否则每接一个渠道都要来这里加一排字段。
  const [im, setIm] = useState<ImValues>(() => imInitialValues(g))
```

4. 表单初值：删掉 `extEnabled`、`extBaseURL`、`extAiGroup`、`extCallbackBaseURL`、`extAllowIPs`、`extToken`、`extCallbackSecret`（含那行注释）；「通知」段删掉 `lark`、`larkWebhook`、`larkSecret`，保留 `consoleURL`。
5. `onSave` 的 `settings`：删掉 7 个 `'approval.external.*'` 和 `'notify.lark'`、`'notify.larkWebhook'`、`'notify.larkSecret'`；在 `'notify.consoleURL'` 那行后加 `...imSettingsPayload(im),`（import 补 `imSettingsPayload`）。
6. 删掉 `const callbackURL = …` 三行。
7. 审批分区里删掉「对接外部飞书审批」整块：从 `<CardRow title={t('setExtAppr')} …>` 到其后 `{f.extEnabled && ( … )}` 的结束。
8. 在审批分区 `</Card></section>` 之后插入新分区：

```tsx
        {/* ---------------- IM 渠道 ---------------- */}
        <section id="set-im" className="set-sec"><Card>
          <CardHead icon={<MessagesSquare size={17} />} title={t('setIm')} sub={t('setImSub')} />
          <CardRow title={t('imConsole')} hint={t('imConsoleD')}>
            <input className="set-in" value={f.consoleURL} placeholder="https://gw.corp.io"
                   onChange={(e) => set({ consoleURL: e.target.value })} />
          </CardRow>
          <ImChannelsPanel
            values={im}
            onChange={(k, v) => setIm((prev) => ({ ...prev, [k]: v }))}
            secretsSet={data.secretsSet}
          />
        </Card></section>
```

9. 通知分区里删掉飞书那一整块：从 `<CardRow title={t('setLark')} …>` 到 `{f.lark && ( … )}` 的结束（`setEmail`、`setPush`、Webhook 转发保持不动）。

- [ ] **Step 5: 删除旧的飞书测试 API 与 hook**

`api/modules/settings.ts` 删掉 `testLark: …` 一行；`hooks/useSettings.ts` 删掉 `useTestLark` 整个函数（含注释）。

- [ ] **Step 6: 文案**

`locales/zh.ts`：删除 `setExtAppr`、`setExtApprD`、`extBaseURL`、`extToken`、`extTokenPh`、`extAiGroup`、`extCallbackBase`、`extCallbackFull`、`extCallbackSecret`、`extCallbackSecretPh`、`extAllowIPs`、`extAllowIPsPh`、`setLark`、`setLarkD`、`larkWebhook`、`larkSecret`、`larkSecretPh`、`larkConsole`、`larkConsoleD`、`larkSending`、`larkSendTest`、`larkTestOk`、`larkTestFail`；在原 `setLark` 的位置加：

```ts
  setIm: 'IM 渠道',
  setImSub: '审批卡片私信给审批链上已绑定的成员,点按钮即可审批;群通知把新工单推到指定群',
  imConsole: '控制台外网地址',
  imConsoleD: '卡片里「前往审批」的链接用它拼出来',
  imNotify: '群通知',
  imNotifyD: '新工单创建时往群里发一条消息(不带按钮)',
  imApproval: '交互审批',
  imApprovalD: '私信审批链上已绑定的成员,可直接在 IM 里通过或驳回',
  imLarkWebhook: '自定义机器人 Webhook',
  imLarkSecret: '签名密钥',
  imLarkSecretPh: '启用了签名校验时填写',
  imTestNotify: '发一条测试通知',
  imTestCard: '给我发一张测试卡片',
  imSending: '发送中',
  imTestOk: '测试消息已发出',
  imTestFail: '测试消息发送失败',
  imStateRunning: '运行中',
  imStateReady: '已配置',
  imStateIdle: '未启用',
  imStateUnconfigured: '未配置',
  imStateError: '异常:{{msg}}',
```

`locales/en.ts`：删除同一批键，在对应位置加：

```ts
  setIm: 'IM channels',
  setImSub: 'Approval cards go by DM to bound approvers on the chain, who decide with a click; group notifications announce new tickets',
  imConsole: 'Console public URL',
  imConsoleD: 'Used to build the "Review" link inside cards',
  imNotify: 'Group notification',
  imNotifyD: 'Post a message (no buttons) to a group when a ticket is raised',
  imApproval: 'Interactive approval',
  imApprovalD: 'DM bound approvers on the chain so they can approve or reject in the IM',
  imLarkWebhook: 'Custom bot webhook',
  imLarkSecret: 'Signing secret',
  imLarkSecretPh: 'Only if signature verification is on',
  imTestNotify: 'Send a test notification',
  imTestCard: 'Send me a test card',
  imSending: 'Sending',
  imTestOk: 'Test message sent',
  imTestFail: 'Test message failed',
  imStateRunning: 'Running',
  imStateReady: 'Configured',
  imStateIdle: 'Off',
  imStateUnconfigured: 'Not configured',
  imStateError: 'Error: {{msg}}',
```

- [ ] **Step 7: 样式**

`styles/theme.css` 末尾追加：

```css
/* ---- IM 渠道(设置页) ---- */
.im-list { display: flex; flex-direction: column; gap: 12px; margin-top: 8px; }
.im-ch { border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.im-ch-head { display: flex; align-items: center; gap: 10px; padding: 12px 16px 4px; }
.im-ch-name { font: 600 13px var(--font-body); color: var(--text-strong); }
.im-state { font: 600 10px var(--font-mono); color: var(--text-faint); }
.im-state.on { color: var(--success-text); }
.im-state.err { color: var(--danger-text); max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
```

- [ ] **Step 8: 类型检查与 lint**

Run: `cd frontend && npm run build && npm run lint`
Expected: 无错误。`tsc` 若报 `en.ts` 缺键或多键，按报错把两边对齐。

`t()` 接受任意字符串，删掉的键若还有人引用，编译器不会报。再查一遍：

Run: `cd frontend && grep -rnE "'(setExtAppr|setExtApprD|ext[A-Z][A-Za-z]*|setLark|setLarkD|lark[A-Z][A-Za-z]*)'" src`
Expected: 无输出。

- [ ] **Step 9: 在真实应用里看一眼**

用 `run-app` skill 起前后端，以 `linwei@vela.io / vela123` 登录，打开 `/settings#set-im`：
- 左侧导航出现「IM 渠道」；分区里有「控制台外网地址」和一张「飞书」卡片，状态为「未配置」。
- 打开「群通知」，填一个 webhook，点「发一条测试通知」→ 出现 toast（webhook 不通会显示失败原因，这是预期）。
- 「审批」分区不再有「对接外部飞书审批」；「通知」分区不再有飞书那块。
- 保存后刷新，飞书开关与 webhook 保持，密钥框显示「已配置」占位。

- [ ] **Step 10: 提交**

```bash
git add frontend
git commit -m "feat(ui): add the IM channels settings section

Channel fields are derived from one table, so a new channel is one entry
there rather than another row of form state. The approval-cube block and
the old Lark notification block are gone.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: 前端 · 账户菜单绑定弹窗与权限页代绑

**Files:**
- Modify: `frontend/src/hooks/useIm.ts`（加绑定相关 hooks）
- Create: `frontend/src/components/modals/ImBindModal.tsx`
- Create: `frontend/src/components/permission/UserImPane.tsx`
- Modify: `frontend/src/components/shell/AccountMenu.tsx`
- Modify: `frontend/src/pages/users/index.tsx`（约 326 行，`CredentialPane` 下面挂 `UserImPane`）
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`、`frontend/src/styles/theme.css`

**Interfaces:**
- Consumes: Task 9 的 `imApi`、`myImIdentitiesQueryOptions`、`userImIdentitiesQueryOptions`、`ImIdentity`
- Produces：`useMyImIdentities(poll)`、`useIssueImCode()`、`useUnbindMyIm()`、`useUserImIdentities(id)`、`useBindUserIm()`、`useUnbindUserIm()`；组件 `ImBindModal({ onClose })`、`UserImPane({ user })`

- [ ] **Step 1: 追加 hooks 到 `hooks/useIm.ts`**

```ts
import { myImIdentitiesQueryOptions, userImIdentitiesQueryOptions } from '@/api/modules/im'

export function useMyImIdentities(poll = false) {
  return useQuery(myImIdentitiesQueryOptions(poll))
}

/** 生成绑定码。成功不弹「已保存」—— 结果是一串只出现这一次的码,由弹窗接住显示。 */
export function useIssueImCode() {
  const notify = useUIStore((s) => s.notify)
  return useMutation({
    mutationFn: imApi.issueCode,
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

function useImMutation<V>(fn: (v: V) => Promise<unknown>) {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['im-identities'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

export function useUnbindMyIm() {
  return useImMutation((channel: string) => imApi.unbindMine(channel))
}

export function useUserImIdentities(id: number) {
  return useQuery(userImIdentitiesQueryOptions(id))
}

export function useBindUserIm() {
  return useImMutation((v: { id: number; channel: string; platformUserId: string }) =>
    imApi.bindUser(v.id, v.channel, v.platformUserId))
}

export function useUnbindUserIm() {
  return useImMutation((v: { id: number; channel: string }) => imApi.unbindUser(v.id, v.channel))
}
```

（把新 import 合并进文件顶部已有的 `@/api/modules/im` 导入。）

- [ ] **Step 2: 写 `components/modals/ImBindModal.tsx`**

```tsx
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { MessagesSquare } from 'lucide-react'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'
import { Loading, ErrorState } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import { useIssueImCode, useMyImIdentities, useUnbindMyIm } from '@/hooks/useIm'
import type { ImIdentity } from '@/api/modules/im'

/**
 * IM 账号自助绑定。
 *
 * 流程:点「绑定」→ 服务端生成一次性码,只在这里显示一次 → 用户用**自己的** IM 账号
 * 私聊机器人发这串码 → 平台保证发送者是谁,服务端据此写绑定。这边轮询,绑上了自动更新。
 *
 * 渠道没启用时不给绑:机器人不在跑,码发出去没人收,表现就是「按了没反应」。
 *
 * 由调用方在打开时才挂载,上一次的码不会留到下一次。
 */
export default function ImBindModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<{
    channel: string; code: string; botName?: string; before?: string
  } | null>(null)
  const { data, isLoading, error, refetch } = useMyImIdentities(pending !== null)
  const issue = useIssueImCode()
  const unbind = useUnbindMyIm()

  // 轮询到这个渠道绑上了**一个新的账号**,就把码收起来 —— 那串码已经被用掉了。
  // 只看 bound 不够:重新绑定时旧绑定本来就在。用同一个平台账号重新绑定时 ID 不变,
  // 码会一直显示到弹窗关闭,可以接受。
  const pendingRow = pending && data?.find((r) => r.channel === pending.channel)
  const used = !!pendingRow?.bound && pendingRow.platformUserId !== pending?.before
  const shownCode = pending && !used ? pending : null

  function bind(row: ImIdentity) {
    issue.mutate(row.channel, {
      onSuccess: (r) => setPending({
        channel: row.channel, code: r.code, botName: r.botName || row.botName, before: row.platformUserId,
      }),
    })
  }

  function doUnbind(row: ImIdentity) {
    if (!confirmAction(t('imUnbindConfirm', { channel: row.label }))) return
    unbind.mutate(row.channel)
  }

  return (
    <Modal open title={<><MessagesSquare size={16} /> {t('imBindTitle')}</>} sub={t('imBindSub')} onClose={onClose}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {data && data.length === 0 && <div className="im-empty">{t('imBindEmpty')}</div>}
      {data && data.length > 0 && (
        <div className="im-bind-list">
          {data.map((row) => (
            <div key={row.channel} className="im-bind-row">
              <span className="im-ch-name">{row.label}</span>
              <span className={clsx('um-tag', row.bound && 'on')}>
                {!row.enabled ? t('imChannelOff') : row.bound ? t('imBound') : t('imUnbound')}
              </span>
              {row.bound && <code className="im-pid">{row.platformUserId}</code>}
              <div className="im-bind-act">
                <Button disabled={!row.enabled || issue.isPending} onClick={() => bind(row)}
                        title={!row.enabled ? t('imChannelOffHint') : undefined}>
                  {row.bound ? t('imRebind') : t('imBind')}
                </Button>
                {row.bound && (
                  <Button variant="danger" disabled={unbind.isPending} onClick={() => doUnbind(row)}>
                    {t('imUnbind')}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      {shownCode && (
        <div className="im-codebox">
          <div className="cell-strong">
            {t('imCodeTitle', {
              channel: data?.find((r) => r.channel === shownCode.channel)?.label ?? shownCode.channel,
              bot: shownCode.botName ? ` @${shownCode.botName}` : '',
            })}
          </div>
          {/* 码是数据,不进 i18n;等宽字体是为了让人能一位一位对着敲。 */}
          <code className="im-code">{shownCode.code}</code>
          <div className="cell-sub">{t('imCodeExpire')}</div>
        </div>
      )}
    </Modal>
  )
}
```

- [ ] **Step 3: 账户菜单加入口 `components/shell/AccountMenu.tsx`**

1. `lucide-react` 导入加 `MessagesSquare`；加 `import ImBindModal from '@/components/modals/ImBindModal'`。
2. `const [mfaOpen, setMfaOpen] = useState(false)` 下加 `const [imOpen, setImOpen] = useState(false)`。
3. MFA 那个 `um-row` 按钮之后加：

```tsx
            <button className="um-row" onClick={() => { setOpen(false); setImOpen(true) }}>
              <MessagesSquare size={15} />{t('imBindTitle')}
            </button>
```

4. 末尾 `{mfaOpen && …}` 之后加 `{imOpen && <ImBindModal onClose={() => setImOpen(false)} />}`。
5. 顶部注释里「二次验证的自助绑定也在这里」一句后补：「IM 账号绑定同理 —— 审批人往往不是管理员。」

- [ ] **Step 4: 写 `components/permission/UserImPane.tsx`**

```tsx
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MessagesSquare, Unlink } from 'lucide-react'
import { Card, CardHead } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { confirmAction } from '@/lib/confirm'
import { useBindUserIm, useUnbindUserIm, useUserImIdentities } from '@/hooks/useIm'
import type { UserView } from '@/types'

/**
 * 管理员代绑 / 解绑某人的 IM 账号。用于换号、离职回收、绑错 —— 与代绑 MFA 同一个
 * 信任假设:管理员声明「这个平台账号是这个人的」。两个动作都进审计链。
 */
export default function UserImPane({ user }: { user: UserView }) {
  const { t } = useTranslation()
  const { data } = useUserImIdentities(user.id)
  const bind = useBindUserIm()
  const unbind = useUnbindUserIm()
  const [draft, setDraft] = useState<Record<string, string>>({})

  if (!data || data.length === 0) return null
  return (
    <Card className="us-dcard">
      <CardHead title={t('usIm')} sub={t('usImSub')} />
      <div className="perm-sec us-cred">
        {data.map((row) => (
          <div key={row.channel} className="us-credrow">
            <div className="us-credl"><MessagesSquare size={14} />{row.label}</div>
            <span className="cell-sub">
              {row.bound ? `${row.platformUserId} · ${t(row.boundBy === 'admin' ? 'imBoundAdmin' : 'imBoundSelf')}` : t('imUnbound')}
            </span>
            <input
              value={draft[row.channel] ?? ''}
              placeholder={t('usImPh')}
              onChange={(e) => setDraft((d) => ({ ...d, [row.channel]: e.target.value }))}
            />
            <Button
              disabled={bind.isPending || !(draft[row.channel] ?? '').trim()}
              onClick={() => bind.mutate(
                { id: user.id, channel: row.channel, platformUserId: (draft[row.channel] ?? '').trim() },
                { onSuccess: () => setDraft((d) => ({ ...d, [row.channel]: '' })) },
              )}
            >
              {t('usImBind')}
            </Button>
            {row.bound && (
              <Button
                variant="danger"
                disabled={unbind.isPending}
                onClick={() => {
                  if (confirmAction(t('usImUnbindConfirm', { name: user.name, channel: row.label }))) {
                    unbind.mutate({ id: user.id, channel: row.channel })
                  }
                }}
              >
                <Unlink size={14} />{t('imUnbind')}
              </Button>
            )}
          </div>
        ))}
      </div>
    </Card>
  )
}
```

- [ ] **Step 5: 用户详情挂上 `pages/users/index.tsx`**

加 `import UserImPane from '@/components/permission/UserImPane'`；把 `{isAdmin && <CredentialPane user={user} />}` 改成：

```tsx
      {isAdmin && <CredentialPane user={user} />}
      {isAdmin && <UserImPane user={user} />}
```

- [ ] **Step 6: 文案与样式**

`locales/zh.ts`（放在 `otpUnboundState` 附近）：

```ts
  imBindTitle: 'IM 账号绑定',
  imBindSub: '绑定后,审批卡片会私信到你的 IM,点按钮即可审批',
  imBindEmpty: '管理员还没有启用任何交互审批渠道',
  imBound: '已绑定',
  imUnbound: '未绑定',
  imChannelOff: '渠道未启用',
  imChannelOffHint: '机器人没在运行,现在生成的码发出去没人收,请联系管理员启用',
  imBind: '绑定',
  imRebind: '重新绑定',
  imUnbind: '解绑',
  imUnbindConfirm: '解绑后,{{channel}} 上将不再收到审批卡片。继续?',
  imCodeTitle: '用你自己的 {{channel}} 账号私聊机器人{{bot}},发送下面这串码:',
  imCodeExpire: '10 分钟内有效,只显示这一次。绑定成功后这里会自动更新。',
  imBoundSelf: '自助绑定',
  imBoundAdmin: '管理员代绑',
```

放在 `usOtpUnbind` 附近：

```ts
  usIm: 'IM 绑定',
  usImSub: '代绑用于换号、离职回收、绑错;代绑与解绑都会写审计',
  usImPh: '平台用户 ID',
  usImBind: '代绑',
  usImUnbindConfirm: '解绑 {{name}} 的 {{channel}} 账号?',
```

`locales/en.ts` 对应位置：

```ts
  imBindTitle: 'IM accounts',
  imBindSub: 'Once bound, approval cards arrive as DMs and you decide with a click',
  imBindEmpty: 'No interactive approval channel has been enabled yet',
  imBound: 'Bound',
  imUnbound: 'Not bound',
  imChannelOff: 'Channel off',
  imChannelOffHint: 'The bot is not running, so a code sent now would go nowhere. Ask an admin to enable it.',
  imBind: 'Bind',
  imRebind: 'Rebind',
  imUnbind: 'Unbind',
  imUnbindConfirm: 'You will stop receiving approval cards on {{channel}}. Continue?',
  imCodeTitle: 'From your own {{channel}} account, DM the bot{{bot}} this code:',
  imCodeExpire: 'Valid for 10 minutes and shown only once. This dialog updates when binding succeeds.',
  imBoundSelf: 'self-bound',
  imBoundAdmin: 'bound by admin',
```

```ts
  usIm: 'IM accounts',
  usImSub: 'For number changes, off-boarding or fixing a wrong binding; both actions are audited',
  usImPh: 'Platform user ID',
  usImBind: 'Bind',
  usImUnbindConfirm: "Unbind {{name}}'s {{channel}} account?",
```

`styles/theme.css` 追加：

```css
.im-empty { font: 400 12.5px/1.6 var(--font-body); color: var(--text-muted); padding: 8px 0; }
.im-bind-list { display: flex; flex-direction: column; gap: 10px; }
.im-bind-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.im-bind-row .um-tag { margin-left: 0; }
.im-pid { font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.im-bind-act { margin-left: auto; display: flex; gap: 8px; }
.im-codebox { margin-top: 16px; padding: 14px 16px; border-radius: 10px; background: var(--accent-subtle); border: 1px solid var(--accent-subtle-border); display: flex; flex-direction: column; gap: 8px; }
.im-code { font: 600 20px var(--font-mono); letter-spacing: 0.08em; color: var(--accent-text); user-select: all; }
```

- [ ] **Step 7: 类型检查与 lint**

Run: `cd frontend && npm run build && npm run lint`
Expected: 无错误。

- [ ] **Step 8: 在真实应用里看一眼**

用 `run-app` skill 起应用：
- 头像菜单出现「IM 账号绑定」；打开后显示「管理员还没有启用任何交互审批渠道」（第 1 期没有交互渠道，这是预期）。
- 以管理员进权限页打开任一用户详情：因为没有交互渠道，「IM 绑定」卡片不出现（`UserImPane` 在列表为空时返回 null）。
- 手机宽度下（开发者工具 390px）菜单与弹窗不出现横向滚动。

第 1 期没有真实交互渠道，完整的绑定流程由后端 `TestIMBind_*` 端到端覆盖；前端绑定流程的人工走查放到第 2 期 Telegram 接入后进行。

- [ ] **Step 9: 提交**

```bash
git add frontend
git commit -m "feat(ui): let users bind IM accounts and admins bind on their behalf

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 11: 文档

**Files:**
- Create: `docs/adr/0020-im-channel-approval.md`
- Modify: `docs/adr/0003-external-lark-approval-integration.md`（状态行）
- Modify: `docs/superpowers/specs/2026-09-11-telegram-approval-design.md`（开头加一段说明）
- Modify: `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（记下第 1 期实现与 spec 的三处偏离）
- Rewrite: `docs/external-approval-setup.md`
- Modify: `README.md`、`docs/features.md`、`DEPLOY.md`

**Interfaces:**
- Consumes: Task 1–10 的实际行为
- Produces: 无代码

- [ ] **Step 1: 新增 `docs/adr/0020-im-channel-approval.md`**

```markdown
# ADR 0020:外部审批改为多渠道 IM,删除审批魔方

- 状态:已采纳(第 1 期:框架 + 飞书群通知;交互渠道分期接入)
- 日期:2026-09-27
- 取代:ADR 0003
- 设计:`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`

## 变更

外部审批不再经过审批魔方。网关直接对接 IM 平台(飞书、Telegram、钉钉、Slack;企业微信只做群
通知),审批卡片私信给审批链上**已绑定**该渠道的成员,点按钮即可通过 / 驳回。多个渠道可以同时
开,任一渠道的决定生效,其余渠道的卡片随即收起。

## 为什么

审批魔方替网关做了两件事:渲染飞书卡片,以及**替网关认人** —— 回调里的 `approver[]` 是它认证过
的飞书账号,网关拿它去比对发起人邮箱来执行禁自审。第二件事是这条集成里最脆弱的地方(见
issue.md 的 EA2、EA4、EA5):一把回调密钥能终审任意待审工单,审批人身份依赖厂商回传的字符串格式,
禁自审靠字符串比对。

新的做法把认人收回网关:

- 平台用户 ID 由平台保证不可伪造;它对应哪个网关账户,只由网关自己的绑定表 `tbl_im_identity`
  决定。绑定靠一次性码:用户用自己的 IM 账号把码私聊给机器人,由此证明账号归属。
- 认出人之后,决策走 `DecideApprovalAs` → `DecideBlockFor`,与控制台是**同一处判断**。渠道代码里
  没有任何判定逻辑。

## 取舍

- **每人每渠道要手动绑一次。** 不用平台通讯录邮箱自动匹配 —— 那等于信任平台目录里的邮箱就是
  网关邮箱,目录配错、外部访客、共享频道都会变成一次提权。
- **升级时在途的魔方工单只能在控制台处理。** 回调端点已删除。
- **迁移删列**(`tbl_approval.external_task_id`),不能与旧二进制共存。
- Telegram long polling 只能单实例;多实例用 webhook 模式。
```

- [ ] **Step 2: ADR-0003 与旧 Telegram spec 标注被取代**

`docs/adr/0003-external-lark-approval-integration.md` 第 3 行 `Status: Accepted(设计已定,实现待 Phase 1)` 改为：

```
Status: Superseded by ADR-0020(2026-09-27 起审批魔方对接已删除)
```

`docs/superpowers/specs/2026-09-11-telegram-approval-design.md` 标题下加：

```markdown
> **已被吸收**:本设计的信任模型(绑定码认人、判定交回 `DecideBlockFor`)已推广到所有渠道,
> 见 `2026-09-27-im-channel-approval-design.md`。「与飞书外审互斥」「切换即失效」两条决定被推翻:
> 新设计里多渠道可同时启用。Telegram 的具体接入以第 2 期计划为准。
```

- [ ] **Step 3: 在 spec 末尾记下第 1 期的实现偏离**

`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md` 末尾追加：

```markdown
## 18. 第 1 期实现记录

与上文的三处偏离,均为实现时的细化,不改变设计意图:

- **迁移拆成三个文件**:`0005_im_channel.sql`(三张表)、`0006_im_lark_notify_keys.sql`(飞书通知
  键搬家)、`0007_drop_approval_cube.sql`(删魔方遗留)。每个迁移和依赖它的代码在同一次提交里落地。
- **绑定码按哈希查找**,而不是取出后常量时间比较:库里只有 `sha256(code)`,查的也是哈希,查找本身
  不泄露码。
- **新增 `GET /users/:id/im-identities`**:权限页要显示某人的绑定状态,§10 漏列了这个读接口。
- **飞书群通知迁移时保留「默认开」语义**:旧 `notify.lark` 默认 true 且生产环境不播种,只配了
  webhook 的部署升级后照常发通知。
- **webhook 入口推迟到第 2 期**:`POST /api/v1/im/:channel/webhook`、`im.callbackBaseURL`、
  `im.callbackAllowIPs` 与来源 IP 白名单都随第一个用到它们的渠道(Telegram)一起落地。第 1 期没有
  真实的交互渠道,提前放一个没有调用方的公开入口只会是一段无人测试的高危代码。
```

- [ ] **Step 4: 重写 `docs/external-approval-setup.md`**

整文件替换为：

```markdown
# IM 渠道审批与通知 · 启用手册

网关直接对接 IM 平台:审批卡片**私信**给审批链上已绑定该渠道的成员,点按钮即可通过 / 驳回;
群通知把新工单推到指定群。设计见 ADR 0020 与
`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`。

- 审批**通过不执行命令**(ADR 0010):通过后由发起人回到「审批」页执行。
- 站内审批始终可用。多个渠道可同时开,任一渠道(或控制台)的决定生效,其余卡片自动收起。
- 这些都是**运行时设置**(存库、在「设置 › IM 渠道」里配),不是部署期环境变量。保存即生效,
  不需要重启。

## 当前支持

| 渠道 | 群通知 | 交互审批 |
|---|---|---|
| 飞书 | ✅ 自定义机器人 webhook | 规划中(第 3 期) |
| Telegram | 规划中 | 规划中(第 2 期) |
| 钉钉 | 规划中 | 规划中(第 4 期) |
| Slack | 规划中 | 规划中(第 5 期) |
| 企业微信 | 规划中 | 不支持(平台群机器人无回调) |

## 飞书群通知

1. 飞书群 › 设置 › 群机器人 › 添加「自定义机器人」,复制 webhook 地址;需要签名校验时同时开启
   「签名校验」并复制密钥。
2. 网关「设置 › IM 渠道」:
   - 「控制台外网地址」填审批人能打开的网关地址,卡片里「前往审批」按钮用它拼链接。
   - 飞书卡片打开「群通知」,填 webhook 与签名密钥,点「发一条测试通知」。
3. 保存。此后每张新审批单(终端高危命令、执行窗口申请、含敏感字段的导出、发布单审批阶段)都会
   往群里推一张卡片。卡片里的命令已脱敏(口令替换为 `***`)。

签名密钥加密落库、接口不回显;输入框留空保存表示保持原值。

## 交互审批的账号绑定(交互渠道上线后适用)

审批卡片上的点击只认**绑定过的账号**。每个人在每个渠道绑定一次:

1. 头像菜单 › 「IM 账号绑定」,在对应渠道点「绑定」,弹出一串 `BIND-XXXXXXXX`(10 分钟有效,只显示一次)。
2. 用**自己的** IM 账号私聊机器人,发送这串码。机器人回复「已绑定到 张三(zhang@…)」即完成。

管理员可在「权限 › 用户详情 › IM 绑定」里代绑或解绑(换号、离职回收、绑错时用),两个动作都写审计。

点击卡片时:

- 没绑定、账户停用、服务账号 → 拒绝,且审计里记一行「有人试过」。
- 不在审批链上、自己发起的单(未开「允许自审」)→ 拒绝,提示与控制台完全一致。
- 已被别人处理 → 提示当前状态,不会重复决策。

## 从审批魔方升级

- 审批魔方对接已整体删除:回调端点 `/api/v1/approvals/lark/callback` 不再存在,`approval.external.*`
  设置(含加密的令牌与回调密钥)由迁移 `0007` 删除。
- **升级前**请在审批魔方侧清空待审单:升级后那些飞书卡片点击无效,只能回控制台处理。
- 原「设置 › 通知」里的飞书群通知配置由迁移 `0006` 自动搬到「设置 › IM 渠道 › 飞书」,行为不变。
- 迁移会删列,不能与旧版本进程共存。按 README 顺序:备份 → 停旧进程 → migrate → 起新进程。
```

- [ ] **Step 5: README / features / DEPLOY**

1. `README.md` 第 6 行 `可经 Webhook /\n飞书外推` 一句改为 `可经 Webhook / IM 渠道(飞书等)外推`（保持原句断行风格）。
2. `README.md` 约 206 行 ADR 列表：`0003 外部飞书审批` 改为 `0020 多渠道 IM 审批(取代 0003)`，并把它挪到列表末尾按编号排序的位置。
3. `docs/features.md`：
   - 约 142–143 行：`auto-reject` 置为已过期、审计、通知发起人并回写\n  审批魔方` → `…通知发起人并收起各 IM 渠道上的审批卡片`。
   - 约 153–156 行整条「外部飞书审批（审批魔方）」替换为：

```markdown
- **IM 渠道审批**：审批卡片私信给审批链上已绑定该渠道的成员，点按钮即可通过 / 驳回（不执行）；
  平台账号经一次性绑定码映射到网关账户，判定与控制台是同一处（`DecideBlockFor`）；多渠道可同时开，
  任一处决定后其余卡片自动收起。第 1 期只有飞书**群通知**，交互渠道分期接入。
  见 `docs/external-approval-setup.md` 与 ADR 0020。
```

   - 约 212 行 `（飞书 / 审批魔方 / 控制台各入口都算数）` → `（IM 渠道 / 控制台各入口都算数）`；约 214 行 `发布单终止时作废其审批并回写审批魔方。` → `发布单终止时作废其审批并收起 IM 卡片。`
   - 约 326–327 行「审批」条目删去 `、外部飞书审批（审批魔方）\n  的地址 / 令牌 / 回调根地址 / 回调密钥 / 回调 IP 白名单（令牌与回调密钥加密落库、不回显）`，改为以 `允许自审。` 结尾。
   - 约 329 行「通知」条目改为：`- **IM 渠道**：控制台外网地址；各渠道的群通知与交互审批开关、凭据（im.* 下的密钥一律加密落库、不回显）、运行状态、测试通知 / 测试卡片。`；原句后半 `同页的「邮件通知」「拦截即时通知」开关后端目前不读。` 挪到新的 `- **通知**：` 条目单独成行。
4. `DEPLOY.md` 约 145 行 `(审批魔方令牌等)` → `(IM 渠道密钥等)`。

- [ ] **Step 6: 确认文档里没有指向已删除内容的说法**

Run: `grep -rnE '审批魔方|approval\.external|lark/callback|notify\.lark' README.md DEPLOY.md docs/features.md docs/external-approval-setup.md`
Expected: 只剩 `external-approval-setup.md`「从审批魔方升级」一节里有意保留的提及。

- [ ] **Step 7: 提交**

```bash
git add docs README.md DEPLOY.md
git commit -m "docs: record the move from the approval cube to IM channels

ADR 0020 supersedes ADR 0003; the setup guide now covers Lark group
notifications, account binding and the upgrade path, and the spec notes
where phase one refined it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## 完成标准

- `cd backend && go vet ./... && go test ./...` 全绿；`cd frontend && npm run build && npm run lint` 无错误。
- `grep -rnE '审批魔方|approval\.external|DecideApprovalExternal' backend --include='*.go'` 只命中丢弃旧键的那一行和两个断言它已删除的测试。
- 设置页有「IM 渠道」分区，飞书群通知可测；头像菜单有「IM 账号绑定」。
- 第 2 期（Telegram）计划在本期合并后编写：它是第一个真实的 `Approver`，会用上 `im.callbackBaseURL`、`im.callbackAllowIPs`、`ValidateCallbackBaseURL` 和 webhook 路由 `POST /api/v1/im/:channel/webhook`，并在那时把 `ipAllowed` 以 `imchannel.IPAllowed` 的形式加回来。
