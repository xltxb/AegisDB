# 多渠道 IM 审批 · 第 5 期（Slack 交互审批）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Slack 成为交互审批渠道：审批卡片（Block Kit 消息）私信给已绑定的成员，点「通过 / 驳回」即决定；用户私聊机器人发绑定码完成绑定；入站默认走 Socket Mode 长连接（不需要公网入口），可选 webhook（Slack 把交互与事件 POST 到网关，Signing Secret 验签）。

**Architecture:** 新包 `internal/imchannel/slack` 的 `*App`（一个装进工作区的 Slack 应用）。出站（`auth.test`、`conversations.open`、`chat.postMessage`、`chat.update`、`conversations.history`、`chat.postEphemeral`、`apps.connections.open`）用标准库直接调 Slack Web API，与飞书、钉钉、Telegram 适配器同一个写法。入站自己实现 Socket Mode 协议（一次 `apps.connections.open` 换 `wss` 地址 + 一条 JSON 信封的 WebSocket，先确认再处理），拨号走网关带 SSRF 防护的拨号器；webhook 模式由 `HandleWebhook` 验签后交给同一组处理函数（`inbound.go`）。另建 `slack/slacktest` 假 Slack：Web API + Socket Mode 网关 + 签名工具，单元测试和端到端测试都不连真实 Slack。判定仍只在 `DecideBlockFor`。

**Tech Stack:** Go 1.25 · **无新依赖**（已有的 `github.com/gorilla/websocket v1.5.3`）· React 19 + TS。

**Spec:** `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（§1 信任模型、§2.2 接口、§2.4 多实例、§4 绑定、§5 出站、§6 入站、§7 决策、§8 收卡、§9 配置、§14 分期、§15 失败朝严、§16 测试；§18–§21 是前四期的实现记录）。

**范围说明：** spec §14 的第 5 期。Slack 群通知（Incoming Webhook）属于第 6 期，本期 Slack 登记为 `Caps{Approve: true}`。

**代码已预验证：** 写计划时在基于 `2b88f1f` 的临时工作树里按 Task 1–6 原文逐步落地跑过：slack 包测试（`-race`、`-count=3`）、bootstrap 里 Slack / 钉钉 / 飞书 / IM 设置与 webhook 的端到端测试、`go vet ./...`、前端 build / lint / 291 个单元测试全部通过，gofmt 干净。执行时照抄即可；与仓库现状对不上的地方以仓库为准并在报告里说明。

### 与 spec 的偏离（执行前请人确认；Task 7 写进 spec §22）

1. **不用 `slack-go/slack`，自己实现 Socket Mode。** spec §14 写「Socket Mode（`slack-go/slack`）」。读了 v0.30.1（2026-10-04 发布）的 `socketmode` 源码：它能注入拨号器（`OptionDialer`）与 HTTP 客户端、Debug 默认关、`RunContext` 认 ctx —— 与钉钉 SDK 不同，**没有** SSRF 或泄密的硬伤。不用它的理由是错误分类：`connect` 只把 `invalid_auth` / `account_inactive` / `not_authed` / `token_revoked` 四个码当致命，其余（最常见的配置错 `not_allowed_token_type` —— 把 Bot Token 填进了 App Token 一栏、`missing_scope`、API 地址填错回来的页面）都以最长 5 分钟的退避**无限重连**，只往 `Events` 通道里发一个事件。网关的约定是永久性错误让 `Run` 返回、设置页显示原因（§20、§21），套一层 SDK 也得先自己调一次 `apps.connections.open` 来分类，剩下的协议只有约 150 行（换地址、`hello`、`disconnect`、信封、`{"envelope_id"}` 确认）。四个适配器因此保持同一种写法：标准库 Web API + `imchannel.Retry` + 假服务器。`slack-go` 源码里有一条值得照抄的经验：Slack 定时发 WebSocket ping，适配器收到就续读超时（Task 3）。
2. **卡片里的外部文本全部用 `plain_text` 与 `rich_text_preformatted`，不用 mrkdwn 转义。** spec §5.2 写「Slack 转义 `& < >`」。`plain_text` 与 `rich_text` 的文本 Slack 不解析：发起人写的 `[点我](…)`、`<!channel>`、SQL 里的 `<` `&` `` ``` `` 原样显示，没有可注入的链接、提及或代码块逃逸 —— 比转义少一类出错的机会。只有顶层 `text`（通知预览，mrkdwn）与机器人回的私聊文本、点击结果按 spec 转义 `& < >`。
3. **点击结果是一条「仅你可见」的消息（`chat.postEphemeral`）。** Slack 的按钮点击没有 toast。Socket Mode 下**先确认信封、再处理**（Slack 要 3 秒内确认，判定要查库）；确认之后处理用脱离取消的 ctx，`Reload` 不会丢掉一次已确认的点击。webhook 模式下判定同步做完再回 200，回点击者的那条消息放到协程里发。
4. **收卡保留原文。** Slack 改消息只能整条替换 blocks。收卡先用 `conversations.history`（`latest=<ts>&inclusive=true&limit=1`）取回卡片原来的 blocks，把那组按钮（`block_id = aegis_actions`）换成一行终态，其余原样保留；取不到（缺 `im:history`、消息太旧）或 Slack 不收取回的 blocks（`invalid_blocks`）时退回「单号 + 终态」。消息已被删除（`message_not_found`）算收起。
5. **新增配置键 `im.slack.apiBase`**（缺省 `https://slack.com/api`，测试与 Enterprise 代理用，走出站 URL 校验）。两种 token 在**保存时**按前缀校验（`xoxb-` / `xapp-`）：填反是 Slack 最常见的配置错。
6. **机器人名取 `auth.test` 的 `user`**（机器人用户名）。`Run` 一开始就调它：顺带验证 Bot Token、拿到机器人自己的成员 ID（私聊里忽略自己发的消息）；`auth.test` 没有 `bot_id`（填了用户 token `xoxp-`）按永久性错误报。
7. **webhook 只有一个地址**：Slack 后台「Interactivity & Shortcuts」与「Event Subscriptions」的 Request URL 都填 `<回调根地址>/api/v1/im/slack/webhook`；网关按 `Content-Type` 分辨（表单 `payload=` 是交互，JSON 是事件）。时间戳窗口 ±5 分钟（spec §6）。

## Global Constraints

- 模块名 `velagateway`，Go 1.25；后端命令都在 `backend/` 下执行；跑后端测试需要 `createdb vela_test`，**绝不用** `vela_gateway`。
- 本机没有 MySQL：`internal/osc` 整包与 bootstrap 的 `TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes` 是已知基线失败，不算回归。bootstrap 整包约 5 分钟，前台跑。
- 本期**没有**数据库迁移，**没有**新 Go 依赖。新配置键全部落在 `tbl_setting`。
- 配置键：`im.slack.approval`（bool）、`im.slack.botToken`🔒（`xoxb-…`）、`im.slack.appToken`🔒（`xapp-…`，Socket Mode 用）、`im.slack.mode`（`socket` | `webhook`，缺省 `socket`）、`im.slack.signingSecret`🔒（webhook 模式用）、`im.slack.apiBase`（缺省 `https://slack.com/api`）。三个🔒键名字里有 token / secret，`isSecretKey` 已经认得（加密落库、接口不回显）。
- 渠道标识 `imchannel.Slack` = `"slack"`，`imchannel.Label` = `Slack`（已存在）。webhook 路径 `/api/v1/im/slack/webhook`（已有的公共路由 `POST /api/v1/im/:channel/webhook`）。
- 平台用户 ID 是 Slack **成员 ID**，白名单 `^[UW][A-Z0-9]{2,31}$`：绑定取 `message.im` 事件的 `event.user`；认点击取 `block_actions` 载荷的 `user.id`（Slack 保证是点击者）。私聊频道 ID 白名单 `^D[A-Z0-9]{2,31}$`，回「仅你可见」消息的会话 ID 白名单 `^[CDG][A-Z0-9]{2,31}$`。
- 卡片：`chat.postMessage` 带 `blocks`（JSON 串）、`text`（通知预览，mrkdwn 转义）、`unfurl_links=false`、`unfurl_media=false`。按钮 `action_id`：`aegis_approve` / `aegis_reject` / `aegis_open`（「前往控制台」链接按钮）；那组按钮的 `block_id` = `aegis_actions`；按钮 `value` = `ap:<ApNo>:a` | `ap:<ApNo>:r`，白名单 `^ap:([A-Za-z0-9-]{1,32}):([ar])$`。按钮数据不带任何权限信息。
- msgRef：`<D 频道 ID>|<ts>|<ApNo>`，白名单 `^(D[A-Z0-9]{2,31})\|([0-9]{1,12}\.[0-9]{1,8})\|([A-Za-z0-9-]{1,32})$`。
- Block Kit 上限：header 150 字符、section 文本 3000、命令（`rich_text_preformatted`）截到 2900、链接按钮 URL 3000（超了就不放这颗按钮）。空文本不发（Slack 拒收空的 text 对象）。
- 所需权限（启用手册写给管理员）：Bot Token Scopes `chat:write`、`im:write`、`im:history`；订阅 bot 事件 `message.im`；App Home 打开「Messages Tab」并允许用户发消息；打开 Interactivity。Socket Mode 另需打开 Socket Mode、建一个带 `connections:write` 的 App-Level Token。
- Socket Mode：`apps.connections.open`（App Token）换地址；收 `hello` 忽略；`disconnect` 换连接（`reason = link_disabled` 是永久性错误：应用关掉了 Socket Mode）；带 `envelope_id` 的信封**先确认**（回 `{"envelope_id":"…"}`）再处理：`interactive` → 点击、`events_api` → 私聊，其余只确认。
- webhook：`X-Slack-Request-Timestamp` 与当前时间差超过 5 分钟、`X-Slack-Signature` ≠ `v0=hex(HMAC-SHA256(signingSecret, "v0:<ts>:<body>"))`（常量时间比较）→ 403。只在 `mode = webhook` 且配了 Signing Secret 时收。鉴权通过后一律 200；`url_verification` 回 `{"challenge":…}`（challenge 限 `^[A-Za-z0-9_-]{1,128}$`）。
- 失败朝严（spec §15）：渠道未开交互审批 → 长连接不启动、不发卡片、webhook 403；畸形的点击（value 不是网关造的、未知按钮、点击者 ID 不合白名单）不进 sink；频道消息、编辑 / 删除等带 `subtype` 的消息、机器人（含网关自己）发的消息不进 sink、不回复。私聊按 `event_id` 去重（Slack 没收到确认会重投）。
- 出站只用 `Deps.HTTP`（带 SSRF 防护，经 `imchannel.NoRedirects` 复制一份不跟随重定向的）。WebSocket 拨号用 `imchannel.GuardedDial(a.client)`；拿不到时 `Run` 报错，**不许**退回默认拨号器。Slack 下发的 `wss` 地址也过一遍 `Deps.ValidateURL`（`wss` 按 `https`、`ws` 按 `http`）。
- 永久性错误（Slack `ok:false` 的错误码，除 `ratelimited` / `internal_error` / `fatal_error` / `service_unavailable` / `request_timeout`；API 地址填错导致的 3xx / 4xx / 解析不了的 2xx / 没有 `ok` 字段的 2xx；出站校验拒绝；`link_disabled`）让 `Run` 返回错误，进 `LastError`；网络错误、DNS 解析失败、5xx、429 与上面那五个码退避重试。纯配置错（Socket Mode 缺 App Token、webhook 模式缺 Signing Secret、拿不到受控拨号器）在任何网络请求之前报。
- Builder（`slack.New`）在 Hub 锁内调用，必须廉价、不做 I/O。`BotName` 只读 `Run` 里缓存的值。
- 三个密钥在所有错误文本里替换成 `<bot-token>` / `<app-token>` / `<signing-secret>`。
- 代码注释沿用仓库风格（中文，讲清「为什么」）；提交信息英文 conventional commits，结尾 `Co-Authored-By: <执行提交的模型名> <noreply@anthropic.com>`。
- 前端验证：`cd frontend && npm run build && npm run lint && npm run test:unit`。

## Review Focus

spec 没有逐条写出、但最可能伤到使用者的五类输入。每条的测试放进负责它的任务：

1. **两种 token 填反、或填了用户 token**：Slack 后台同时给出 `xoxb-`、`xapp-`、`xoxp-` 三种 token，填反是最常见的配置错；Slack 只回一个 `not_allowed_token_type`，SDK 会拿它无限重连。保存时按前缀拦下（Task 5 `TestIMSettings_SlackSettingsAreValidatedOnSave`）；绕过保存校验进库的，`Run` 停下并报原因（Task 3 `TestRun_ABotTokenInTheAppTokenSlotStopsTheLoop`，Task 2 `TestAuthTest_AUserTokenIsRefused`）。
2. **点了「前往控制台」**：链接按钮被点时 Slack 照样发一次 `block_actions`。当成决策会出错，回一句「无法识别的操作」会让每个想去控制台的人都收到一条莫名其妙的消息 —— 确认后什么都不做（Task 3 `TestRun_TheConsoleLinkButtonIsAcknowledgedAndIgnored`）。
3. **点击确认之后、处理之前恰好 Reload**：信封已确认，Slack 不会重投；处理若跟着 `Run` 的 ctx 一起取消，这次批准就悄无声息地丢了（Task 3 `TestRun_AClickAcknowledgedBeforeAReloadIsStillDecided`）。
4. **截获的 webhook 请求被重放**：签名对、时间戳是 6 分钟前的 —— 拒（Task 4 `TestWebhook_RefusesBadSignaturesAndStaleRequests`）。
5. **Slack 重投同一条私聊**：没及时收到确认（或 webhook 应答慢）时 Slack 会重投，绑定码第二次到达时已被消费，用户会在「已绑定」之后又收到「无效或已过期」。按 `event_id` 去重（Task 3 `TestRun_ARedeliveredEventIsHandledOnce`，Task 4 `TestWebhook_ADirectMessageIsAnsweredAndARetryIsHandledOnce`）。

---

## 文件结构

**新建（后端）**

| 文件 | 职责 |
|---|---|
| `backend/internal/imchannel/slack/card.go` | Block Kit 卡片、收卡后的 blocks、msgRef 与按钮 value 的生成与解析、mrkdwn 转义 |
| `backend/internal/imchannel/slack/app.go` | `*App`：构造、Web API 调用（错误分类、脱敏）、`auth.test`、发卡 / 收卡 / 回消息 |
| `backend/internal/imchannel/slack/inbound.go` | 点击与私聊的处理（Socket Mode 与 webhook 共用） |
| `backend/internal/imchannel/slack/socket.go` | `Run`、Socket Mode 连接与重连、信封分发与确认 |
| `backend/internal/imchannel/slack/webhook.go` | `HandleWebhook`：验签、时间窗、url_verification、分发 |
| `backend/internal/imchannel/slack/slacktest/server.go` | 假 Slack Web API：调用记录、故障注入、消息存储、签名工具 |
| `backend/internal/imchannel/slack/slacktest/socket.go` | 假 Socket Mode 网关：发 ticket、WebSocket、推信封并等确认、交互与事件载荷 |
| `backend/internal/imchannel/slack/card_test.go` `app_test.go` `socket_test.go` `webhook_test.go` | 适配器测试 |
| `backend/internal/bootstrap/im_slack_test.go` | Slack 端到端测试 |

**修改（后端）**：`service/service.go`（登记 Slack）、`handler/admin.go`（`im.slack.*` 校验）、`bootstrap/im_helpers_test.go`（通用的状态轮询）、`bootstrap/im_dingtalk_test.go`（改用它）、`bootstrap/im_settings_test.go`（状态里有 Slack）。

**修改（前端）**：`components/settings/ImChannelsPanel.tsx`、`locales/zh.ts`、`locales/en.ts`。

**修改（文档）**：`docs/external-approval-setup.md`、spec（新增 §22）、`docs/features.md`、`docs/adr/0020-im-channel-approval.md`（状态行）。

---
### Task 1: Slack 卡片：Block Kit、收卡、msgRef 与按钮 value

纯函数，没有网络。后面所有任务都靠这里的名字（`action_id`、`block_id`、msgRef 与 value 的格式）。

**Files:**
- Create: `backend/internal/imchannel/slack/card.go`
- Create: `backend/internal/imchannel/slack/card_test.go`

**Interfaces:**
- Consumes: `imchannel.ApprovalCard`、`imchannel.CardState`、`model.RiskHigh`、`model.StatusApproved` / `StatusRejected` / `StatusExpired` / `StatusCancelled`（都已存在）。
- Produces（package `slack`）：
  ```go
  const actionApprove, actionReject, actionOpen, blockActions, footer string
  const headerLimit, sectionLimit, commandLimit, urlLimit int
  var apNoRe, valueRe, userIDRe, dmRe, convRe, tsRe, refRe *regexp.Regexp
  func cardBlocks(c imchannel.ApprovalCard) []any
  func cardText(c imchannel.ApprovalCard) string
  func closedBlocks(original []json.RawMessage, apNo string, final imchannel.CardState) []any
  func closedText(apNo string, final imchannel.CardState) string
  func stateLine(final imchannel.CardState) string
  func cardRef(channel, ts, apNo string) string
  func parseRef(ref string) (channel, ts, apNo string, ok bool)
  func parseValue(v string) (apNo string, approve, ok bool)
  func mrkdwnEscape(s string) string
  func clipRunes(s string, n int) string
  ```

- [ ] **Step 1: 写失败的测试**

Create `backend/internal/imchannel/slack/card_test.go`：

```go
package slack

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

func blocksJSON(t *testing.T, blocks []any) string {
	t.Helper()
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return string(b)
}

// block 是测试里读 blocks 用的形状:header / section 的 text、rich_text 的嵌套文本、actions 的按钮。
type block struct {
	Type    string `json:"type"`
	BlockID string `json:"block_id"`
	Text    *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"text"`
	Elements []struct {
		Type     string `json:"type"`
		Text     any    `json:"text"`
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
		URL      string `json:"url"`
		Style    string `json:"style"`
		Elements []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"elements"`
	} `json:"elements"`
}

func decodeBlocks(t *testing.T, blocks []any) []block {
	t.Helper()
	var out []block
	if err := json.Unmarshal([]byte(blocksJSON(t, blocks)), &out); err != nil {
		t.Fatalf("decode blocks: %v", err)
	}
	return out
}

func TestCardBlocks_ShowEveryExternalTextVerbatimAsPlainText(t *testing.T) {
	c := imchannel.ApprovalCard{
		ApNo: "AP-2294", Kind: "sql", Env: "prod", Tier: "core", Instance: "pg-main", Database: "orders",
		Risk: model.RiskHigh, Initiator: "张伟",
		Reason:  "修数据 [点我](https://evil.example) <!channel> *粗体*",
		Command: "DELETE FROM t WHERE a < b && c > 0; -- ``` <@U0ADMIN>",
	}
	blocks := decodeBlocks(t, cardBlocks(c))
	// 没有一处 mrkdwn:发起人写的链接、@channel、反引号都不能被 Slack 渲染出来
	if js := blocksJSON(t, cardBlocks(c)); strings.Contains(js, `"mrkdwn"`) {
		t.Fatalf("external text must not be mrkdwn: %s", js)
	}
	if h := blocks[0]; h.Type != "header" || h.Text.Type != "plain_text" || h.Text.Text != "🔴 高危 · 高危命令待审批 · AP-2294" {
		t.Fatalf("header = %+v", h)
	}
	if s := blocks[1].Text.Text; !strings.Contains(s, "PROD(core)") || !strings.Contains(s, "pg-main / orders") || !strings.Contains(s, "发起人 张伟") {
		t.Fatalf("summary = %q", s)
	}
	var reason, command string
	for _, b := range blocks {
		if b.Type == "section" && b.Text != nil && strings.HasPrefix(b.Text.Text, "原因 ") {
			reason = b.Text.Text
		}
		if b.Type == "rich_text" && len(b.Elements) == 1 && b.Elements[0].Type == "rich_text_preformatted" {
			command = b.Elements[0].Elements[0].Text
		}
	}
	if reason != "原因 "+c.Reason {
		t.Errorf("reason = %q", reason)
	}
	if command != c.Command {
		t.Errorf("the command must reach Slack unchanged, got %q", command)
	}
}

func TestCardBlocks_ButtonsSayOnlyWhichTicketAndWhatAction(t *testing.T) {
	actions := func(c imchannel.ApprovalCard) block {
		for _, b := range decodeBlocks(t, cardBlocks(c)) {
			if b.Type == "actions" {
				return b
			}
		}
		t.Fatal("no actions block")
		return block{}
	}
	a := actions(imchannel.ApprovalCard{ApNo: "AP-2294", ConsoleURL: "https://gw.corp.io/approvals"})
	if a.BlockID != blockActions || len(a.Elements) != 3 {
		t.Fatalf("actions = %+v", a)
	}
	approve, reject, open := a.Elements[0], a.Elements[1], a.Elements[2]
	if approve.ActionID != actionApprove || approve.Value != "ap:AP-2294:a" || approve.Style != "primary" {
		t.Errorf("approve = %+v", approve)
	}
	if reject.ActionID != actionReject || reject.Value != "ap:AP-2294:r" || reject.Style != "danger" {
		t.Errorf("reject = %+v", reject)
	}
	if open.ActionID != actionOpen || open.URL != "https://gw.corp.io/approvals" || open.Value != "" {
		t.Errorf("console = %+v", open)
	}
	if a := actions(imchannel.ApprovalCard{ApNo: "AP-2294"}); len(a.Elements) != 2 {
		t.Errorf("no console URL → no link button, got %d buttons", len(a.Elements))
	}
	if a := actions(imchannel.ApprovalCard{ApNo: "AP-2294", ConsoleURL: "https://gw.corp.io/" + strings.Repeat("x", urlLimit)}); len(a.Elements) != 2 {
		t.Errorf("a console URL over Slack's limit would get the whole card refused; got %d buttons", len(a.Elements))
	}
}

func TestCardBlocks_LongTextIsClippedToSlacksLimits(t *testing.T) {
	long := strings.Repeat("长", 5000)
	for _, b := range decodeBlocks(t, cardBlocks(imchannel.ApprovalCard{ApNo: "AP-1", Instance: long, Initiator: "x", Reason: long, Command: long})) {
		switch b.Type {
		case "header":
			if n := utf8.RuneCountInString(b.Text.Text); n > headerLimit {
				t.Errorf("header is %d characters", n)
			}
		case "section":
			if n := utf8.RuneCountInString(b.Text.Text); n > sectionLimit {
				t.Errorf("section is %d characters", n)
			}
		case "rich_text":
			if n := utf8.RuneCountInString(b.Elements[0].Elements[0].Text); n > commandLimit {
				t.Errorf("command is %d characters", n)
			}
		}
	}
}

func TestCardBlocks_LeaveOutAnEmptyReasonAndCommand(t *testing.T) {
	// Slack 拒收空的 text 对象:没有原因、没有命令的单子(执行窗口申请)不能因此发不出去。
	for _, b := range decodeBlocks(t, cardBlocks(imchannel.ApprovalCard{ApNo: "AP-1", Kind: "window", Initiator: "x", Reason: "  "})) {
		if b.Type == "rich_text" || (b.Text != nil && strings.TrimSpace(b.Text.Text) == "") {
			t.Fatalf("empty block %+v", b)
		}
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "原因") {
			t.Fatalf("a blank reason must be left out: %+v", b)
		}
	}
}

func TestCardText_IsTheEscapedNotificationPreview(t *testing.T) {
	if got := mrkdwnEscape("a < b && c > d"); got != "a &lt; b &amp;&amp; c &gt; d" {
		t.Fatalf("escape = %q", got)
	}
	if got := cardText(imchannel.ApprovalCard{ApNo: "AP-1", Kind: "export"}); got != "AegisDB · 敏感字段导出待审批 · AP-1" {
		t.Fatalf("preview = %q", got)
	}
	if got := closedText("AP-1", imchannel.CardState{Text: "已由 A&B 通过"}); got != "AegisDB · 工单 AP-1 · 已由 A&amp;B 通过" {
		t.Fatalf("closed preview = %q", got)
	}
}

func TestCardRef_RoundTripsAndRefusesAnythingElse(t *testing.T) {
	ref := cardRef("D0123ABC", "1700000000.000100", "AP-2294")
	ch, ts, ap, ok := parseRef(ref)
	if !ok || ch != "D0123ABC" || ts != "1700000000.000100" || ap != "AP-2294" {
		t.Fatalf("parseRef(%q) = %q %q %q %v", ref, ch, ts, ap, ok)
	}
	for _, bad := range []string{
		"", "D0123ABC|1700000000.000100", // 缺段
		"C0123ABC|1700000000.000100|AP-1",   // 卡片只发在私聊里
		"D0123ABC|abc|AP-1",                 // ts 不是数字
		"D0123ABC|1700000000.000100|AP 1",   // 单号里有空格
		"D0123ABC|1700000000.000100|AP-1|x", // 多一段
	} {
		if _, _, _, ok := parseRef(bad); ok {
			t.Errorf("parseRef(%q) accepted", bad)
		}
	}
}

func TestParseValue_KnowsOnlyTheGatewaysOwnButtons(t *testing.T) {
	if ap, approve, ok := parseValue("ap:AP-2294:a"); !ok || ap != "AP-2294" || !approve {
		t.Fatalf("approve = %q %v %v", ap, approve, ok)
	}
	if ap, approve, ok := parseValue("ap:AP-2294:r"); !ok || ap != "AP-2294" || approve {
		t.Fatalf("reject = %q %v %v", ap, approve, ok)
	}
	for _, bad := range []string{"", "AP-2294", "ap:AP 1:a", "ap:AP-2294:x", "ap:AP-2294:a:extra", "ap::a"} {
		if _, _, ok := parseValue(bad); ok {
			t.Errorf("parseValue(%q) accepted", bad)
		}
	}
}

func TestClosedBlocks_SwapTheButtonsForTheOutcomeAndKeepTheRest(t *testing.T) {
	c := imchannel.ApprovalCard{ApNo: "AP-2294", Initiator: "张伟", Reason: "清理", Command: "DROP TABLE orders;"}
	var original []json.RawMessage
	if err := json.Unmarshal([]byte(blocksJSON(t, cardBlocks(c))), &original); err != nil {
		t.Fatal(err)
	}
	closed := closedBlocks(original, "AP-2294", imchannel.CardState{Status: model.StatusApproved, Text: "已由 李四(Slack) 通过"})
	js := blocksJSON(t, closed)
	if strings.Contains(js, blockActions) || strings.Contains(js, actionApprove) || strings.Contains(js, actionOpen) {
		t.Fatalf("closed card still has buttons: %s", js)
	}
	if !strings.Contains(js, "DROP TABLE orders;") || !strings.Contains(js, "清理") || !strings.Contains(js, "✅ 已由 李四(Slack) 通过") {
		t.Fatalf("closed card must keep the ticket and show the outcome: %s", js)
	}
	// 终态占了原来那组按钮的位置:块数不变,页脚仍是最后一块
	got := decodeBlocks(t, closed)
	if len(got) != len(original) || got[len(got)-1].Type != "context" {
		t.Fatalf("closed blocks = %+v", got)
	}
}

func TestClosedBlocks_WithoutTheOriginalKeepTheTicketAndTheOutcome(t *testing.T) {
	for status, icon := range map[string]string{
		model.StatusRejected: "❌", model.StatusExpired: "⏱", model.StatusCancelled: "↩",
	} {
		js := blocksJSON(t, closedBlocks(nil, "AP-2294", imchannel.CardState{Status: status, Text: "已处理"}))
		if !strings.Contains(js, "工单 AP-2294 · 已处理") || !strings.Contains(js, icon+" 已处理") || strings.Contains(js, blockActions) {
			t.Errorf("%s: %s", status, js)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/slack/ -run 'TestCard|TestParse|TestClosed' -count=1`
Expected: FAIL，编译错误 `undefined: cardBlocks` 等

- [ ] **Step 3: 实现**

Create `backend/internal/imchannel/slack/card.go`：

```go
package slack

import (
	"encoding/json"
	"regexp"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// Block Kit 的长度上限(字符)。超了 Slack 拒收整条消息 —— 那是一次「卡片发不出去」的静默失败,
// 所以每一段外部文本都先截断。
const (
	headerLimit  = 150
	sectionLimit = 3000
	// commandLimit:命令在通用层已截到 800,这里再兜一次底。
	commandLimit = 2900
	urlLimit     = 3000
)

// action_id 认动作,block_id 认出卡片上那组按钮(收卡时把它换成终态)。按钮的 value 只说「哪张单、
// 什么动作」,不带任何权限信息 —— 权限完全由收到点击后的认人与 DecideBlockFor 决定。
const (
	actionApprove = "aegis_approve"
	actionReject  = "aegis_reject"
	actionOpen    = "aegis_open" // 「前往控制台」:链接按钮被点时 Slack 照样回调一次,网关不做任何事
	blockActions  = "aegis_actions"
	footer        = "AegisDB 数据库网关"
)

// 点击与事件里的 ID 都会流进后面的查询、请求与提示语,一律按白名单收。
var (
	apNoRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)
	valueRe = regexp.MustCompile(`^ap:([A-Za-z0-9-]{1,32}):([ar])$`)
	// userIDRe 是 Slack 成员 ID(U…;Enterprise Grid 里是 W…),也就是绑定表里的平台 ID。
	userIDRe = regexp.MustCompile(`^[UW][A-Z0-9]{2,31}$`)
	// dmRe 是私聊频道;convRe 是任意会话(C 公开频道、G 私有频道、D 私聊)。
	dmRe   = regexp.MustCompile(`^D[A-Z0-9]{2,31}$`)
	convRe = regexp.MustCompile(`^[CDG][A-Z0-9]{2,31}$`)
	tsRe   = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,8}$`)
	refRe  = regexp.MustCompile(`^(D[A-Z0-9]{2,31})\|([0-9]{1,12}\.[0-9]{1,8})\|([A-Za-z0-9-]{1,32})$`)
)

var kindTitle = map[string]string{
	"sql": "高危命令待审批", "window": "执行窗口申请待审批",
	"export": "敏感字段导出待审批", "release": "发布单审批",
}

func title(kind string) string {
	if t := kindTitle[kind]; t != "" {
		return t
	}
	return kindTitle["sql"]
}

// plain 是一段不解析的文本。卡片上**每一段外部文本都用它**(命令用 rich_text_preformatted):
// 发起人写的 [链接](…)、<!channel>、SQL 里的 < & ``` 都原样显示,没有可注入的链接、提及或代码块
// 逃逸,也就不必转义。emoji 关掉:原因里的 :xxx: 照原样显示。
func plain(s string) map[string]any {
	return map[string]any{"type": "plain_text", "text": s, "emoji": false}
}

func section(s string) map[string]any {
	return map[string]any{"type": "section", "text": plain(clipRunes(s, sectionLimit))}
}

func button(label, actionID, style, value string) map[string]any {
	return map[string]any{"type": "button", "action_id": actionID, "style": style, "text": plain(label), "value": value}
}

func footerBlock() map[string]any {
	return map[string]any{"type": "context", "elements": []any{plain(footer)}}
}

// cardBlocks 是私信给审批人的卡片。
func cardBlocks(c imchannel.ApprovalCard) []any {
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	blocks := []any{
		map[string]any{"type": "header", "text": plain(clipRunes(risk+" · "+title(c.Kind)+" · "+c.ApNo, headerLimit))},
		section("环境 " + env + " · 实例 " + c.Instance + " / " + db + "\n发起人 " + c.Initiator),
	}
	if strings.TrimSpace(c.Reason) != "" {
		blocks = append(blocks, section("原因 "+c.Reason))
	}
	if strings.TrimSpace(c.Command) != "" {
		blocks = append(blocks, map[string]any{"type": "rich_text", "elements": []any{
			map[string]any{"type": "rich_text_preformatted", "elements": []any{
				map[string]any{"type": "text", "text": clipRunes(c.Command, commandLimit)},
			}},
		}})
	}
	buttons := []any{
		button("✅ 通过", actionApprove, "primary", "ap:"+c.ApNo+":a"),
		button("❌ 驳回", actionReject, "danger", "ap:"+c.ApNo+":r"),
	}
	// 超长的链接不放:一颗按钮的 URL 超限,Slack 会拒收整张卡片。
	if c.ConsoleURL != "" && len(c.ConsoleURL) <= urlLimit {
		buttons = append(buttons, map[string]any{"type": "button", "action_id": actionOpen, "text": plain("前往控制台"), "url": c.ConsoleURL})
	}
	return append(blocks, map[string]any{"type": "actions", "block_id": blockActions, "elements": buttons}, footerBlock())
}

var mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// mrkdwnEscape 转义 Slack mrkdwn 的三个控制字符。用于顶层 text(通知预览)与机器人发的纯文本:
// 不转义的话,一句「已绑定到 张三<zhang@…>」里的尖括号会被 Slack 当成链接或提及。
func mrkdwnEscape(s string) string { return mrkdwnEscaper.Replace(s) }

// cardText 是卡片的顶层 text:通知横幅与无障碍朗读用它,blocks 里才是正文。
func cardText(c imchannel.ApprovalCard) string {
	return mrkdwnEscape("AegisDB · " + title(c.Kind) + " · " + c.ApNo)
}

func closedText(apNo string, final imchannel.CardState) string {
	return mrkdwnEscape("AegisDB · 工单 " + apNo + " · " + final.Text)
}

// stateLine 是收卡后取代按钮的那一行。
func stateLine(final imchannel.CardState) string {
	icon := "✔"
	switch final.Status {
	case model.StatusApproved:
		icon = "✅"
	case model.StatusRejected:
		icon = "❌"
	case model.StatusExpired:
		icon = "⏱"
	case model.StatusCancelled:
		icon = "↩"
	}
	return icon + " " + final.Text
}

// closedBlocks 是收起后的卡片。Slack 改消息只能整条替换 blocks,而网关不存卡片原文 —— 所以
// original 是收卡前从 Slack 取回的原卡片:把那组按钮换成一行终态,其余原样保留,审批人仍看得到
// 这张单原来是什么。取不到原卡片时只留单号与终态。
func closedBlocks(original []json.RawMessage, apNo string, final imchannel.CardState) []any {
	state := section(stateLine(final))
	if len(original) == 0 {
		return []any{
			map[string]any{"type": "header", "text": plain("工单 " + apNo + " · 已处理")},
			state,
			footerBlock(),
		}
	}
	out := make([]any, 0, len(original))
	replaced := false
	for _, b := range original {
		var head struct {
			BlockID string `json:"block_id"`
		}
		_ = json.Unmarshal(b, &head)
		if head.BlockID == blockActions {
			out = append(out, state)
			replaced = true
			continue
		}
		out = append(out, b)
	}
	if !replaced {
		out = append(out, state)
	}
	return out
}

// cardRef 是 tbl_im_card.msg_ref:私聊频道、消息 ts、单号。收卡要前两者定位消息,单号给取不到
// 原卡片时的那张替代卡片用。
func cardRef(channel, ts, apNo string) string { return channel + "|" + ts + "|" + apNo }

func parseRef(ref string) (channel, ts, apNo string, ok bool) {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// parseValue 读按钮的 value:ap:<ApNo>:a | ap:<ApNo>:r。webhook 模式下点击可以被伪造(验签挡住了
// 伪造者,挡不住一个拿到签名密钥的人),只认网关自己造的形状。
func parseValue(v string) (apNo string, approve, ok bool) {
	m := valueRe.FindStringSubmatch(v)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] == "a", true
}

// clipRunes 把 s 截到至多 n 个字符(含省略号):Slack 的上限是硬的,多一个字符整条消息就被拒。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd backend && go test ./internal/imchannel/slack/ -count=1 && go vet ./internal/imchannel/slack/`
Expected: PASS，vet 无输出

- [ ] **Step 5: Commit**

```bash
cd backend
git add internal/imchannel/slack/card.go internal/imchannel/slack/card_test.go
git commit -m "feat(im): render Slack approval cards as plain-text Block Kit"
```

---
### Task 2: Slack 应用的出站：Web API 客户端、发卡、收卡、回消息

**Files:**
- Create: `backend/internal/imchannel/slack/app.go`
- Create: `backend/internal/imchannel/slack/slacktest/server.go`
- Create: `backend/internal/imchannel/slack/app_test.go`

**Interfaces:**
- Consumes（Task 1）：`cardBlocks`、`cardText`、`closedBlocks`、`closedText`、`cardRef`、`parseRef`、`mrkdwnEscape`、`apNoRe`、`userIDRe`、`dmRe`、`tsRe`；`imchannel.Deps` / `Key` / `NoRedirects` / `Recent`（已存在）。
- Produces（package `slack`）：
  ```go
  const WebhookPath = "/api/v1/im/slack/webhook"
  const defaultBase, modeSocket, modeWebhook string
  var errBadBase error
  type App struct { /* botToken, appToken, signingSecret, base, mode, client, validate, now, bot, seen */ }
  type botInfo struct{ name, userID string }
  type apiError struct{ Method string; HTTP int; Code string; Msg string }
  func New(d imchannel.Deps) imchannel.Channel
  func (a *App) Kind() string
  func (a *App) BotName() string
  func (a *App) self() botInfo
  func permanent(err error) bool
  func (a *App) request(ctx context.Context, method, token string, form url.Values) ([]byte, error)
  func (a *App) scrub(err error) error
  func (a *App) authTest(ctx context.Context) (botInfo, error)
  func (a *App) sendText(ctx context.Context, channel, text string) error
  func (a *App) postEphemeral(ctx context.Context, channel, userID, text string) error
  func (a *App) SendCard(ctx context.Context, userID string, c imchannel.ApprovalCard) (string, error)
  func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error
  ```
- Produces（package `slacktest`）：`New(t) *Server`（字段 `BotToken`、`AppToken`、`SigningSecret`、`BotUserID`、`BotName`）、`Call{Method, Token, Params}` 与 `Call.Get`、`Failure{HTTP, Error, Needed, Raw, Location, Times}`、`SetFail`、`Calls`、`WaitCall`、`Blocks(channel, ts)`、`Sign(secret, ts, body)`、`SignedHeaders(body, contentType)`。假 Web API 在 `<srv.URL>/api/<method>`，所以测试里 `im.slack.apiBase = srv.URL + "/api"`。

- [ ] **Step 1: 写假 Slack 的 Web API**

Create `backend/internal/imchannel/slack/slacktest/server.go`：

```go
// Package slacktest 是一个假的 Slack:记录每次 Web API 调用,按需注入失败,并扮演 Socket Mode 网关
// 把信封推给适配器(socket.go)。适配器和端到端测试都用它,不连真实的 Slack。
package slacktest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type Call struct {
	Method string // Web API 方法名,如 chat.postMessage
	Token  string // Authorization: Bearer 之后的那段
	Params url.Values
}

func (c Call) Get(key string) string { return c.Params.Get(key) }

// Failure 让某个方法回一个 Slack 错误:HTTP 为 0 时按 200 回 {"ok":false,"error":Error}(Slack 的惯例),
// Needed 非空时带上缺的权限。Raw 非空时原样回这段 body(HTTP 为 0 时按 200),Location 非空时带上这个头
// —— 扮演填错的 API 地址:一个回 200 HTML 的页面,或一次重定向。Times > 0 时只失败这么多次。
type Failure struct {
	HTTP     int
	Error    string
	Needed   string
	Raw      string
	Location string
	Times    int
}

type Server struct {
	*httptest.Server
	BotToken      string
	AppToken      string
	SigningSecret string
	BotUserID     string
	BotName       string

	mu         sync.Mutex
	calls      []Call
	fail       map[string]Failure
	nextTS     int
	messages   map[string]string // "<channel>|<ts>" → blocks(JSON)
	tickets    map[string]bool
	nextTicket int
	endpoint   string // 非空时 apps.connections.open 下发它,而不是自己的 /link
}

func New(t testing.TB) *Server {
	s := &Server{
		BotToken: "xoxb-1-test-bot-token-0000", AppToken: "xapp-1-test-app-token-0000",
		SigningSecret: "8f742231b10e8888abcd99edf5d7d85a", BotUserID: "U0BOT0001", BotName: "aegisbot",
		fail: map[string]Failure{}, messages: map[string]string{}, tickets: map[string]bool{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) SetFail(method string, f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// rejectToken 回 Slack 拒绝 token 的错误,并把收到的 token 回显进 response_metadata:扮演一个往错误里
// 回显请求的代理,让「错误里不出现 token」的测试真的有东西可拦(否则删掉 scrub 它们也照样绿)。
// 拿另一种 token 来调(Bot Token 去换 Socket Mode 地址、App Token 去发消息)是 not_allowed_token_type。
func (s *Server) rejectToken(w http.ResponseWriter, got string) {
	code := "invalid_auth"
	if got == s.BotToken || got == s.AppToken {
		code = "not_allowed_token_type"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": false, "error": code,
		"response_metadata": map[string]any{"messages": []string{"[ERROR] token rejected: " + got}},
	})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	method, isAPI := strings.CutPrefix(r.URL.Path, "/api/")
	if !isAPI || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Token: token, Params: r.PostForm})
	f, failing := s.fail[method]
	if failing && f.Times > 0 {
		if f.Times == 1 {
			delete(s.fail, method)
		} else {
			s.fail[method] = Failure{HTTP: f.HTTP, Error: f.Error, Needed: f.Needed, Raw: f.Raw, Location: f.Location, Times: f.Times - 1}
		}
	}
	s.mu.Unlock()
	if failing && (f.Raw != "" || f.Location != "") {
		status := f.HTTP
		if status == 0 {
			status = http.StatusOK
		}
		if f.Location != "" {
			w.Header().Set("Location", f.Location)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(f.Raw))
		return
	}
	if failing {
		status := f.HTTP
		if status == 0 {
			status = http.StatusOK
		}
		body := map[string]any{"ok": false, "error": f.Error}
		if f.Needed != "" {
			body["needed"] = f.Needed
		}
		writeJSON(w, status, body)
		return
	}
	if method == "apps.connections.open" {
		s.open(w, token)
		return
	}
	if token != s.BotToken {
		s.rejectToken(w, token)
		return
	}
	p := r.PostForm
	switch method {
	case "auth.test":
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "url": "https://aegis-test.slack.com/", "team": "Aegis Test", "user": s.BotName,
			"team_id": "T0TEST001", "user_id": s.BotUserID, "bot_id": "B0TEST001",
		})
	case "conversations.open":
		users := p.Get("users")
		if !strings.HasPrefix(users, "U") && !strings.HasPrefix(users, "W") {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "user_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"id": DMChannel(users)}})
	case "chat.postMessage":
		ch := p.Get("channel")
		s.mu.Lock()
		s.nextTS++
		ts := fmt.Sprintf("1700000000.%06d", s.nextTS)
		s.messages[ch+"|"+ts] = p.Get("blocks")
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch, "ts": ts})
	case "chat.update":
		key := p.Get("channel") + "|" + p.Get("ts")
		s.mu.Lock()
		_, found := s.messages[key]
		if found {
			s.messages[key] = p.Get("blocks")
		}
		s.mu.Unlock()
		if !found {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "message_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": p.Get("channel"), "ts": p.Get("ts")})
	case "conversations.history":
		// 取单条消息的标准做法:latest = 它的 ts、inclusive、limit 1。
		latest := p.Get("latest")
		s.mu.Lock()
		blocks, found := s.messages[p.Get("channel")+"|"+latest]
		s.mu.Unlock()
		msgs := []any{}
		if found && p.Get("inclusive") == "true" {
			m := map[string]any{"type": "message", "ts": latest, "user": s.BotUserID, "bot_id": "B0TEST001"}
			if blocks != "" {
				m["blocks"] = json.RawMessage(blocks)
			}
			msgs = append(msgs, m)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": msgs, "has_more": false})
	case "chat.postEphemeral":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_ts": "1700000001.000001"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

// open 扮演 apps.connections.open:App Token 换一个带一次性 ticket 的 Socket Mode 地址。
func (s *Server) open(w http.ResponseWriter, token string) {
	if token != s.AppToken {
		s.rejectToken(w, token)
		return
	}
	s.mu.Lock()
	s.nextTicket++
	ticket := fmt.Sprintf("ticket-%d", s.nextTicket)
	s.tickets[ticket] = true
	endpoint := s.endpoint
	s.mu.Unlock()
	if endpoint == "" {
		endpoint = "ws" + strings.TrimPrefix(s.URL, "http") + "/link?ticket=" + ticket
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": endpoint})
}

// DMChannel 是假 Slack 给某个成员开的私聊频道 ID(与 conversations.open 回的一致):U0OWNER → D0OWNER。
func DMChannel(userID string) string {
	if len(userID) < 2 {
		return "D0NOBODY"
	}
	return "D" + userID[1:]
}

// Blocks 取一条消息当前的 blocks(JSON);没有这条消息时返回 ""。
func (s *Server) Blocks(channel, ts string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messages[channel+"|"+ts]
}

func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// WaitCall 等到出现一次满足 match 的调用(match 为 nil 表示任意一次)。
func (s *Server) WaitCall(t testing.TB, method string, match func(Call) bool) Call {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range s.Calls(method) {
			if match == nil || match(c) {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Sign 是 Slack 给 webhook 请求的签名:v0=hex(HMAC-SHA256(secret, "v0:<ts>:<body>"))。
func Sign(secret string, ts int64, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%d:%s", ts, body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// SignedHeaders 造一组带当前时间戳与签名的 webhook 请求头。contentType 为空时按 JSON(事件);
// 交互是 application/x-www-form-urlencoded。
func (s *Server) SignedHeaders(body, contentType string) map[string]string {
	if contentType == "" {
		contentType = "application/json"
	}
	ts := time.Now().Unix()
	return map[string]string{
		"Content-Type":              contentType,
		"X-Slack-Request-Timestamp": strconv.FormatInt(ts, 10),
		"X-Slack-Signature":         Sign(s.SigningSecret, ts, body),
	}
}
```

- [ ] **Step 2: 写失败的测试**

Create `backend/internal/imchannel/slack/app_test.go`：

```go
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
	"velagateway/internal/model"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings: s,
		HTTP: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext},
			// 与网关的出站客户端(service.newOutboundClient)一样拒绝跟随重定向
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("重定向已被禁止(防 SSRF)") },
		},
		ValidateURL: func(string) error { return nil },
	}
}

// newTestApp 造一个指向假 Slack 的 *App(Socket Mode,两种 token 都配好)。
func newTestApp(t *testing.T, srv *slacktest.Server, extra kv) *App {
	t.Helper()
	s := kv{
		"im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api",
	}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}

var bg = context.Background()

// richText 取 blocks 里 rich_text_preformatted 的文本(即命令)。
func richText(t *testing.T, blocks string) string {
	t.Helper()
	var bs []struct {
		Type     string `json:"type"`
		Elements []struct {
			Elements []struct {
				Text string `json:"text"`
			} `json:"elements"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(blocks), &bs); err != nil {
		t.Fatalf("blocks %q: %v", blocks, err)
	}
	for _, b := range bs {
		if b.Type == "rich_text" {
			return b.Elements[0].Elements[0].Text
		}
	}
	return ""
}

func TestNew_NeedsTheBotToken(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if ch := New(deps(kv{"im.slack.appToken": "xapp-1"})); ch != nil {
		t.Fatal("an app-level token alone cannot send a card")
	}
	a, ok := New(deps(kv{"im.slack.botToken": "xoxb-1"})).(*App)
	if !ok || a.base != "https://slack.com/api" || a.mode != modeSocket || a.Kind() != imchannel.Slack || a.BotName() != "" {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{"im.slack.botToken": "xoxb-1", "im.slack.mode": "webhook", "im.slack.apiBase": "https://slack.corp.io/api/"})).(*App)
	if a.mode != modeWebhook || a.base != "https://slack.corp.io/api" {
		t.Fatalf("overrides: mode=%s base=%s", a.mode, a.base)
	}
}

func TestSendCard_OpensTheDirectMessageAndPostsTheCard(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	const cmd = "DELETE FROM t WHERE a < b & c;"
	ref, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{
		ApNo: "AP-2294", Command: cmd, Risk: model.RiskHigh, Env: "prod", Instance: "pg-main", Initiator: "张伟",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "D0OWNER|1700000000.000001|AP-2294" {
		t.Fatalf("ref = %q", ref)
	}
	open := srv.Calls("conversations.open")
	if len(open) != 1 || open[0].Get("users") != "U0OWNER" || open[0].Token != srv.BotToken {
		t.Fatalf("conversations.open = %+v", open)
	}
	post := srv.Calls("chat.postMessage")[0]
	if post.Get("channel") != "D0OWNER" || post.Get("unfurl_links") != "false" || post.Get("unfurl_media") != "false" ||
		post.Get("text") != "AegisDB · 高危命令待审批 · AP-2294" {
		t.Fatalf("chat.postMessage = %+v", post.Params)
	}
	// 命令原样进了 rich_text:出网关之前不再改写,也没有被 mrkdwn 转义过
	if got := richText(t, post.Get("blocks")); got != cmd {
		t.Fatalf("command in the card = %q", got)
	}
}

func TestSendCard_RefusesMalformedRecipientsAndTickets(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	for _, c := range []struct{ user, apNo string }{
		{"U0 OWNER", "AP-1"}, {"C0CHANNEL", "AP-1"}, {"", "AP-1"}, {"U0OWNER", "AP 1"},
	} {
		if _, err := a.SendCard(bg, c.user, imchannel.ApprovalCard{ApNo: c.apNo}); err == nil {
			t.Errorf("%+v: accepted", c)
		}
	}
	if n := len(srv.Calls("conversations.open")) + len(srv.Calls("chat.postMessage")); n != 0 {
		t.Fatalf("%d requests went out for malformed input", n)
	}
}

func TestSendCard_AMissingScopeNamesTheScope(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("chat.postMessage", slacktest.Failure{Error: "missing_scope", Needed: "chat:write"})
	_, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !permanent(err) || !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "chat:write") {
		t.Fatalf("err = %v", err)
	}
}

func sendAndClose(t *testing.T, srv *slacktest.Server, a *App, final imchannel.CardState) (ref string, updates []slacktest.Call) {
	t.Helper()
	ref, err := a.SendCard(bg, "U0OWNER", imchannel.ApprovalCard{ApNo: "AP-2294", Initiator: "张伟", Command: "DROP TABLE orders;"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CloseCard(bg, ref, final); err != nil {
		t.Fatalf("close: %v", err)
	}
	return ref, srv.Calls("chat.update")
}

func TestCloseCard_KeepsTheCardAndSwapsTheButtonsForTheOutcome(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusApproved, Text: "已由 李四(Slack) 通过"})
	if len(updates) != 1 {
		t.Fatalf("chat.update called %d times", len(updates))
	}
	u := updates[0]
	h := srv.Calls("conversations.history")
	if len(h) != 1 || h[0].Get("latest") != u.Get("ts") || h[0].Get("inclusive") != "true" || h[0].Get("limit") != "1" {
		t.Fatalf("history = %+v", h)
	}
	blocks := u.Get("blocks")
	if u.Get("channel") != "D0OWNER" || strings.Contains(blocks, blockActions) || !strings.Contains(blocks, "已由 李四(Slack) 通过") ||
		richText(t, blocks) != "DROP TABLE orders;" {
		t.Fatalf("chat.update = %+v", u.Params)
	}
}

func TestCloseCard_FallsBackWhenTheOriginalCannotBeRead(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("conversations.history", slacktest.Failure{Error: "missing_scope", Needed: "im:history"})
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusExpired, Text: "已超时"})
	if len(updates) != 1 || !strings.Contains(updates[0].Get("blocks"), "工单 AP-2294 · 已处理") || strings.Contains(updates[0].Get("blocks"), blockActions) {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestCloseCard_RetriesWithoutTheOriginalWhenSlackRefusesIt(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("chat.update", slacktest.Failure{Error: "invalid_blocks", Times: 1})
	_, updates := sendAndClose(t, srv, a, imchannel.CardState{Status: model.StatusRejected, Text: "已驳回"})
	if len(updates) != 2 || !strings.Contains(updates[1].Get("blocks"), "工单 AP-2294 · 已处理") {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestCloseCard_ADeletedMessageCountsAsClosed(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	// 对方把消息删了:这张卡片不会再有能点的按钮,算收起,不必每次扫描都 WARN 一遍。
	if err := a.CloseCard(bg, "D0OWNER|1700000000.000099|AP-2294", imchannel.CardState{Text: "已撤回"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if err := a.CloseCard(bg, "not-a-ref", imchannel.CardState{}); err == nil {
		t.Fatal("an unknown ref must be refused")
	}
}

func TestReplies_AreEscapedForMrkdwn(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	if err := a.sendText(bg, "D0OWNER", "已绑定到 张三<zhang@corp.io> & co"); err != nil {
		t.Fatal(err)
	}
	if err := a.postEphemeral(bg, "D0OWNER", "U0OWNER", "a < b"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Calls("chat.postMessage")[0].Get("text"); got != "已绑定到 张三&lt;zhang@corp.io&gt; &amp; co" {
		t.Errorf("reply = %q", got)
	}
	if e := srv.Calls("chat.postEphemeral")[0]; e.Get("text") != "a &lt; b" || e.Get("user") != "U0OWNER" || e.Get("channel") != "D0OWNER" {
		t.Errorf("ephemeral = %+v", e.Params)
	}
}

func TestAuthTest_ReadsTheBotsNameAndID(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	info, err := a.authTest(bg)
	if err != nil || info.name != srv.BotName || info.userID != srv.BotUserID {
		t.Fatalf("auth.test = %+v, %v", info, err)
	}
}

func TestAuthTest_AUserTokenIsRefused(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	// 用户 token(xoxp-)也能过 auth.test,只是没有 bot_id:拿它发的卡片会以那个人的名义出现。
	srv.SetFail("auth.test", slacktest.Failure{Raw: `{"ok":true,"user":"zhangwei","user_id":"U0OWNER","team_id":"T0TEST001"}`})
	if _, err := a.authTest(bg); err == nil || !permanent(err) || !strings.Contains(err.Error(), "xoxb-") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequest_ARejectedTokenIsPermanentAndNeverShown(t *testing.T) {
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token"
	a := newTestApp(t, srv, kv{"im.slack.botToken": wrong})
	_, err := a.authTest(bg)
	if err == nil || !permanent(err) || !strings.Contains(err.Error(), "invalid_auth") ||
		!strings.Contains(err.Error(), "<bot-token>") || strings.Contains(err.Error(), wrong) {
		t.Fatalf("err = %v", err)
	}
}

func TestPermanent_SortsSlacksErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 200, Code: "invalid_auth"}, true},
		{&apiError{HTTP: 200, Code: "not_allowed_token_type"}, true},
		{&apiError{HTTP: 200, Code: "missing_scope"}, true},
		{&apiError{HTTP: 302, Msg: "响应无法解析,检查 API 地址"}, true},
		{&apiError{HTTP: 404}, true},
		{fmt.Errorf("%w: x", errBadBase), true},
		{&apiError{HTTP: 200, Code: "ratelimited"}, false},
		{&apiError{HTTP: 200, Code: "internal_error"}, false},
		{&apiError{HTTP: 200, Code: "fatal_error"}, false},
		{&apiError{HTTP: 200, Code: "service_unavailable"}, false},
		{&apiError{HTTP: 200, Code: "request_timeout"}, false},
		{&apiError{HTTP: http.StatusTooManyRequests}, false},
		{&apiError{HTTP: 503}, false},
		{errors.New("connection refused"), false},
	} {
		if got := permanent(c.err); got != c.want {
			t.Errorf("permanent(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestRequest_AWrongAPIAddressIsPermanent(t *testing.T) {
	for name, f := range map[string]slacktest.Failure{
		"a console page":  {Raw: "<html>console</html>"},
		"a redirect":      {HTTP: http.StatusFound, Location: "https://example.com/", Raw: "moved"},
		"json without ok": {Raw: "{}"},
	} {
		srv := slacktest.New(t)
		a := newTestApp(t, srv, nil)
		srv.SetFail("auth.test", f)
		if _, err := a.authTest(bg); err == nil || !permanent(err) || !strings.Contains(err.Error(), "检查 API 地址") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("auth.test", slacktest.Failure{HTTP: http.StatusBadGateway, Raw: "<html>bad gateway</html>"})
	if _, err := a.authTest(bg); err == nil || permanent(err) {
		t.Errorf("a 5xx page is Slack having a bad moment, not a wrong address: %v", err)
	}
}

func TestRequest_AnAddressRefusedByTheOutboundCheckIsPermanentButDNSIsNot(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	a.validate = func(string) error { return errors.New("解析到内网地址") }
	if _, err := a.authTest(bg); !errors.Is(err, errBadBase) || !permanent(err) {
		t.Fatalf("err = %v", err)
	}
	a.validate = func(string) error { return &net.DNSError{Err: "no such host", Name: "slack.com", IsTemporary: true} }
	if _, err := a.authTest(bg); err == nil || permanent(err) {
		t.Fatalf("a DNS failure may be a passing network problem: %v", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out to a refused address", n)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/slack/ -count=1`
Expected: FAIL，编译错误 `undefined: New` 等

- [ ] **Step 4: 实现**

Create `backend/internal/imchannel/slack/app.go`：

```go
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd backend && go test ./internal/imchannel/slack/... -count=1 && go vet ./internal/imchannel/slack/...`
Expected: PASS，vet 无输出

- [ ] **Step 6: Commit**

```bash
cd backend
git add internal/imchannel/slack/app.go internal/imchannel/slack/app_test.go internal/imchannel/slack/slacktest/server.go
git commit -m "feat(im): send and close Slack approval cards over the Web API"
```

---
### Task 3: Socket Mode 长连接：Run、信封分发、点击与私聊

**Files:**
- Create: `backend/internal/imchannel/slack/inbound.go`
- Create: `backend/internal/imchannel/slack/socket.go`
- Create: `backend/internal/imchannel/slack/slacktest/socket.go`
- Create: `backend/internal/imchannel/slack/socket_test.go`
- Modify: `backend/internal/imchannel/slack/slacktest/server.go`（Socket Mode 的字段与 `/link` 路由）
- Modify: `backend/internal/imchannel/slack/app.go`（`permanent` 认 `errLinkDisabled`）

**Interfaces:**
- Consumes（Task 1、2）：`parseValue`、`userIDRe`、`dmRe`、`convRe`、`actionApprove` / `actionReject` / `actionOpen`；`(*App).request`、`authTest`、`sendText`、`postEphemeral`、`scrub`、`self`、`permanent`、`errBadBase`、`modeWebhook`、`WebhookPath`；`imchannel.Retry` / `Sleep` / `GuardedDial` / `Recent`（已存在）。
- Produces（package `slack`）：
  ```go
  var errNoAppToken, errNoSigningSecret, errReconnect, errLinkDisabled error
  func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error
  func (a *App) onInteractive(ctx context.Context, sink imchannel.ActionSink, raw []byte) (toast, channel, user string)
  func (a *App) tell(ctx context.Context, channel, user, toast string)
  func (a *App) onEvent(ctx context.Context, sink imchannel.ActionSink, raw []byte)
  ```
  Task 4 的 `HandleWebhook` 复用 `onInteractive`、`tell`、`onEvent`。
- Produces（package `slacktest`）：`Push(t, typ, payload)`（推信封并断言 3 秒内被确认）、`Interaction(userID, channel, actionID, value)`、`MessageEvent(userID, text, eventID, extra)`、`Click(t, userID, apNo, act) string`、`WaitEphemeral(t, n) string`、`SendDM(t, userID, text)`、`Disconnect(t, reason)`、`DropConnection()`、`WaitConnected(t)`、`WaitConnects(t, n)`、`ConnectCount()`、`SetEndpoint(endpoint)`。

- [ ] **Step 1: 假 Slack 加上 Socket Mode**

`backend/internal/imchannel/slack/slacktest/server.go`：

import 里加 `"github.com/gorilla/websocket"`。`Server` 结构体的 `endpoint` 字段之后加：

```go

	// Socket Mode(socket.go)
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connects  int
	waiting   map[string]chan struct{}
	nextEnv   int
	nextEvent int
```

`New` 里 `tickets: map[string]bool{},` 之后加 `waiting: map[string]chan struct{}{},`。

`handle` 开头（`method, isAPI := …` 之前）加：

```go
	if r.URL.Path == "/link" {
		s.upgrade(w, r)
		return
	}
```

Create `backend/internal/imchannel/slack/slacktest/socket.go`：

```go
package slacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// upgrade 扮演 Socket Mode 的 WebSocket 入口:只认 apps.connections.open 发出去、还没用过的 ticket;
// 连上先发一个 hello,与 Slack 一样。
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	s.mu.Lock()
	ok := s.tickets[ticket]
	delete(s.tickets, ticket)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "num_connections": 1,
		"connection_info": map[string]any{"app_id": "A0TEST001"},
		"debug_info":      map[string]any{"host": "applink-test", "approximate_connection_time": 18060},
	})
	_ = s.write(conn, hello)
	s.mu.Lock()
	s.conn = conn
	s.connects++
	s.mu.Unlock()
	go s.readLoop(conn)
}

// readLoop 收适配器回的确认({"envelope_id": …}),交给等它的 Push。WebSocket 的 ping 由 gorilla
// 默认处理器回 pong。
func (s *Server) readLoop(conn *websocket.Conn) {
	defer func() {
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.mu.Unlock()
		conn.Close()
	}()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var ack struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if json.Unmarshal(msg, &ack) != nil {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[ack.EnvelopeID]
		s.mu.Unlock()
		if ch != nil {
			// 不阻塞:适配器若对同一个信封确认了两次,第二次丢掉,别把读循环卡死。
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Server) write(conn *websocket.Conn, b []byte) error {
	if conn == nil {
		return fmt.Errorf("slacktest: no socket mode connection")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, b)
}

// WaitConnected 等适配器连上来。
func (s *Server) WaitConnected(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		up := s.conn != nil
		s.mu.Unlock()
		if up {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("slacktest: the socket mode connection never came up")
}

// WaitConnects 等到总共建立过至少 n 条连接,且当前有一条连着。
func (s *Server) WaitConnects(t testing.TB, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := s.connects >= n && s.conn != nil
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: %d connections within 5s, want %d", s.ConnectCount(), n)
}

// ConnectCount 是到目前为止建立过的连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经 Socket Mode 推一个信封,等适配器确认。Slack 要求 3 秒内确认,超时就当测试失败。
func (s *Server) Push(t testing.TB, typ string, payload any) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextEnv++
	id := fmt.Sprintf("env-%d", s.nextEnv)
	ch := make(chan struct{}, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	b, _ := json.Marshal(map[string]any{
		"envelope_id": id, "type": typ, "payload": payload,
		"accepts_response_payload": false, "retry_attempt": 0, "retry_reason": "",
	})
	if err := s.write(conn, b); err != nil {
		t.Fatalf("slacktest: push: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("slacktest: envelope %s (%s) was not acknowledged within 3s", id, typ)
	}
}

// Interaction 造一次 block_actions 载荷(按钮点击)。channel 是卡片所在的会话。
func Interaction(userID, channel, actionID, value string) map[string]any {
	return map[string]any{
		"type": "block_actions", "api_app_id": "A0TEST001", "trigger_id": "1.2.3",
		"team":         map[string]any{"id": "T0TEST001", "domain": "aegis-test"},
		"user":         map[string]any{"id": userID, "team_id": "T0TEST001", "username": "tester"},
		"container":    map[string]any{"type": "message", "message_ts": "1700000000.000001", "channel_id": channel, "is_ephemeral": false},
		"channel":      map[string]any{"id": channel, "name": "directmessage"},
		"response_url": "https://hooks.slack.com/actions/T0TEST001/1/abc",
		"actions": []any{map[string]any{
			"type": "button", "action_id": actionID, "block_id": "aegis_actions", "value": value, "action_ts": "1700000000.100000",
		}},
	}
}

// Click 推一次「通过」(act = a)或「驳回」(r),返回只回给点击者的那条消息。
func (s *Server) Click(t testing.TB, userID, apNo, act string) string {
	t.Helper()
	action := "aegis_approve"
	if act == "r" {
		action = "aegis_reject"
	}
	before := len(s.Calls("chat.postEphemeral"))
	s.Push(t, "interactive", Interaction(userID, DMChannel(userID), action, "ap:"+apNo+":"+act))
	return s.WaitEphemeral(t, before)
}

// WaitEphemeral 等到第 n+1 条「仅你可见」的消息出现(n 从 0 起),返回它的正文。
func (s *Server) WaitEphemeral(t testing.TB, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls := s.Calls("chat.postEphemeral"); len(calls) > n {
			return calls[n].Get("text")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slacktest: ephemeral message #%d never came", n+1)
	return ""
}

// MessageEvent 造一条 message.im 事件(events_api 的载荷,也是 webhook 推来的 JSON)。extra 覆盖
// event 里的字段(值为 nil 时删掉该字段):subtype、bot_id、channel_type …
func MessageEvent(userID, text, eventID string, extra map[string]any) map[string]any {
	ev := map[string]any{
		"type": "message", "channel": DMChannel(userID), "channel_type": "im", "user": userID,
		"text": text, "ts": "1700000002.000001", "client_msg_id": "c-" + eventID, "team": "T0TEST001",
	}
	for k, v := range extra {
		if v == nil {
			delete(ev, k)
		} else {
			ev[k] = v
		}
	}
	return map[string]any{
		"type": "event_callback", "token": "legacy-verification-token", "team_id": "T0TEST001", "api_app_id": "A0TEST001",
		"event": ev, "event_id": eventID, "event_time": 1700000002,
	}
}

// SendDM 推一条私聊文本(每次一个新的 event_id)。
func (s *Server) SendDM(t testing.TB, userID, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextEvent++
	id := fmt.Sprintf("Ev%06d", s.nextEvent)
	s.mu.Unlock()
	s.Push(t, "events_api", MessageEvent(userID, text, id, nil))
}

// Disconnect 发一个 disconnect 信封,不等确认:refresh_requested / warning 是 Slack 定期刷新连接,
// link_disabled 是应用后台关掉了 Socket Mode。
func (s *Server) Disconnect(t testing.TB, reason string) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.conn = nil // 之后的 Push 等新连接
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "disconnect", "reason": reason, "debug_info": map[string]any{"host": "applink-test"}})
	if err := s.write(conn, b); err != nil {
		t.Fatalf("slacktest: disconnect: %v", err)
	}
}

// DropConnection 直接断开当前连接(网络抖动)。
func (s *Server) DropConnection() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// SetEndpoint 让之后的 apps.connections.open 下发这个地址(扮演一个被劫持或指向内网的下发)。空串恢复默认。
func (s *Server) SetEndpoint(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoint = endpoint
}
```

- [ ] **Step 2: 写失败的测试**

Create `backend/internal/imchannel/slack/socket_test.go`：

```go
package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
)

// recSink 记下交上来的点击与私聊,回一个能看出收到了什么的提示 / 回复。
type recSink struct {
	mu      sync.Mutex
	actions []imchannel.Action
	dms     []imchannel.DM
}

func (s *recSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	s.mu.Lock()
	s.actions = append(s.actions, a)
	s.mu.Unlock()
	return imchannel.ActionResult{Toast: "ok " + a.PlatformUserID + " " + a.ApNo + " " + strconv.FormatBool(a.Approve)}
}

func (s *recSink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	s.mu.Lock()
	s.dms = append(s.dms, dm)
	s.mu.Unlock()
	return "reply to " + dm.Text
}

func (s *recSink) dmList() []imchannel.DM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imchannel.DM(nil), s.dms...)
}

func (s *recSink) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.actions), len(s.dms)
}

// startRun 在后台跑 Run;返回的 stop 取消它并拿到返回值。
func startRun(t *testing.T, a *App, sink imchannel.ActionSink) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				err = errors.New("Run did not return after cancel")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// runUntilError 跑 Run,期望它自己很快返回一个错误(永久性错误 / 配置错误)。
func runUntilError(t *testing.T, a *App) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going; a permanent failure must stop it with a reason")
		return nil
	}
}

// settle 给已确认、仍在协程里处理的信封一点时间:断言「什么都没发生」之前用。
func settle() { time.Sleep(300 * time.Millisecond) }

func TestRun_ChecksTheBotTokenThenConnectsWithTheAppToken(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	var _ imchannel.BotNamer = a
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	auth, open := srv.Calls("auth.test"), srv.Calls("apps.connections.open")
	if len(auth) != 1 || auth[0].Token != srv.BotToken || len(open) != 1 || open[0].Token != srv.AppToken {
		t.Fatalf("auth.test = %+v, apps.connections.open = %+v", auth, open)
	}
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
}

func TestRun_AClickReachesTheSinkAndOnlyTheClickerSeesTheAnswer(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if got := srv.Click(t, "U0OWNER", "AP-2294", "a"); got != "ok U0OWNER AP-2294 true" {
		t.Fatalf("approve → %q", got)
	}
	if got := srv.Click(t, "U0OWNER", "AP-2294", "r"); got != "ok U0OWNER AP-2294 false" {
		t.Fatalf("reject → %q", got)
	}
	if e := srv.Calls("chat.postEphemeral")[0]; e.Get("channel") != "D0OWNER" || e.Get("user") != "U0OWNER" || e.Token != srv.BotToken {
		t.Fatalf("ephemeral = %+v", e)
	}
}

func TestRun_ClicksThatDoNotNameATicketAndAnActionAreDropped(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	bad := []struct{ action, value string }{
		{actionApprove, "AP-2294"},               // 不是网关造的 value
		{actionApprove, "ap:AP 1:a"},             // 单号里有空格
		{actionApprove, "ap:AP-2294:x"},          // 不认识的动作
		{"someone_elses_button", "ap:AP-2294:a"}, // 不是网关的按钮
	}
	for _, c := range bad {
		srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", c.action, c.value))
	}
	for i := range bad {
		if got := srv.WaitEphemeral(t, i); got != "无法识别的操作" {
			t.Errorf("#%d → %q", i, got)
		}
	}
	// 认不出点击者的:无人可回,什么也不发
	srv.Push(t, "interactive", slacktest.Interaction("", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.Push(t, "interactive", slacktest.Interaction("not-a-member", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.Push(t, "interactive", map[string]any{"type": "view_submission", "user": map[string]any{"id": "U0OWNER"}})
	settle()
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d malformed clicks reached the sink", n)
	}
	if n := len(srv.Calls("chat.postEphemeral")); n != len(bad) {
		t.Fatalf("%d ephemeral messages, want %d", n, len(bad))
	}
}

func TestRun_TheConsoleLinkButtonIsAcknowledgedAndIgnored(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	// 链接按钮被点时 Slack 打开链接,同时照样回调一次:不是决策,也不该收到一句「无法识别的操作」。
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionOpen, ""))
	settle()
	if n, _ := sink.counts(); n != 0 || len(srv.Calls("chat.postEphemeral")) != 0 {
		t.Fatalf("a console link click did something: %d actions, %d messages", n, len(srv.Calls("chat.postEphemeral")))
	}
}

func TestRun_ADirectMessageIsPassedOnAndAnswered(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "U0OWNER", "  BIND-7K3QX9MP ")
	post := srv.WaitCall(t, "chat.postMessage", nil)
	if post.Get("channel") != "D0OWNER" || post.Get("text") != "reply to BIND-7K3QX9MP" || post.Get("blocks") != "" {
		t.Fatalf("reply = %+v", post.Params)
	}
	if dms := sink.dmList(); len(dms) != 1 || dms[0].PlatformUserID != "U0OWNER" || dms[0].Text != "BIND-7K3QX9MP" {
		t.Fatalf("dms = %+v", dms)
	}
}

func TestRun_OnlyDirectTextFromPeopleIsRead(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.WaitConnected(t)
	for i, extra := range []map[string]any{
		{"channel_type": "channel", "channel": "C0GENERAL"}, // 频道里的码:就是泄露了,更不能认
		{"channel_type": "mpim", "channel": "G0GROUP"},      // 多人私聊同理
		{"subtype": "message_changed"},                      // 编辑、删除、入群提示 …
		{"bot_id": "B0OTHER"},                               // 机器人发的
		{"user": srv.BotUserID},                             // 网关自己发的
		{"text": "   "},
		{"user": "not-a-member"},
	} {
		srv.Push(t, "events_api", slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", fmt.Sprintf("EvIgnored%d", i), extra))
	}
	srv.Push(t, "events_api", map[string]any{"type": "app_rate_limited", "team_id": "T0TEST001"})
	settle()
	if _, n := sink.counts(); n != 0 {
		t.Fatalf("%d messages reached the sink", n)
	}
	if n := len(srv.Calls("chat.postMessage")); n != 0 {
		t.Fatalf("%d replies were sent", n)
	}
}

func TestRun_ARedeliveredEventIsHandledOnce(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	ev := slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", "EvDup", nil)
	srv.Push(t, "events_api", ev)
	srv.Push(t, "events_api", ev) // 重投的信封照样要确认
	srv.WaitCall(t, "chat.postMessage", nil)
	settle()
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same event_id was handled %d times", n)
	}
}

func TestRun_EveryEnvelopeIsAcknowledged(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	// 网关不处理斜杠命令和没见过的类型,但都得确认 —— 不确认 Slack 会一直重投。Push 自己断言确认。
	srv.Push(t, "slash_commands", map[string]any{"command": "/aegis", "user_id": "U0OWNER"})
	srv.Push(t, "something_new", map[string]any{})
}

func TestRun_ReconnectsWhenAskedAndWhenTheConnectionDrops(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	srv.Disconnect(t, "refresh_requested")
	srv.WaitConnects(t, 2)
	srv.DropConnection()
	srv.WaitConnects(t, 3)
	if n := len(srv.Calls("apps.connections.open")); n != 3 {
		t.Fatalf("each connection needs a fresh ticket; apps.connections.open called %d times", n)
	}
	if n := len(srv.Calls("auth.test")); n != 1 {
		t.Fatalf("reconnecting must not re-check the bot token; auth.test called %d times", n)
	}
	if got := srv.Click(t, "U0OWNER", "AP-2294", "a"); got != "ok U0OWNER AP-2294 true" {
		t.Fatalf("clicks must work after reconnecting, got %q", got)
	}
}

func TestRun_SocketModeTurnedOffInSlackStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	srv.WaitConnected(t)
	srv.Disconnect(t, "link_disabled")
	select {
	case err := <-done:
		if !errors.Is(err, errLinkDisabled) || !permanent(err) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept reconnecting to an app whose Socket Mode was turned off")
	}
	if srv.ConnectCount() != 1 {
		t.Fatalf("%d connections", srv.ConnectCount())
	}
}

func TestRun_StopsWhenCancelledAndStaysStopped(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
	n := srv.ConnectCount()
	time.Sleep(1500 * time.Millisecond)
	if srv.ConnectCount() != n || len(srv.Calls("apps.connections.open")) != n {
		t.Fatal("a cancelled Run must not reconnect: after Reload the old connection would keep taking events")
	}
}

// blockingSink 的 OnAction 停在 release 上,记下当时 ctx 是否已被取消。
type blockingSink struct {
	recSink
	started chan struct{}
	release chan struct{}
	ctxErr  chan error
}

func (s *blockingSink) OnAction(ctx context.Context, _ imchannel.Action) imchannel.ActionResult {
	close(s.started)
	<-s.release
	s.ctxErr <- ctx.Err()
	return imchannel.ActionResult{Toast: "decided"}
}

func TestRun_AClickAcknowledgedBeforeAReloadIsStillDecided(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{}), ctxErr: make(chan error, 1)}
	stop := startRun(t, a, sink)
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the click never reached the sink")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }() // Reload:Run 的 ctx 被取消
	time.Sleep(200 * time.Millisecond)
	close(sink.release)
	if err := <-sink.ctxErr; err != nil {
		t.Fatalf("the envelope was already acknowledged, so Slack will not resend it — a cancelled ctx here loses the decision: %v", err)
	}
	if got := srv.WaitEphemeral(t, 0); got != "decided" {
		t.Fatalf("the clicker must still be told, got %q", got)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// panicSink 处理点击时 panic:那个协程死掉,连接与其他事件不受影响。
type panicSink struct{ recSink }

func (*panicSink) OnAction(context.Context, imchannel.Action) imchannel.ActionResult { panic("boom") }

func TestRun_APanickingHandlerDoesNotTakeTheConnectionDown(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &panicSink{})
	srv.Push(t, "interactive", slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	srv.SendDM(t, "U0OWNER", "BIND-7K3QX9MP")
	srv.WaitCall(t, "chat.postMessage", nil)
	if srv.ConnectCount() != 1 {
		t.Fatalf("%d connections: the panic took the connection down", srv.ConnectCount())
	}
}

func TestRun_ABotTokenInTheAppTokenSlotStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	// 绕过了保存时的前缀校验(比如直接改库):Slack 回 not_allowed_token_type,重连只会被同样拒绝。
	a := newTestApp(t, srv, kv{"im.slack.appToken": srv.BotToken})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "not_allowed_token_type") || strings.Contains(err.Error(), srv.BotToken) {
		t.Fatalf("err = %v", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("no connection with the wrong kind of token")
	}
}

func TestRun_ARejectedBotTokenStopsTheLoopWithoutTheToken(t *testing.T) {
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token"
	a := newTestApp(t, srv, kv{"im.slack.botToken": wrong})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") || !strings.Contains(err.Error(), "<bot-token>") ||
		strings.Contains(err.Error(), wrong) {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("%d socket mode connections attempted with a rejected bot token", n)
	}
}

func TestRun_AWrongAPIAddressStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("auth.test", slacktest.Failure{Raw: "<html>console</html>"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "检查 API 地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_RefusesBeforeAnyRequestWithoutAnAppToken(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, kv{"im.slack.appToken": ""})
	if err := runUntilError(t, a); !errors.Is(err, errNoAppToken) {
		t.Fatalf("err = %v, want errNoAppToken", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := slacktest.New(t)
	d := deps(kv{"im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api"})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "受控的拨号器") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

// Slack 下发的长连接地址被出站校验拒绝(解析到内网)是永久性的;校验拿到的是 http(s) 形式。
func TestRun_AnEndpointRefusedByTheOutboundCheckStopsTheLoop(t *testing.T) {
	for _, c := range []struct{ endpoint, checked string }{
		{"ws://10.0.0.5:9000/link", "http://10.0.0.5:9000"},
		{"wss://10.0.0.5/link", "https://10.0.0.5"},
	} {
		srv := slacktest.New(t)
		a := newTestApp(t, srv, nil)
		srv.SetEndpoint(c.endpoint)
		var mu sync.Mutex
		var checked []string
		a.validate = func(u string) error {
			mu.Lock()
			checked = append(checked, u)
			mu.Unlock()
			if strings.Contains(u, "10.0.0.5") {
				return errors.New("解析到内网地址")
			}
			return nil
		}
		err := runUntilError(t, a)
		if !errors.Is(err, errBadBase) || !strings.Contains(err.Error(), "内网") {
			t.Fatalf("%s: err = %v", c.endpoint, err)
		}
		if srv.ConnectCount() != 0 {
			t.Fatalf("%s: a refused endpoint must not be dialled", c.endpoint)
		}
		mu.Lock()
		found := false
		for _, u := range checked {
			found = found || u == c.checked
		}
		mu.Unlock()
		if !found {
			t.Fatalf("%s: validated %q, want %s", c.endpoint, checked, c.checked)
		}
	}
}

func TestRun_AnOpenResponseWithoutAURLStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("apps.connections.open", slacktest.Failure{Raw: `{"ok":true}`})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "没有返回长连接地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_AnUnusableEndpointStopsTheLoop(t *testing.T) {
	srv := slacktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetEndpoint("https://example.com/link")
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "无法使用") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_WebhookModeNeedsTheSigningSecretAndOpensNoSocket(t *testing.T) {
	srv := slacktest.New(t)
	if err := runUntilError(t, newTestApp(t, srv, kv{"im.slack.mode": "webhook"})); !errors.Is(err, errNoSigningSecret) {
		t.Fatalf("err = %v, want errNoSigningSecret", err)
	}
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
	// webhook 模式不需要 App Token,也不建长连接;Run 仍验 Bot Token、拿机器人名,然后待命。
	a := newTestApp(t, srv, kv{"im.slack.mode": "webhook", "im.slack.signingSecret": srv.SigningSecret, "im.slack.appToken": ""})
	startRun(t, a, &recSink{})
	deadline := time.Now().Add(5 * time.Second)
	for a.BotName() != srv.BotName && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
	settle()
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("webhook mode opened %d socket mode connections", n)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/slack/ -run TestRun -count=1`
Expected: FAIL，编译错误 `a.Run undefined`、`undefined: errLinkDisabled` 等

- [ ] **Step 4: 实现入站处理**

Create `backend/internal/imchannel/slack/inbound.go`：

```go
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
```

- [ ] **Step 5: 实现 Socket Mode**

`backend/internal/imchannel/slack/app.go` 的 `permanent` 第一行改为：

```go
	if errors.Is(err, errBadBase) || errors.Is(err, errLinkDisabled) {
```

注释第一句「判断重试无望的错误:」之后补「应用关掉了 Socket Mode、」。

Create `backend/internal/imchannel/slack/socket.go`：

```go
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
```

- [ ] **Step 6: 跑测试确认通过**

Run: `cd backend && go test ./internal/imchannel/slack/... -count=1 -race && go vet ./internal/imchannel/slack/...`
Expected: PASS（含 `-race`），vet 无输出

- [ ] **Step 7: Commit**

```bash
cd backend
git add internal/imchannel/slack/
git commit -m "feat(im): receive Slack clicks and direct messages over Socket Mode"
```

---
### Task 4: webhook 模式：验签、时间窗、url_verification、分发

**Files:**
- Create: `backend/internal/imchannel/slack/webhook.go`
- Create: `backend/internal/imchannel/slack/webhook_test.go`
- Modify: `backend/internal/imchannel/slack/slacktest/server.go`（加 `InteractionForm`）

**Interfaces:**
- Consumes（Task 2、3）：`(*App).onInteractive`、`tell`、`onEvent`、字段 `mode` / `signingSecret` / `now`、`modeWebhook`；`slacktest.Sign` / `SignedHeaders` / `Interaction` / `MessageEvent`。
- Produces：`func (a *App) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) imchannel.WebhookReply`（实现 `imchannel.Webhooker`）；`slacktest.InteractionForm(payload map[string]any) string`。

- [ ] **Step 1: 假 Slack 加上交互表单的编码**

`backend/internal/imchannel/slack/slacktest/server.go` 末尾加：

```go

// InteractionForm 把交互载荷编码成 Slack 推 webhook 时的表单体:payload=<JSON>。
func InteractionForm(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return url.Values{"payload": {string(b)}}.Encode()
}
```

- [ ] **Step 2: 写失败的测试**

Create `backend/internal/imchannel/slack/webhook_test.go`：

```go
package slack

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
)

const formType = "application/x-www-form-urlencoded"

func header(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

func webhookApp(t *testing.T, srv *slacktest.Server) *App {
	t.Helper()
	return newTestApp(t, srv, kv{"im.slack.mode": "webhook", "im.slack.signingSecret": srv.SigningSecret, "im.slack.appToken": ""})
}

func TestWebhook_IsClosedUnlessInWebhookModeWithASigningSecret(t *testing.T) {
	srv := slacktest.New(t)
	body := `{"type":"url_verification","challenge":"c-123"}`
	for name, a := range map[string]*App{
		"socket mode":       newTestApp(t, srv, kv{"im.slack.signingSecret": srv.SigningSecret}),
		"no signing secret": newTestApp(t, srv, kv{"im.slack.mode": "webhook"}),
	} {
		if r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, "")), []byte(body), &recSink{}); r.Status != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	var _ imchannel.Webhooker = webhookApp(t, srv)
}

func TestWebhook_RefusesBadSignaturesAndStaleRequests(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	now := time.Now()
	a.now = func() time.Time { return now }
	body := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	signed := func(at time.Time, secret, payload string) http.Header {
		return header(map[string]string{
			"Content-Type":              formType,
			"X-Slack-Request-Timestamp": strconv.FormatInt(at.Unix(), 10),
			"X-Slack-Signature":         slacktest.Sign(secret, at.Unix(), payload),
		})
	}
	for name, c := range map[string]struct {
		h    http.Header
		body string
	}{
		"unsigned":             {header(map[string]string{"Content-Type": formType}), body},
		"no timestamp":         {header(map[string]string{"Content-Type": formType, "X-Slack-Signature": slacktest.Sign(srv.SigningSecret, now.Unix(), body)}), body},
		"wrong secret":         {signed(now, "0000000000000000000000000000000x", body), body},
		"tampered body":        {signed(now, srv.SigningSecret, body), body + "x"},
		"six minutes old":      {signed(now.Add(-6*time.Minute), srv.SigningSecret, body), body},
		"six minutes from now": {signed(now.Add(6*time.Minute), srv.SigningSecret, body), body},
	} {
		if r := a.HandleWebhook(bg, c.h, []byte(c.body), sink); r.Status != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d refused requests reached the sink", n)
	}
	// 4 分钟前、签名正确:还在 ±5 分钟的窗口里(网关与 Slack 之间的时钟偏差)
	if r := a.HandleWebhook(bg, signed(now.Add(-4*time.Minute), srv.SigningSecret, body), []byte(body), sink); r.Status != http.StatusOK {
		t.Fatalf("a request inside the window → %d", r.Status)
	}
}

func TestWebhook_AnswersTheURLVerificationChallenge(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	const challenge = "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"
	body := `{"token":"legacy","challenge":"` + challenge + `","type":"url_verification"}`
	r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, "")), []byte(body), &recSink{})
	if r.Status != http.StatusOK || string(r.Body) != `{"challenge":"`+challenge+`"}` {
		t.Fatalf("challenge → %d %s", r.Status, r.Body)
	}
	// 只回显 challenge 形状的串:这条路不该变成「让网关回显任意内容」
	odd := `{"type":"url_verification","challenge":"<script>alert(1)</script>"}`
	if r := a.HandleWebhook(bg, header(srv.SignedHeaders(odd, "")), []byte(odd), &recSink{}); r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("odd challenge → %d %s", r.Status, r.Body)
	}
}

func TestWebhook_AClickIsDecidedBeforeTheReplyAndAnsweredToTheClicker(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	body := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, formType)), []byte(body), sink)
	if r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("click → %d %s", r.Status, r.Body)
	}
	if n, _ := sink.counts(); n != 1 {
		t.Fatalf("the decision must be made before the 200: %d actions", n)
	}
	e := srv.WaitCall(t, "chat.postEphemeral", nil)
	if e.Get("text") != "ok U0OWNER AP-2294 true" || e.Get("user") != "U0OWNER" || e.Get("channel") != "D0OWNER" {
		t.Fatalf("ephemeral = %+v", e.Params)
	}
}

func TestWebhook_ADirectMessageIsAnsweredAndARetryIsHandledOnce(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	ev, _ := json.Marshal(slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", "EvWebhook1", nil))
	for i := 0; i < 2; i++ {
		h := header(srv.SignedHeaders(string(ev), ""))
		if i == 1 {
			h.Set("X-Slack-Retry-Num", "1") // Slack 没在 3 秒内拿到 200,重投
			h.Set("X-Slack-Retry-Reason", "http_timeout")
		}
		if r := a.HandleWebhook(bg, h, ev, sink); r.Status != http.StatusOK {
			t.Fatalf("delivery %d → %d", i+1, r.Status)
		}
	}
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same event_id was handled %d times", n)
	}
	if posts := srv.Calls("chat.postMessage"); len(posts) != 1 || posts[0].Get("channel") != "D0OWNER" {
		t.Fatalf("replies = %+v", posts)
	}
}

func TestWebhook_ASignedButUnusableRequestIsStillAcknowledged(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	// 验签过了就一律 200:非 2xx 会让 Slack 一直重投,而重投不会让一次坏的处理变好。
	for _, c := range []struct{ body, ct string }{
		{"not json", ""},
		{"payload=%7Bbroken", formType},
		{"command=%2Faegis&user_id=U0OWNER", formType}, // 斜杠命令:网关不提供
		{`{"type":"event_callback","event":{"type":"app_mention"}}`, ""},
	} {
		if r := a.HandleWebhook(bg, header(srv.SignedHeaders(c.body, c.ct)), []byte(c.body), sink); r.Status != http.StatusOK {
			t.Errorf("%q → %d", c.body, r.Status)
		}
	}
	if n, m := sink.counts(); n+m != 0 {
		t.Fatalf("unusable requests reached the sink: %d actions, %d messages", n, m)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/slack/ -run TestWebhook -count=1`
Expected: FAIL，编译错误 `a.HandleWebhook undefined`

- [ ] **Step 4: 实现**

Create `backend/internal/imchannel/slack/webhook.go`：

```go
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd backend && go test ./internal/imchannel/slack/... -count=1 -race && go vet ./internal/imchannel/slack/...`
Expected: PASS，vet 无输出

- [ ] **Step 6: Commit**

```bash
cd backend
git add internal/imchannel/slack/
git commit -m "feat(im): accept signed Slack webhooks for clicks and direct messages"
```

---
### Task 5: 登记 Slack 为交互渠道、配置校验与端到端

**Files:**
- Modify: `backend/internal/service/service.go`（登记 Slack）
- Modify: `backend/internal/handler/admin.go`（`im.slack.*` 保存时校验）
- Modify: `backend/internal/bootstrap/im_helpers_test.go`（通用的 `channelStatus`）
- Modify: `backend/internal/bootstrap/im_dingtalk_test.go`（`dingtalkStatus` 改用它）
- Modify: `backend/internal/bootstrap/im_settings_test.go`（状态里有 Slack）
- Create: `backend/internal/bootstrap/im_slack_test.go`

**Interfaces:**
- Consumes（Task 2–4）：`slack.New`、`slack.WebhookPath`；`slacktest.New` / `Server` 的 `BotToken` `AppToken` `SigningSecret` / `WaitConnected` / `WaitCall` / `Calls` / `SendDM` / `Click` / `WaitEphemeral` / `SignedHeaders` / `InteractionForm` / `Interaction` / `DMChannel`。bootstrap 已有的 `newTestApp`、`login`、`do`、`setSettings`、`submitProdHighRisk`、`approvalRow`、`flushIM`、`bindIM`、`postIMWebhook`、`postIMWebhookFull`、`eq`、`repo.AuditByOperatorLike`。
- Produces：`s.IM.Register(imchannel.Slack, imchannel.Caps{Approve: true}, slack.New)`；`func (a *testApp) channelStatus(token, kind string, match func(running bool, lastError string) bool) (bool, string)`。

- [ ] **Step 1: 写失败的端到端测试**

Create `backend/internal/bootstrap/im_slack_test.go`：

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/slack/slacktest"
)

// slackIM 起一个假 Slack,把网关的 Slack 渠道指过去、打开交互审批。Socket Mode 等到连上;webhook 模式
// 带上假 Slack 的 Signing Secret,等到 Run 验过 Bot Token。
func (a *testApp) slackIM(token string, webhook bool) *slacktest.Server {
	a.t.Helper()
	srv := slacktest.New(a.t)
	kv := map[string]any{
		"im.slack.approval": true, "im.slack.botToken": srv.BotToken, "im.slack.appToken": srv.AppToken,
		"im.slack.apiBase": srv.URL + "/api",
	}
	if webhook {
		kv["im.slack.mode"], kv["im.slack.signingSecret"] = "webhook", srv.SigningSecret
	}
	a.setSettings(token, kv)
	if webhook {
		srv.WaitCall(a.t, "auth.test", nil)
	} else {
		srv.WaitConnected(a.t)
	}
	return srv
}

func TestSlack_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.slackIM(admin, false)

	// 1. 自助绑定:控制台生成码(弹窗里告诉他私聊哪个机器人),用自己的 Slack 私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/slack/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotName, "the bind dialog names the bot")
	srv.SendDM(t, "U0OWNER", issued.Code)
	srv.WaitCall(t, "chat.postMessage", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("text"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "chat.postMessage", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("blocks"), ap.ApNo)
	})
	if !strings.Contains(card.Get("blocks"), "DROP TABLE orders") || !strings.Contains(card.Get("blocks"), "aegis_actions") {
		t.Fatalf("card = %s", card.Get("blocks"))
	}
	open := srv.Calls("conversations.open")
	if len(open) == 0 || open[0].Get("users") != "U0OWNER" {
		t.Fatalf("conversations.open = %+v", open)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 只有他看得见的结果 → 卡片收起
	eq(t, srv.Click(t, "U0OWNER", ap.ApNo, "a"), "已批准,请发起人回控制台执行", "ephemeral")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Slack")
	rows, _ := app.repo.AuditByOperatorLike("%(Slack)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "chat.update", func(c slacktest.Call) bool {
		return c.Get("channel") == "D0OWNER" && strings.Contains(c.Get("blocks"), "通过") &&
			!strings.Contains(c.Get("blocks"), "aegis_actions") && strings.Contains(c.Get("blocks"), "DROP TABLE orders")
	})
}

func TestSlack_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.slackIM(admin, false)
	ap := app.submitProdHighRisk(admin)

	if got := srv.Click(t, "U0STRANGER", ap.ApNo, "a"); !strings.Contains(got, "未绑定") {
		t.Fatalf("ephemeral = %q", got)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "slack:U0STRANGER" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestSlack_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.slackIM(admin, true)

	// Slack 后台保存 Request URL 时的校验
	verify := `{"token":"legacy","challenge":"c-123","type":"url_verification"}`
	code, body := app.postIMWebhookFull("slack", srv.SignedHeaders(verify, ""), verify)
	if code != http.StatusOK || body != `{"challenge":"c-123"}` {
		t.Fatalf("challenge → %d %s", code, body)
	}

	app.bindIM("zhangwei@vela.io", "slack", "U0OWNER")
	ap := app.submitProdHighRisk(admin)
	click := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", "aegis_approve", "ap:"+ap.ApNo+":a"))

	eq(t, app.postIMWebhook("slack", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, click), http.StatusForbidden, "unsigned")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	eq(t, app.postIMWebhook("slack", srv.SignedHeaders(click, "application/x-www-form-urlencoded"), click), http.StatusOK, "signed click")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	srv.WaitCall(t, "chat.postEphemeral", func(c slacktest.Call) bool { return c.Get("user") == "U0OWNER" })
	if n := len(srv.Calls("apps.connections.open")); n != 0 {
		t.Fatalf("webhook mode opened %d socket mode connections", n)
	}
}

func TestSlack_ARejectedBotTokenShowsInStatusWithoutTheToken(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := slacktest.New(t)
	const wrong = "xoxb-the-wrong-bot-token-0000"
	app.setSettings(admin, map[string]any{
		"im.slack.approval": true, "im.slack.botToken": wrong, "im.slack.appToken": srv.AppToken, "im.slack.apiBase": srv.URL + "/api",
	})
	_, lastErr := app.channelStatus(admin, "slack", func(running bool, e string) bool {
		return !running && strings.Contains(e, "invalid_auth")
	})
	// 假 Slack 把收到的 token 回显进了错误:LastError 里只能剩 scrub 的占位符
	if !strings.Contains(lastErr, "<bot-token>") || strings.Contains(lastErr, wrong) {
		t.Fatalf("lastError = %q", lastErr)
	}
	if r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil); strings.Contains(string(r.Data), wrong) {
		t.Fatalf("status leaked the bot token: %s", r.Data)
	}
}

func TestSlack_WebhookModeWithoutASigningSecretShowsInStatus(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := slacktest.New(t)
	// webhook 模式缺 Signing Secret:纯配置错,在任何请求之前就报
	app.setSettings(admin, map[string]any{
		"im.slack.approval": true, "im.slack.botToken": srv.BotToken, "im.slack.mode": "webhook", "im.slack.apiBase": srv.URL + "/api",
	})
	app.channelStatus(admin, "slack", func(running bool, e string) bool { return !running && strings.Contains(e, "Signing Secret") })
	if n := len(srv.Calls("auth.test")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestSlack_SecretsAreStoredEncrypted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	secrets := map[string]string{
		"im.slack.botToken":      "xoxb-1234567890-secret-bot-token",
		"im.slack.appToken":      "xapp-1-A0TEST-secret-app-token",
		"im.slack.signingSecret": "0123456789abcdef0123456789abcdef",
	}
	kv := map[string]any{}
	for k, v := range secrets {
		kv[k] = v
	}
	app.setSettings(admin, kv)
	get := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	for k, v := range secrets {
		if raw := app.repo.SettingString(k, ""); raw == "" || strings.Contains(raw, v) {
			t.Errorf("%s must be stored encrypted, stored %q", k, raw)
		}
		if strings.Contains(string(get.Data), v) {
			t.Errorf("GET /settings leaked %s", k)
		}
	}
}

func TestIMSettings_SlackSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.slack.apiBase":       "ftp://slack.com/api",
		"im.slack.mode":          "rtm",
		"im.slack.botToken":      "xapp-1-A0TEST-this-is-the-app-token", // 两种 token 填反
		"im.slack.appToken":      "xoxb-1234567890-this-is-the-bot-token",
		"im.slack.signingSecret": "too-short",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be refused at save time", k, bad)
		}
	}
	// 用户 token 也不行:拿它发的卡片会以那个人的名义出现
	if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"im.slack.botToken": "xoxp-1234567890-a-user-token"}).Code == 0 {
		t.Error("a user token must be refused as the bot token")
	}
	app.setSettings(admin, map[string]any{
		"im.slack.apiBase": "https://slack.com/api", "im.slack.mode": "webhook",
		"im.slack.botToken": "xoxb-1234567890-0987654321-AbCdEfGhIjKl", "im.slack.appToken": "xapp-1-A0TEST001-1234567890-abcdef",
		"im.slack.signingSecret": "8f742231b10e8888abcd99edf5d7d85a",
	})
}
```

`backend/internal/bootstrap/im_settings_test.go` 的 `TestIMSettings_StatusListsChannels`：`var sawLark, sawDingTalk, sawFake bool` 改为 `var sawLark, sawDingTalk, sawSlack, sawFake bool`；`case "dingtalk": …` 之后加：

```go
		case "slack":
			sawSlack = true
			if !row.CanApprove {
				t.Error("slack does interactive approval since phase 5")
			}
```

`if !sawLark || !sawDingTalk || !sawFake {` 改为 `if !sawLark || !sawDingTalk || !sawSlack || !sawFake {`。

把钉钉测试里的状态轮询挪成通用的。`backend/internal/bootstrap/im_helpers_test.go` 的 import 改为：

```go
import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
	"velagateway/internal/model"
)
```

文件末尾加：

```go

// channelStatus 轮询设置页状态,直到 kind 那一行满足 match。
func (a *testApp) channelStatus(token, kind string, match func(running bool, lastError string) bool) (bool, string) {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := a.do(http.MethodGet, "/api/v1/settings/im/status", token, nil)
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == kind && match(row.Running, row.LastError) {
				return row.Running, row.LastError
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatalf("the %s status never reached the expected state", kind)
	return false, ""
}
```

`backend/internal/bootstrap/im_dingtalk_test.go` 的 `dingtalkStatus` 整个函数换成：

```go
// dingtalkStatus 轮询设置页状态,直到钉钉那一行满足 match。
func (a *testApp) dingtalkStatus(token string, match func(running bool, lastError string) bool) (bool, string) {
	a.t.Helper()
	return a.channelStatus(token, "dingtalk", match)
}
```

并从它的 import 里删掉不再用到的 `"time"`（`encoding/json`、`net/http` 其他测试仍在用；以 `go vet` 为准）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestSlack|TestIMSettings_Slack|TestIMSettings_StatusListsChannels|TestDingTalk' -count=1`
Expected: FAIL —— 状态里没有 `slack`（渠道没登记）；保存时 `im.slack.mode = rtm` 等没被拒；钉钉测试照常通过

- [ ] **Step 3: 登记渠道**

`backend/internal/service/service.go` 的 import 里 `"velagateway/internal/imchannel/lark"` 之后加 `"velagateway/internal/imchannel/slack"`。登记钉钉那两行之后加：

```go
	// Slack:第 5 期起能做交互审批(Socket Mode / webhook);群通知在第 6 期。
	s.IM.Register(imchannel.Slack, imchannel.Caps{Approve: true}, slack.New)
```

- [ ] **Step 4: 保存时校验**

`backend/internal/handler/admin.go`，`dingtalkRobotCodeRe` 之后加：

```go

// slackBotTokenRe / slackAppTokenRe:Slack 后台同时给出 xoxb-(Bot)、xapp-(App-Level)、xoxp-(用户)三种
// token,填反是最常见的配置错 —— Slack 只回一句 not_allowed_token_type。保存时按前缀拦下。
var (
	slackBotTokenRe = regexp.MustCompile(`^xoxb-[A-Za-z0-9-]{10,250}$`)
	slackAppTokenRe = regexp.MustCompile(`^xapp-[A-Za-z0-9-]{10,250}$`)
	// slackSigningSecretRe 约束 Slack 应用的 Signing Secret(Basic Information › App Credentials 里那串)。
	slackSigningSecretRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
)
```

`SaveSettings` 的校验 `switch k` 里：

`case "im.telegram.apiBase", "im.lark.apiBase", "im.dingtalk.apiBase":` 改为 `case "im.telegram.apiBase", "im.lark.apiBase", "im.dingtalk.apiBase", "im.slack.apiBase":`。

`case "im.dingtalk.robotCode": …` 那一段之后加：

```go
			case "im.slack.mode":
				if sv != "socket" && sv != "webhook" {
					verr = fmt.Errorf("只能是 socket 或 webhook")
				}
			case "im.slack.botToken":
				// 到这里的一定是真要换的新明文(空值已在上面按「保持原值」跳过),校验在加密之前。
				if !slackBotTokenRe.MatchString(sv) {
					verr = fmt.Errorf("Bot User OAuth Token 应以 xoxb- 开头(应用后台 › OAuth & Permissions)")
				}
			case "im.slack.appToken":
				if !slackAppTokenRe.MatchString(sv) {
					verr = fmt.Errorf("App-Level Token 应以 xapp- 开头(应用后台 › Basic Information › App-Level Tokens)")
				}
			case "im.slack.signingSecret":
				if !slackSigningSecretRe.MatchString(sv) {
					verr = fmt.Errorf("Signing Secret 应为 16-128 位字母或数字(应用后台 › Basic Information › App Credentials)")
				}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd backend && go vet ./... && go test ./internal/imchannel/... ./internal/handler/... ./internal/service/... -count=1`
Expected: vet 无输出；PASS

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestSlack|TestIMSettings|TestDingTalk|TestLark|TestIMWebhook' -count=1`
Expected: PASS

Run（前台，约 5 分钟）: `cd backend && go test ./... -count=1 2>&1 | grep -E '^(FAIL|ok|---)' | grep -v '^ok'`
Expected: 只有 Global Constraints 里那两处已知基线失败（`internal/osc` 整包、`TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes`）

- [ ] **Step 6: Commit**

```bash
cd backend
git add internal/service/service.go internal/handler/admin.go internal/bootstrap/im_slack_test.go internal/bootstrap/im_helpers_test.go internal/bootstrap/im_dingtalk_test.go internal/bootstrap/im_settings_test.go
git commit -m "feat(im): register Slack as an interactive approval channel"
```

---
### Task 6: 设置页配置 Slack 交互审批

**Files:**
- Modify: `frontend/src/components/settings/ImChannelsPanel.tsx`
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`

**Interfaces:**
- Consumes: 后端配置键（Global Constraints）、状态接口里 Slack 的 `canApprove: true`（Task 5）。
- Produces: `IM_CHANNELS.slack.approval`；`SlackHint` 组件；13 个文案键（zh / en 都要有，`Dict` 类型要求两边一致）。

- [ ] **Step 1: 加字段表**

`frontend/src/components/settings/ImChannelsPanel.tsx`，`IM_CHANNELS` 上方注释的最后两行改为：

```ts
 * 这里加一项,不必再去动设置页。飞书群通知（第 1 期）与交互审批（第 3 期）、Telegram 交互审批（第 2 期）、
 * 钉钉交互审批（第 4 期）、Slack 交互审批（第 5 期）。
```

`IM_CHANNELS` 里 `dingtalk: { … },` 之后加：

```ts
  slack: {
    approval: [
      { key: 'im.slack.botToken', label: 'imSlBotToken', placeholderKey: 'imSlBotTokenPh', secret: true },
      {
        key: 'im.slack.mode', label: 'imSlMode', defaultValue: 'socket',
        options: [{ value: 'socket', labelKey: 'imSlModeSocket' }, { value: 'webhook', labelKey: 'imSlModeWebhook' }],
      },
      { key: 'im.slack.appToken', label: 'imSlAppToken', placeholderKey: 'imSlAppTokenPh', secret: true },
      { key: 'im.slack.signingSecret', label: 'imSlSigningSecret', placeholderKey: 'imSlSigningSecretPh', secret: true },
      { key: 'im.slack.apiBase', label: 'imSlApiBase', placeholder: 'https://slack.com/api' },
    ],
  },
```

- [ ] **Step 2: 加提示**

同一文件，交互审批那块的 `extra={…}` 改为：

```tsx
                extra={
                  s.kind === 'telegram' ? <TelegramHint values={values} />
                    : s.kind === 'lark' ? <LarkHint values={values} />
                      : s.kind === 'dingtalk' ? <DingTalkHint />
                        : s.kind === 'slack' ? <SlackHint values={values} />
                          : undefined
                }
```

在 `DingTalkHint` 函数之后加：

```tsx
/**
 * Slack 两种接收方式各要管理员在应用后台做一件事:Socket Mode 要打开 Socket Mode 并建 App-Level Token,
 * webhook 要把这个地址同时填进「Interactivity & Shortcuts」与「Event Subscriptions」的 Request URL ——
 * Slack 不像 Telegram 那样由网关自己登记。
 */
function SlackHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.slack.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imSlSocketNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imSlWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imSlWebhookURL')}<code>{`${base}/api/v1/im/slack/webhook`}</code></div>
}
```

- [ ] **Step 3: 加文案**

`frontend/src/locales/zh.ts`，紧跟 `imDtStreamNote` 那一行之后加：

```ts
  imSlBotToken: 'Bot User OAuth Token',
  imSlBotTokenPh: '应用后台 › OAuth & Permissions,以 xoxb- 开头',
  imSlMode: '接收方式',
  imSlModeSocket: 'Socket Mode(无需公网入口)',
  imSlModeWebhook: 'Webhook(需要公网 HTTPS)',
  imSlAppToken: 'App-Level Token',
  imSlAppTokenPh: 'Basic Information › App-Level Tokens,以 xapp- 开头;Socket Mode 用',
  imSlSigningSecret: 'Signing Secret',
  imSlSigningSecretPh: 'Basic Information › App Credentials;webhook 模式用',
  imSlApiBase: 'Web API 地址',
  imSlSocketNote: 'Socket Mode:在应用后台打开 Socket Mode 与 Interactivity,订阅 bot 事件 message.im,并在 App Home 允许用户从 Messages 标签页发消息。Bot Token 需要 chat:write、im:write、im:history 权限。多实例部署也安全。',
  imSlWebhookURL: '在应用后台「Interactivity & Shortcuts」与「Event Subscriptions」的 Request URL 里都填这个地址:',
  imSlWebhookNeedsBase: 'webhook 模式需要先填写上面的「回调根地址」。',
```

`frontend/src/locales/en.ts`，紧跟 `imDtStreamNote` 那一行之后加：

```ts
  imSlBotToken: 'Bot User OAuth Token',
  imSlBotTokenPh: 'App settings › OAuth & Permissions; starts with xoxb-',
  imSlMode: 'Receive events via',
  imSlModeSocket: 'Socket Mode (no public endpoint)',
  imSlModeWebhook: 'Webhook (public HTTPS)',
  imSlAppToken: 'App-Level Token',
  imSlAppTokenPh: 'Basic Information › App-Level Tokens; starts with xapp-; for Socket Mode',
  imSlSigningSecret: 'Signing Secret',
  imSlSigningSecretPh: 'Basic Information › App Credentials; for webhook mode',
  imSlApiBase: 'Web API URL',
  imSlSocketNote: 'Socket Mode: turn on Socket Mode and Interactivity in the app settings, subscribe to the bot event message.im, and in App Home let users send messages from the Messages tab. The bot token needs chat:write, im:write and im:history. Safe with several gateway instances.',
  imSlWebhookURL: 'Enter this URL as the Request URL under both "Interactivity & Shortcuts" and "Event Subscriptions": ',
  imSlWebhookNeedsBase: 'Webhook mode needs the callback base URL above.',
```

- [ ] **Step 4: 验证**

Run: `cd frontend && npm run build && npm run lint && npm run test:unit`
Expected: build 退出码 0、lint 无输出、单元测试全部通过

- [ ] **Step 5: Commit**

```bash
cd frontend
git add src/components/settings/ImChannelsPanel.tsx src/locales/zh.ts src/locales/en.ts
git commit -m "feat(ui): configure Slack interactive approval"
```

---
### Task 7: 文档

**Files:**
- Modify: `docs/external-approval-setup.md`
- Modify: `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（新增 §22）
- Modify: `docs/features.md`
- Modify: `docs/adr/0020-im-channel-approval.md`（状态行）

文档必须描述实际建成的东西：遇到下面文字与代码不一致的地方，以 `backend/internal/imchannel/slack/` 的代码为准，并在报告里说明。

- [ ] **Step 1: 启用手册**

`docs/external-approval-setup.md`：

「当前支持」表格里 Slack 那一行改为：

```markdown
| Slack | 规划中(第 6 期) | ✅ Slack 应用(Socket Mode / webhook) |
```

在「## Telegram 交互审批」整节之前插入：

```markdown
## Slack 交互审批

1. 在 api.slack.com/apps 用 **From scratch** 建一个应用,选要装进的工作区。
2. **OAuth & Permissions** › Bot Token Scopes 加 `chat:write`(发卡片、改卡片、回消息)、`im:write`(打开与成员的私聊)、
   `im:history`(收私聊里的绑定码;收卡时读回卡片原文)。**Install to Workspace**,复制 **Bot User OAuth Token**(`xoxb-` 开头)。
3. **App Home** › 打开「Messages Tab」,勾选「Allow users to send Slash commands and messages from the messages tab」——
   不勾的话成员没法私聊机器人,也就没法绑定。
4. **Interactivity & Shortcuts** 打开 Interactivity;**Event Subscriptions** 打开,在「Subscribe to bot events」里加 `message.im`。
5. 选接收方式:
   - **Socket Mode(默认)**:**Socket Mode** 页打开 Socket Mode;**Basic Information** › App-Level Tokens 建一个带
     `connections:write` 的 token(`xapp-` 开头)。网关主动连 Slack,不需要公网入口,多实例部署也安全 —— Slack 把每个事件只投给其中一个连接。
     打开 Socket Mode 后,上面两处的 Request URL 不用填。
   - **Webhook**:不开 Socket Mode;**Basic Information** › App Credentials 复制 **Signing Secret**。在网关「回调根地址」填一个
     Slack 能访问到的公网 HTTPS 地址,把 `<回调根地址>/api/v1/im/slack/webhook` 同时填进「Interactivity & Shortcuts」与
     「Event Subscriptions」的 Request URL(保存 Event Subscriptions 时 Slack 会发一次校验,网关自动应答)。网关只认签名正确、
     时间戳在 ±5 分钟内的请求。
6. 网关「设置 › IM 渠道 › Slack」打开「交互审批」,填 Bot User OAuth Token,选接收方式,再填 App-Level Token(Socket Mode)或
   Signing Secret(webhook)。两种 token 填反、填成用户 token(`xoxp-`)会在保存时被拒。保存。
7. 状态显示「运行中」且没有错误提示即已连通。token 错误、token 类型不对、缺权限、应用后台关掉了 Socket Mode、Web API 地址填错时,
   状态会显示错误原因。「Web API 地址」一般留空(`https://slack.com/api`);它与其他出站地址走同一套校验。
8. 卡片里的「通过 / 驳回」只做决定,不执行。点击结果以一条**只有点击者看得见**的消息回复(Slack 的按钮没有弹出提示)。工单一结束
   (无论在哪里决定),所有人手上的卡片按钮都会换成一行结果,单号、命令、原因原样保留。只理成员发给机器人的私聊文本:频道里的消息、
   机器人发的消息一律不理。
```

「## 交互审批的账号绑定」第 2 步里，「钉钉里搜索应用的机器人名即可打开单聊…」那一行之后加一行：

```markdown
   Slack 里在侧栏「Apps」下找到这个应用(或搜索绑定弹窗里显示的机器人名),在它的 Messages 标签页发码。
```

管理员代绑那一段之后加一句：

```markdown
代绑 Slack 账号需要对方的**成员 ID**(`U` 开头;对方在自己的 Slack 资料页 › ⋮ › Copy member ID 可以拿到),不是显示名。
```

- [ ] **Step 2: spec 新增 §22**

`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md` 末尾追加：

```markdown
## 22. 第 5 期（Slack 交互审批）实现记录

- **不用 `slack-go/slack`，自己实现 Socket Mode**：v0.30.1 的 `socketmode` 能注入拨号器与 HTTP 客户端，硬伤不在 SSRF，
  而在错误分类 —— 它只把 `invalid_auth` / `account_inactive` / `not_authed` / `token_revoked` 当致命，
  `not_allowed_token_type`（两种 token 填反）、`missing_scope`、API 地址填错都以最长 5 分钟的退避无限重连，设置页看不到原因。
  协议由适配器自己实现（`apps.connections.open` 换地址 → 拨 `wss` → `hello` 忽略、`disconnect` 换连接、信封回
  `{"envelope_id"}` 确认），拨号走受控拨号器，Slack 下发的地址也过出站校验。没有新依赖。
- **先确认、再处理**：Slack 要 3 秒内确认信封。处理用脱离取消的 ctx（`Reload` 不丢一次已确认的点击）；点击幂等、私聊按
  `event_id` 去重（最近 512 条）。`interactive` 与 `events_api` 之外的信封只确认。
- **webhook 模式**：Interactivity 与 Event Subscriptions 共用 `/api/v1/im/slack/webhook`，按 `Content-Type` 分辨。
  `v0=HMAC-SHA256(signingSecret, "v0:<ts>:<body>")` 常量时间比对、时间戳 ±5 分钟，任何一步不过都是同一个 403；之后一律 200。
  `url_verification` 回 challenge（限 `[A-Za-z0-9_-]{1,128}`）。判定同步做完再回 200，回点击者的消息放到协程里发。
- **卡片全是不解析的文本**：外部文本用 `plain_text`、命令用 `rich_text_preformatted`，发起人写的链接、`<!channel>`、SQL
  里的 `<` `&` ``` 原样显示；只有顶层 `text`（通知预览）与机器人发的纯文本按 mrkdwn 转义 `& < >`。
- **按钮**：`action_id` `aegis_approve` / `aegis_reject` / `aegis_open`（链接按钮，被点时 Slack 照样回调，网关只确认），
  `value` = `ap:<ApNo>:a|r`，那组按钮的 `block_id` = `aegis_actions`。认人只凭 `user.id`。
- **点击结果是「仅你可见」的消息**（`chat.postEphemeral`）：Slack 的按钮没有 toast。
- **收卡保留原文**：msgRef = `<D 频道>|<ts>|<ApNo>`。`conversations.history`（`latest=<ts>&inclusive=true&limit=1`）取回原
  blocks，把 `aegis_actions` 换成一行终态；取不到或 Slack 不收（`invalid_blocks`）时退回「单号 + 终态」；`message_not_found`
  算收起。
- **机器人名取 `auth.test` 的 `user`**：`Run` 一开始调它，顺带验证 Bot Token、拿到机器人自己的成员 ID（私聊里忽略自己）；
  没有 `bot_id`（用户 token）按永久性错误报。
- **永久性错误停循环**：`ok:false` 的错误码（`ratelimited` / `internal_error` / `fatal_error` / `service_unavailable` /
  `request_timeout` 除外）、API 地址填错（3xx / 4xx、解析不了或没有 `ok` 的 2xx、被出站校验拒绝）、Slack 下发的地址不能用、
  `disconnect` 的 `link_disabled`。429、5xx、网络错误与 DNS 解析失败退避重试。缺 App Token（Socket Mode）、缺 Signing Secret
  （webhook）、拿不到受控拨号器在任何网络请求之前报。三个密钥在所有错误里换成 `<bot-token>` / `<app-token>` / `<signing-secret>`。
- 新配置键：`im.slack.botToken`🔒、`appToken`🔒、`mode`（`socket` | `webhook`）、`signingSecret`🔒、`apiBase`（缺省
  `https://slack.com/api`，走出站 URL 校验）。保存时按前缀校验两种 token（`xoxb-` / `xapp-`），Signing Secret 限 16–128 位字母数字。
- 所需权限：`chat:write`、`im:write`、`im:history`；bot 事件 `message.im`；App Home 的 Messages Tab 允许用户发消息；Interactivity。
  Socket Mode 另需 `connections:write` 的 App-Level Token。
```

- [ ] **Step 3: 功能清单与 ADR**

`docs/features.md` 里「…、钉钉**交互审批**(Stream 长连接)，其余渠道分期接入」改为「…、钉钉**交互审批**(Stream 长连接)、Slack **交互审批**(Socket Mode / webhook)，其余渠道分期接入」。

`docs/adr/0020-im-channel-approval.md` 状态行改为：

```markdown
- 状态:已采纳(第 1 期:框架 + 飞书群通知;第 2 期:Telegram 交互审批;第 3 期:飞书交互审批;第 4 期:钉钉交互审批;第 5 期:Slack 交互审批;其余渠道分期接入)
```

- [ ] **Step 4: Commit**

```bash
git add docs/external-approval-setup.md docs/superpowers/specs/2026-09-27-im-channel-approval-design.md docs/features.md docs/adr/0020-im-channel-approval.md
git commit -m "docs: document Slack approvals and record phase five"
```
