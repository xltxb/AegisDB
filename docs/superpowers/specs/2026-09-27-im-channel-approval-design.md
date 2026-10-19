# 多渠道 IM 审批 — 设计

2026-09-27

取代：ADR-0003（审批魔方对接）、`2026-09-11-telegram-approval-design.md`（其信任模型被本设计吸收并推广到所有渠道）。

## 背景

外部审批目前只有一条路：审批魔方（ADR-0003）。网关把高危工单 POST 给魔方，魔方渲染飞书交互卡片、
收集决定，再回调 `/api/v1/approvals/lark/callback`。另有一条独立的单向飞书群通知
（`notify.larkWebhook`），卡片上只有「前往审批」链接。

这条路把两件事交给了第三方：**渲染卡片**，以及**替网关认人** —— 回调里的 `approver[]` 是魔方认证过的
飞书账号，网关拿它比对发起人邮箱来执行禁自审。本设计把这两件事都收回网关：

- 网关直接对接各 IM 平台（飞书、Telegram、钉钉、Slack、企业微信），不再经过任何中间审批系统。
- 审批人身份由网关自己的绑定表决定，判定逻辑只有站内那一份。

## 已定的决策

| 问题 | 决定 |
|---|---|
| 能力模型 | 通用层定义「群通知」与「交互审批」两种能力，渠道实现自己支持的部分 |
| 交互审批渠道 | 飞书、Telegram、钉钉、Slack |
| 仅通知渠道 | 企业微信（群机器人）。「微信」指企业微信，个人微信无官方机器人 API |
| 认人 | 统一绑定表；所有渠道一律一次性绑定码自助绑定，外加管理员代绑 / 解绑 |
| 多渠道并存 | 可同时启用，卡片扇出；任一渠道决策即生效，其余卡片收起 |
| 入站方式 | 长连接优先（飞书 WebSocket / 钉钉 Stream / Slack Socket Mode / Telegram long polling），webhook 可选 |
| 卡片投递 | 私信审批链上已绑定该渠道的每个成员 |
| 审批魔方 | 整体删除 |
| 实现方式 | 进程内渠道适配器（方案 A），不引入外置桥接服务 |

放弃的方案：

- **外置桥接服务**：网关只定义一个通用回调协议，各 IM 由独立 bridge 对接。身份绑定仍要回到网关，跨进程
  回调又是一个能触发审批的高危入口 —— 等于把刚删掉的魔方换个形式装回来。
- **通知走聚合库（如 Apprise）、交互自建**：交互部分工作量不减，最后是两套机制。

## 1. 信任模型

其余所有章节的前提。

### 1.1 渠道只认人，不判断

IM 回调里只有平台用户 ID（飞书 `open_id`、Telegram `from.id`、钉钉 `staffId`、Slack `U…`）。
这个 ID 由平台保证不可伪造，但**它对应哪个网关账户，只有网关的绑定表知道**。群里、私聊里任何能看到
按钮的人都可能点它。

所以入站只新增一条判断，是现有体系里确实没有的那条：

> `(channel, platformUserID)` 能查到一个启用状态的**真人**网关账户（服务账号一律拒绝，与登录一致）

查到之后带着这个 `*model.User` 走控制台同一条路：

```
DecideBlockFor(actor, ap) → BlockNone | BlockNotPending | BlockSelf | BlockNotInChain
```

`approval_decidability.go` 的注释写明了理由：能不能决定一张单只有一处判断，它同时喂给待办列表，所以
「按钮亮不亮」和「点下去放不放行」永远说的是同一件事。五个渠道各写一份禁自审，就是在系统里放进五个
答案。**渠道包里不出现任何判定逻辑**，只做协议转换。

### 1.2 为什么一律用绑定码

映射方向是 `platformUserID → 网关账户`。若用户能自己填平台 ID，A 填上同事 B 的 ID 之后，B 点按钮
会被认成 A —— B 凭自己的账号拿到了 A 的审批权。唯一索引只能挡「两人绑同一个 ID」，挡不住「绑了别人
的 ID」。绑定时必须证明这个平台账号归申请人所有。

也不用平台通讯录邮箱自动匹配：那等于信任平台目录里的邮箱 = 网关邮箱，目录配错、外部访客、共享频道都
会变成一次提权。代价是每人每渠道手动绑一次，接受。

## 2. 架构

### 2.1 包结构

```
internal/imchannel/            通用层，不依赖任何平台 SDK
  channel.go                   接口、能力、ApprovalCard / Action / Notice 类型
  hub.go                       Hub：扇出、卡片记录、收起、长连接生命周期
  lark/  telegram/  dingtalk/  slack/  wecom/     各平台适配器
internal/service/im_approval.go    认人 + 调 DecideApprovalAs（唯一的入站决策入口）、绑定码逻辑
```

### 2.2 接口

按能力拆小接口，渠道实现自己支持的部分：

```go
type Channel interface {
	Kind() string // "lark" | "telegram" | "dingtalk" | "slack" | "wecom"
}

// 五个渠道都实现。发到配置的通知群：纯文本 + 控制台深链，不带按钮。
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}

// 企业微信不实现。
type Approver interface {
	SendCard(ctx context.Context, platformUserID string, c ApprovalCard) (msgRef string, err error)
	CloseCard(ctx context.Context, msgRef string, final CardState) error // 改终态文案、去掉按钮
	Run(ctx context.Context, sink ActionSink) error                    // 长连接 / polling；ctx 取消即退出
}

type ActionSink interface {
	OnAction(ctx context.Context, a Action) ActionResult  // 按钮点击 → 给点击者看的 toast
	OnDirectMessage(ctx context.Context, dm DM) string    // 私聊文本（绑定码）→ 回复文本
}

type Action struct {
	Channel        string
	PlatformUserID string
	ApNo           string
	Approve        bool
}
```

按钮数据只说「哪张单、什么动作」（如 `ap:<ApNo>:a` / `ap:<ApNo>:r`），**不携带任何权限信息**，权限完全
由 §1.1 决定。

webhook 模式下，各渠道适配器另外提供一个 `http.Handler`，校验签名后把事件交给同一个 `ActionSink`，
与长连接走同一条处理路径。

### 2.3 Hub

- **建单**：取审批链成员 → 对每个启用的交互渠道，查该成员在此渠道的绑定 → 私信卡片 → 写 `tbl_im_card`。
  发起人本人即使在链上也不发（点了也会被 `BlockSelf` 拒）。同时向每个启用了通知的渠道发一条群通知。
- **终态**：查该单所有未关闭卡片 → 按行上的 `channel` 找适配器 `CloseCard` → 标记 `closed`。
- **Reload**：设置保存后调用，按新配置停启各渠道的 `Run`，不需要重启进程。
- 一切出站**异步、best-effort**：失败只记 WARN，不阻塞建单，站内审批始终可用。
- 暴露各渠道运行状态（已连接 / 最近错误 / 最近事件时间）给设置页。

### 2.4 多实例

飞书 WebSocket、钉钉 Stream、Slack Socket Mode 由平台把每个事件分发给任一连接，多实例部署安全。
**Telegram long polling 只能单实例**（第二个 `getUpdates` 会收到 409，两边互踢）；多实例请用 webhook
模式。启动日志与设置页写明。不做选主 / 分布式锁。

## 3. 数据模型

迁移 `0005_im_channel.sql`。

### 3.1 平台账号绑定

```sql
CREATE TABLE IF NOT EXISTS tbl_im_identity (
  user_id          BIGINT       NOT NULL,
  channel          VARCHAR(16)  NOT NULL,   -- lark|telegram|dingtalk|slack
  platform_user_id VARCHAR(128) NOT NULL,
  bound_by         VARCHAR(16)  NOT NULL,   -- self|admin
  created_at       TIMESTAMPTZ  NOT NULL,
  PRIMARY KEY (user_id, channel),
  UNIQUE (channel, platform_user_id)
);
```

- 主键 `(user_id, channel)`：一人每渠道一个账号，重新绑定即覆盖。
- 唯一键 `(channel, platform_user_id)`：一个平台账号不能挂两个网关账户。
- 不往 `tbl_user` 加列：每个渠道一列会让用户表随渠道数膨胀。
- 删除用户时一并删除其绑定。

### 3.2 一次性绑定码

```sql
CREATE TABLE IF NOT EXISTS tbl_im_bind_code (
  user_id    BIGINT      NOT NULL,
  channel    VARCHAR(16) NOT NULL,
  code_hash  VARCHAR(64) NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (user_id, channel)
);
CREATE INDEX IF NOT EXISTS idx_im_bind_code_hash ON tbl_im_bind_code (channel, code_hash);
```

主键保证「一人每渠道同时只有一个有效码」，重新生成是一次 upsert，旧码自然作废。过期行搭审批超时扫描
（每 60s 那一轮）清理。

### 3.3 已发卡片

```sql
CREATE TABLE IF NOT EXISTS tbl_im_card (
  id          BIGSERIAL    PRIMARY KEY,
  approval_id BIGINT       NOT NULL,
  channel     VARCHAR(16)  NOT NULL,
  user_id     BIGINT       NOT NULL,        -- 发给了谁
  msg_ref     VARCHAR(256) NOT NULL,        -- 渠道自定义的消息定位串
  closed      BOOLEAN      NOT NULL DEFAULT FALSE,
  created_at  TIMESTAMPTZ  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_im_card_open ON tbl_im_card (approval_id) WHERE NOT closed;
```

收起卡片按行上的 `channel` 找适配器，**不看当前设置是否启用** —— 渠道被关掉后，它发出去的旧卡片仍要
收起（只要凭据还在；凭据也清空了就只能记 WARN）。否则会留下一批永远挂着、点了没反应的卡片。

### 3.4 魔方遗留

- `tbl_approval.external_task_id` 列与 `idx_approval_ext` 索引：删除。两个用途（魔方关联、超时 PATCH）
  随魔方消失，卡片追踪由 `tbl_im_card` 承担。
- `approval.external.*` 设置行：删除，含两条加密值（token、callbackSecret），不在库里留死凭据。

## 4. 绑定流程

### 4.1 自助绑定

1. 控制台账户菜单 →「IM 账号绑定」，列出当前启用的交互渠道及各自绑定状态。
2. 点某渠道「绑定」→ 网关生成绑定码（如 `BIND-7K3QX9MP`，8 位去歧义字母数字），**只回显这一次**，
   同时显示机器人名称与说明：「用你自己的 xx 账号私聊机器人，发送这串码」。
3. 用户私聊机器人发码 → 适配器经 `OnDirectMessage` 交给 service → 按 `(channel, sha256(code))` 找到码
   的所属用户，校验未过期 → 写 `tbl_im_identity(bound_by=self)`（若该平台 ID 已绑别人则拒绝）→ 删码
   → 写审计 → 机器人回复「已绑定到 张三（zhangsan@…）」。
4. 发送者 ID 来自平台签名 / 连接本身：

| 渠道 | 私聊事件 | 发送者 ID | 之后私信卡片用 |
|---|---|---|---|
| 飞书 | `im.message.receive_v1`（`chat_type=p2p`） | `sender.sender_id.open_id` | 同一 `open_id` |
| Telegram | `message`（`chat.type=private`） | `from.id` | 同一 id 作 `chat_id` |
| 钉钉 | 机器人单聊消息回调 | `senderStaffId` | 机器人批量单聊 `userIds` |
| Slack | `message.im` | `user` | `chat.postMessage(channel=<user>)` |

用户必须先私聊过机器人才能完成绑定，于是「Telegram 机器人不能主动私信没 `/start` 过的人」这类会话
建立问题在绑定时自然解决。

码的约束：10 分钟有效、一次性、库里只存 SHA-256、比较用常量时间、一人每渠道同时只有一个。

### 4.2 不命中的私聊

码不存在 / 过期 / 格式不对 → 只回一句通用提示「无效或已过期的绑定码，请在控制台重新生成」，**不透露
任何与码有关的信息**。非私聊（群消息）一律丢弃，不回复。

### 4.3 管理员代绑 / 解绑

权限页用户行「IM 绑定」，按渠道填平台 ID 或清除（`bound_by=admin`），用于换号、离职、绑错。与现有
代绑 MFA（`POST /users/:id/mfa/bind`）同一信任假设：管理员声明「这个平台账号是这个人的」。用户也能在
账户菜单自助解绑。自助绑定、代绑、解绑全部写审计。

### 4.4 渠道未启用

该渠道的自助绑定入口置灰并说明原因（机器人没在运行，发出去的码没人收 —— 否则表现为「按了没反应」）。
已有绑定保留，渠道再次启用即恢复生效。管理员代绑不受此限。

## 5. 出站

### 5.1 建单挂钩

现有 4 个建单点（`gateway.go`、`pipeline.go`、`export_sensitive.go`、`exec_window_approval.go`）里的
`s.Webhook.SendLarkApproval(ap)` + `s.dispatchExternalApproval(u, ap)` 统一替换为 `s.IM.ApprovalCreated(ap)`。

### 5.2 卡片内容

平台无关的 `ApprovalCard`：

```go
type ApprovalCard struct {
	ApNo       string
	Kind       string // sql | window | export | release
	Env, Tier  string
	Instance   string
	Database   string
	Risk       string
	Initiator  string
	Reason     string
	Command    string // 已脱敏、已截断
	ConsoleURL string
}
```

- 命令**在通用层**先过 `sqlutil.RedactSecrets` 再 `clip`，渠道拿到的已是安全文本，不能绕过。
- **转义由各渠道负责**：Telegram HTML 转义（命令放 `<pre>`）、飞书沿用 `larkSafe`、Slack 转义 `& < >`、
  钉钉 markdown 转义。`WHERE a < b` 这类 SQL 必须能发出去 —— 否则平台拒收整条消息，是一次静默失败。
- 各平台长度上限不同（Telegram 4096 字符等），适配器负责兜底截断。

### 5.3 群通知

触发点不扩：与今天 `notify.larkWebhook` 相同，仅在新工单创建时发一条「新工单待审批」+ 控制台深链。
内容同样脱敏。一个渠道失败不影响其他渠道，也不影响业务路径。

## 6. 入站

| 渠道 | 长连接（默认） | webhook（可选） | webhook 鉴权 |
|---|---|---|---|
| 飞书 | WebSocket 长连接 | `POST /api/v1/im/lark/webhook` | Encrypt Key 解密 + Verification Token |
| Telegram | `getUpdates`，`timeout=30` | `POST /api/v1/im/telegram/webhook` | `X-Telegram-Bot-Api-Secret-Token` |
| 钉钉 | Stream 模式 | `POST /api/v1/im/dingtalk/webhook` | 请求头 timestamp + sign |
| Slack | Socket Mode | `POST /api/v1/im/slack/webhook` | Signing Secret HMAC，时间戳 ±5 分钟 |

webhook 端点在 JWT 中间件之外，**失败朝严**：

- 渠道交互审批未启用、密钥未配置、签名不符、来源 IP 不在 `im.callbackAllowIPs`（非空时）→ 403。
- 鉴权通过但处理出错 → 200 + 日志，避免平台无限重投。
- 所有密钥比较用常量时间。

Telegram 在 polling / webhook 切换时自动 `deleteWebhook` / `setWebhook`；飞书、钉钉、Slack 的 webhook
地址由管理员在平台后台填写，设置页显示要填的完整地址。

## 7. 决策

`HandleIMAction` 的顺序：

1. `(channel, platformUserID)` → 绑定表 → 网关账户。查不到 / 已停用 / 服务账号 → toast 拒绝 + 审计，
   结束。这条审计没有网关账户可归属，`actor` 记 `<channel>:<platformUserID>`、`operator` 记
   `<渠道名>(未绑定)` —— 与失败登录把输入的邮箱记进 `actor` 同一个取舍：**认不出是谁，也要留下
   「有人试过」**。
2. `ApNo` → 工单；不存在 → toast 提示，结束。
3. `DecideApprovalAs(user, ap.ID, approve, clip(user.Name+"("+渠道名+")", 128))`。
4. 按返回渲染 toast：
   - 成功 →「已批准，请发起人回控制台执行」/「已驳回」（ADR 0010：批准不执行）
   - `ErrAlreadyDecided` → 回显当前状态（**幂等**：重投、连点都落在这里）
   - `*DecideRefusal` → 直接用 `Block.Reason()` 的文案
5. 应答点击（Telegram `answerCallbackQuery`、Slack 3 秒内 ack、飞书 / 钉钉回调响应体）。不应答则
   按钮在客户端一直转圈，而这是一个刚刚可能批准了 DROP 的动作。

驳回不填理由（与站内一致）。

### 7.1 DecideApprovalAs

`DecideApproval` 目前把 `actor.Name` 写死为 operator，IM 批的和控制台批的在审计里会长得一样。抽出
`DecideApprovalAs(actor, id, approve, operatorLabel)`，`DecideApproval` 变成
`DecideApprovalAs(actor, id, approve, actor.Name)`。权限判断一处都不动。operator 截到 128，
`tbl_audit_log.operator` 是 `size:128`。

## 8. 收起卡片

单子进入终态的所有出口都调 `s.IM.ApprovalSettled(ap)`：

- `finalizeApproval`（站内决策与 IM 决策）
- 审批超时扫描（替换 `gateway.go` 里的 `cancelExternalApproval`）
- 撤单 / 发布流水线取消（替换 `pipeline.go` 里的 `cancelExternalApproval`，并覆盖 `approval_cancel.go`）

Hub 把该单所有未关闭卡片改成终态文案并去掉按钮：「已由 李四 通过 · 飞书」/「已驳回」/「已超时」/
「已撤回」，标记 `closed`。收起失败只 WARN，工单状态已落定，不回滚决定。

**不做**：卡片发出后审批链被改派（`approval_staffing`），新加入的审批人不补发卡片；旧成员点击会被
`BlockNotInChain` 拒并说明原因。

## 9. 配置

统一 `im.<channel>.*`。🔒 表示加入 `encryptedSettingKeys` 与 `isSecretKey`，沿用「留空 = 保持原值、
库里只存密文、接口不回显」。

| 渠道 | 交互审批 | 群通知 |
|---|---|---|
| 飞书 | `approval`、`domain`(feishu/lark)、`appID`、`appSecret`🔒、`mode`(ws/webhook)、`encryptKey`🔒、`verificationToken`🔒 | `notify`、`notifyWebhook`、`notifySecret`🔒（自定义机器人） |
| Telegram | `approval`、`botToken`🔒、`mode`(polling/webhook)、`webhookSecret`🔒 | `notify`、`notifyChatID`（复用同一 bot） |
| 钉钉 | `approval`、`clientID`、`clientSecret`🔒、`robotCode`、`mode`(stream/webhook) | `notify`、`notifyWebhook`、`notifySecret`🔒（自定义机器人加签） |
| Slack | `approval`、`botToken`🔒、`appToken`🔒、`mode`(socket/webhook)、`signingSecret`🔒 | `notify`、`notifyWebhook`（Incoming Webhook） |
| 企业微信 | — | `notify`、`notifyWebhook`（群机器人） |

公共键：`im.callbackBaseURL`（webhook 模式公网根地址）、`im.callbackAllowIPs`（webhook 来源白名单，
可空）。`notify.consoleURL` 保留。

所有开关默认关闭。出站 URL 继续过 `validateOutboundURL`（SSRF 防护）；使用平台 SDK 的渠道要么注入
网关的受限 HTTP client，要么确认 SDK 只连固定平台域名。

## 10. API

```
GET    /api/v1/settings/im/status                  各渠道运行状态
POST   /api/v1/settings/im/:channel/test-notify    往通知群发测试消息
POST   /api/v1/settings/im/:channel/test-card      给当前管理员私信测试卡片（需已绑定；点击只回 toast，不触及任何工单）
GET    /api/v1/me/im-identities                    我的各渠道绑定状态
POST   /api/v1/me/im-identities/:channel/code      生成绑定码（仅回显一次）
DELETE /api/v1/me/im-identities/:channel           自助解绑
PUT    /api/v1/users/:id/im-identities/:channel    管理员代绑 {platformUserId}
DELETE /api/v1/users/:id/im-identities/:channel    管理员解绑
POST   /api/v1/im/:channel/webhook                 webhook 入口（JWT 之外，独立鉴权）
```

删除：`POST /api/v1/approvals/lark/callback`、`POST /api/v1/settings/lark/test`（由 test-notify 取代）。
`backend/docs/openapi.yaml` 同步。

## 11. 前端

- **设置 →「IM 渠道」页**：取代「外部飞书审批」与「飞书通知」两块。每个渠道一张折叠卡：交互审批 / 群
  通知两个开关、凭据、模式选择、运行状态点、两个测试按钮；webhook 模式显示要填到平台后台的回调地址；
  Telegram polling 模式注明「仅限单实例部署」。
- **账户菜单 →「IM 账号绑定」**：渠道列表、状态、绑定 / 解绑；绑定码弹出一次，含机器人名称与操作说明；
  未启用的渠道置灰并说明原因。
- **权限页用户行**：「IM 绑定」，与代绑 MFA 并列。
- 文案进 `locales/zh.ts` / `en.ts`，删除魔方相关文案。

## 12. 删除审批魔方

**后端**

- `internal/service/external_approval.go` 整个文件：`extApprovalConfig`、`dispatchExternalApproval`、
  `cancelExternalApproval`、`DecideApprovalExternal`、`approversAreInitiator`、`VerifyExternalCallback`。
  `ipAllowed` 移到 `imchannel` 复用；`decryptSetting`、`nonBlank` 如仍有调用方则移到合适位置。
- `internal/service/webhook.go`：`SendExternalApproval`、`PatchExternalStatus`、`SendLarkApproval`、
  `TestLark`；`postLark`、`larkSign`、`larkSafe`、`larkApprovalCard` 移入 `imchannel/lark`。
- `dto.LarkApprovalCallbackReq`；handler `LarkApprovalCallback` 与路由；设置保存里 `approval.external.*`
  的校验分支与加密键。
- `Repo.SetApprovalExternalTask`；`model.Approval.ExternalTaskID`。
- `bootstrap/seed.go` 里的 `approval.external.*` 默认值。

**测试**

删除或改写：`external_approval_test.go`、`external_never_dispatched_test.go`、
`external_decide_race_test.go`、`callback_status_test.go`、`lark_test.go`、
`external_approval_redact_test.go`，以及 `dual_snapshot_test.go`、`config_audit_test.go`、
`settings_persist_test.go`、`webhook_secret_test.go`、`settings_secret_test.go`、`harness_test.go` 中涉及
`approval.external.*`、`notify.lark*` 或魔方回调的部分。脱敏、竞态、幂等三类断言以新渠道的形式保留，**断言强度不降**。

**前端**：设置页魔方区块、`ext*` 字段、相关文案。

**文档**

- ADR-0003 状态改为 `Superseded by ADR-0020`。
- 新增 ADR-0020「多渠道 IM 审批」。
- `2026-09-11-telegram-approval-design.md` 开头注明已被本设计吸收。
- `docs/external-approval-setup.md` 重写为各渠道接入指南：建应用、所需权限 scope、长连接与 webhook 如何
  选、绑定流程怎么跟用户讲。
- README「审批」「系统设置」两节、`docs/features.md`、`DEPLOY.md` 同步。

## 13. 升级与兼容

迁移 `0005_im_channel.sql` 顺序：

1. 建 `tbl_im_identity`、`tbl_im_bind_code`、`tbl_im_card`。
2. `notify.lark` / `notify.larkWebhook` / `notify.larkSecret` 搬到 `im.lark.notify` /
   `im.lark.notifyWebhook` / `im.lark.notifySecret`（加密值原样搬，不解密），删除旧键。
3. 删除 `approval.external.*` 全部设置行。
4. `DROP INDEX IF EXISTS idx_approval_ext`；`ALTER TABLE tbl_approval DROP COLUMN IF EXISTS external_task_id`。

升级后行为 = 纯站内审批 + 原有飞书群通知，所有交互审批开关默认关闭，不会意外打开任何入站口。

**在途的魔方工单**：回调端点已不存在，飞书卡片点击无效，只能在控制台处理。升级说明写明：升级前先在
魔方侧清空待审单，或接受这一点。

遵守 README 顺序：备份 → 停旧进程 → migrate → 起新进程。迁移删列，不能与旧二进制共存。

## 14. 分期交付

每期可独立合并、独立可用：

1. **框架 + 删魔方**：`imchannel` 接口与 Hub、三张表与迁移、绑定码 / 代绑 API、`HandleIMAction`、
   `DecideApprovalAs`、建单 / 终态挂钩、webhook 路由骨架、设置页框架与绑定弹窗；魔方全部删除；飞书
   群通知迁入新框架（`imchannel/lark` 先只实现 `Notifier`）。
2. **Telegram**：无 SDK，协议最简单，用来验证框架。
3. **飞书**交互审批：WebSocket 长连接（官方 Go SDK `larksuite/oapi-sdk-go/v3`）。
4. **钉钉**交互审批：Stream 模式（官方 `dingtalk-stream-sdk-go`）。
5. **Slack**交互审批：Socket Mode（`slack-go/slack`）。
6. **群通知补齐**：Telegram / 钉钉 / Slack / 企业微信。

## 15. 失败朝严清单

| 情形 | 行为 |
|---|---|
| 渠道交互审批未启用 | webhook 403，长连接不启动，不发卡片 |
| webhook 密钥未配置 / 签名不符 / IP 不在白名单 | 403 |
| 平台 ID 未绑定 | 拒绝 + toast + 审计（`actor=<channel>:<id>`） |
| 绑定账户已停用 / 是服务账号 | 拒绝 + toast + 审计 |
| 发起人自审且 `allowSelfApprove=false` | 拒绝（`BlockSelf`） |
| 不在审批链 | 拒绝（`BlockNotInChain`） |
| 重投 / 连点 | 回显当前状态，不重复决策 |
| 绑定码过期 / 不匹配 / 来自群聊 | 不绑定，不回显码相关信息（群聊不回复） |
| 平台 ID 已绑定其他账户 | 拒绝绑定 |
| 卡片推送失败 | WARN；建单照常 |
| 卡片收起失败 | WARN；不回滚决定 |

## 16. 测试

`internal/bootstrap` 端到端 harness；平台 API 用 `httptest` 假服务器，长连接用可注入的假 transport。

**通用层**（每条只测一次，不随渠道重复）：

- 未绑定的平台 ID 点击 → 工单仍 pending，审计里有 `<channel>:<id>` 这一行
- 绑定了但不在链上 → `BlockNotInChain`
- 发起人自己点且 `allowSelfApprove=false` → `BlockSelf`；打开开关后同一点击 → 通过（证明判定走的是
  `DecideBlockFor`，不是另写的一份）
- 停用账户 / 服务账号的绑定 → 拒
- 同一点击投递两次 → 只决策一次
- 站内先批、IM 后点 → 回显已处理
- IM 批准后，该单所有渠道所有卡片收起；超时、撤单同样收起
- 渠道关闭后发出的旧卡片仍被收起
- 建单只给链上已绑定成员发卡片，不给发起人发
- 渠道关闭 → webhook 403、长连接不启动；签名错 → 403；鉴权过但处理失败 → 200
- 绑定码：过期无效、用一次即失效、重新生成作废旧码、不匹配时回复不含码信息、群消息里的码不触发绑定、
  平台 ID 已绑他人时拒绝
- 审计 operator 带渠道标签且不超过 128
- 迁移：`notify.lark*` 正确搬迁（含加密值）、`approval.external.*` 删除、`external_task_id` 列删除

**每个渠道适配器**：签名校验、卡片渲染、点击解析、私聊解析、应答调用、收起调用；出站原始请求体里明文
口令一个字节都不出现；含 `<` `&` `` ` `` 的 SQL 正确转义且能发出。

## 17. 明确不做

- **IM 里发起工单**：只做决定，不做提单。提单要带实例、库、SQL 正文和 MFA，聊天窗口不是合适入口。
- **驳回填理由**的多轮对话。
- **改派后补发卡片**。
- **Telegram 多实例 polling**：不做选主。
- **平台通讯录邮箱自动匹配**：见 §1.2。
- **群成员即审批人**的降级选项。
- **个人微信**。
- 群通知触发点扩展（导出完成、发布完成等）。

## 18. 第 1 期实现记录

与上文的以下几处偏离与补充，均为实现时的细化，不改变设计意图：

- **迁移拆成三个文件**：`0005_im_channel.sql`（三张表）、`0006_im_lark_notify_keys.sql`（飞书通知
  键搬家）、`0007_drop_approval_cube.sql`（删魔方遗留）。每个迁移和依赖它的代码在同一次提交里落地。
- **绑定码按哈希查找**，而不是取出后常量时间比较：库里只有 `sha256(code)`，查的也是哈希，查找本身
  不泄露码。
- **新增 `GET /users/:id/im-identities`**：权限页要显示某人的绑定状态，§10 漏列了这个读接口。
- **飞书群通知迁移时保留「默认开」语义**：旧 `notify.lark` 默认 true 且生产环境不播种，只配了
  webhook 的部署升级后照常发通知。
- **webhook 入口推迟到第 2 期**：`POST /api/v1/im/:channel/webhook`、`im.callbackBaseURL`、
  `im.callbackAllowIPs` 与来源 IP 白名单都随第一个用到它们的渠道（Telegram）一起落地。第 1 期没有
  真实的交互渠道，提前放一个没有调用方的公开入口只会是一段无人测试的高危代码。
- 基线迁移 `0001_init.sql` 同步删掉了 `external_task_id` 列与 `idx_approval_ext` 的定义（注释标明由
  0007 删除）。测试夹具会在空台账上重放全部迁移，0007 删列后 0001 里那条建索引语句会失败；迁移台账
  只按文件名记录、不校验内容，已升级的库不会重跑 0001，全新安装与升级安装最终表结构一致。
- 飞书群通知不再写入 `/settings/webhook/deliveries` 投递记录：失败记 WARN 日志，并把最近一次失败
  原因记进渠道状态的 `lastNotifyError`（「设置 › IM 渠道」状态标签显示），下一次发送成功即清空。
- 旧的 `approval.external.*` 与 `notify.lark*` 键若由旧前端带来，保存设置时静默丢弃。
- 控制台审批接口前的 `menu("approve")` 挪进了判定：`DecideBlockFor` 新增 `BlockNoApproveMenu`
  （链成员的角色没有审批菜单），IM 点击因此与控制台同样受菜单约束。
- 单号不存在、或非发起人 / 非链成员点了已处理的单，都回「仅审批链成员可处理该工单」，不泄露单是否
  存在与终态。
- Hub 把入站事件的渠道钉成投递它的适配器；运行循环、排队任务、群通知里的 panic 被 recover 并记日志 /
  状态，不拖垮网关。

## 19. 第 2 期（Telegram）实现记录

- **收卡用 `editMessageReplyMarkup`，不用 `editMessageText`**：网关不存卡片原文，重写正文会丢掉
  「这张单原来是什么」。改为把两个按钮换成一颗显示终态的按钮（`callback_data = done`，点它只回
  「该工单已处理」）。
- **webhook 入口是所有渠道共用的一条路由** `POST /api/v1/im/:channel/webhook`，由
  `Hub.ServeWebhook` 按渠道分派；渠道实现可选接口 `Webhooker` 自己验 secret / 签名。
- **webhook 入口的拒绝顺序**：来源 IP 白名单（`im.callbackAllowIPs`，非空时）→ 渠道未登记 404 →
  不能审批 / 未配置 / 开关关 / 不收 webhook 403 → 渠道自己验 secret（403）→ 之后一律 200。渠道
  panic 被兜住并回 200。
- **`im.telegram.apiBase`**：新增的配置键，缺省 `https://api.telegram.org`，供自建 Bot API server
  与测试使用。
- **`getUpdates` 每批最多 10 条（`limit`）**：适配器读响应有 1 MiB 上限；不设上限时，任何人私聊
  机器人一批超长消息就能让每一批都解析失败、轮询停在同一个 offset 上，审批通道随之停摆。
- **bot token 在请求 URL 里**，适配器返回的所有错误都先把 token 换成 `<bot-token>`，状态页与日志
  都看不到它。
- **卡片里的控制台链接改为 `/approvals`**：前端是 BrowserRouter，原来的 `/#/approvals` 会落到总览页。
- **第 1 期暂缓项在本期收口的**：Hub `Stop` 之后不再接任务；长连接无声退出留痕；重复登记不卡死
  `Reload`；并发绑定同一平台账号只有一个成功，失败方得到「已绑定到其他网关账户」；代绑拒绝停用账户
  与服务账号；代解绑只在真的删到时写审计。
- Telegram 群通知不在本期，属于第 6 期。

## 20. 第 3 期（飞书交互审批）实现记录

- **出站不用 SDK，入站用 SDK**：发卡、改卡、查机器人名、回私聊都是几个固定的 OpenAPI，用标准库直接调，
  与 Telegram 适配器一个写法，测试也能用同一种假服务器；长连接是 protobuf 帧 + 心跳 + 重连，用官方 SDK
  （`larksuite/oapi-sdk-go/v3` 的 `ws` 包）。SDK 的取连接地址与 WebSocket 拨号都注入网关受控的出站客户端；
  拿不到受控拨号器就拒绝建立长连接。SDK 的 Debug 日志一律丢弃（会打印含绑定码的事件 payload）。
- **飞书渠道按配置分两种形态**：只配群机器人 webhook 是只能通知的 `*Channel`；配了 App ID + App Secret 是
  `*App`（嵌着群通知那一半）。Hub 靠类型断言 `Approver` 判断能否发卡片，只配群通知的飞书不能出现在绑定弹窗里。
- **收卡整张替换**：飞书改卡片只能整张替换，网关不存卡片原文，所以收起后的卡片只留单号与终态。msgRef 是
  `<message_id>|<ApNo>`；卡片 `config.update_multi = true` 才能被改写。
- **按钮 value**：`{"ap": "<ApNo>", "act": "a" | "r"}`，ApNo 白名单 `^[A-Za-z0-9-]{1,32}$`。
- **`Webhooker` 的返回值改为 `WebhookReply{Status, Body}`**：飞书的 URL 校验要回 challenge，卡片回调的 toast
  在响应体里。Telegram 不受影响。
- **webhook 不用 SDK 的 `dispatcher.Handle`**：它的签名比较不是常量时间、验签失败回 500（飞书会重投）、允许
  未加密请求只靠 Token 过关。网关自己做：只收加密请求 → 解密 → 常量时间比对 Verification Token →
  （URL 校验到此回 challenge，飞书不给它签名）→ 常量时间验签 → 交给同一个分发器。任何一步失败都是同一个 403。
- **私聊按 `message_id` 去重**（最近 512 条）：飞书没及时收到确认会重推，绑定码第二次到达时已被消费。
- **原因改用 `plain_text`**：群通知与交互卡片都是。lark_md 会把发起人写的 markdown 链接渲染成真链接。
- **tenant_access_token** 缓存到过期前 5 分钟；飞书回 `99991661` / `99991663` 时换新 token 重试一次。
- **永久性错误停循环**：飞书明确拒绝（code 非零，频控 `99991400` 除外）或开放平台地址回了 4xx，`Run` 返回
  错误，设置页显示原因。
- `isSecretKey` 认得 `encryptKey`：它名字里没有 secret / token，但同样能伪造飞书推送。
- 新增配置键 `im.lark.apiBase`（私有化部署与测试用）。它走出站 URL 校验（https + 非私网），生产环境指向内网需部署配置 `webhook.allow_private: true`（`VELA_WEBHOOK_ALLOW_PRIVATE`）。
