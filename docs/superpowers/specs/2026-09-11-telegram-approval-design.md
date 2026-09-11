# 外部审批新增 Telegram 渠道 — 设计

2026-09-11

## 背景

外部审批目前只有一条路：审批魔方（ADR-0003）。网关把高危工单 POST 给它，它渲染飞书
交互卡片、收集决定，再回调 `/api/v1/approvals/lark/callback`。这条路能成立，靠的是
**魔方替网关保证了审批人身份** —— 回调里的 `approver[]` 是飞书账号，网关拿它比对发起人
邮箱就能执行禁自审。

Telegram 没有这个中间层。网关直接调 Bot API 发一条带 `inline_keyboard` 的消息，用户点
按钮，Telegram 把 `callback_query` 送回来 —— 里面只有 `from.id`（Telegram 数字 ID）和
可选的 `from.username`。**群里任何成员都能点这个按钮**，而网关不认识这两个字段里的任何
一个。

所以这不是"再写一个 dispatcher"。整个设计的重心在一件事上：**把一个 Telegram 数字 ID
变成一个网关账户**。做不到这一点，禁自审、审批链成员校验、审计里 operator 的真实归属会
同时失效 —— 拉进群的任何人都能批准生产库的 DROP。

## 已定的决策

| 问题 | 决定 |
|---|---|
| 点按钮的人是谁 | 绑定到网关账户（`tbl_user.telegram_user_id`） |
| 绑定怎么验 | 一次性绑定码自助绑定 **+** 管理员代绑 / 解绑 |
| 回调怎么进来 | long polling 与 webhook 两种模式，设置里选 |
| 与飞书外审的关系 | 互斥，设置里选渠道 |
| 切换渠道时的在途单 | 切换即失效：只认当前渠道的回调 |
| 范围 | 审批卡片 · 超时收起按钮 · 群通知 · 设置页界面 |

## 1. 信任模型

这一节是其余所有章节的前提。

### 1.1 为什么不能让用户自己填 Telegram ID

映射方向是 `telegram_user_id → 网关账户`。若用户 A 能在自己的资料里填任意数字，他填上
同事 B 的 Telegram ID 之后，**B 用自己的 Telegram 点按钮会被认成 A** —— B 凭自己的账号
拿到了 A 的审批权。填一个数字完成的提权，比没有这个集成更糟。

唯一索引只能挡住"两个账户绑同一个 ID"，挡不住"绑了别人的 ID"。要挡住后者，绑定时必须
证明这个 Telegram 账号归申请人所有。

### 1.2 一次性绑定码

用户在控制台点「绑定 Telegram」→ 网关生成一个一次性码 → 用户用**自己的** Telegram 私聊
把码发给 bot → bot 收到的 `from.id` 就是真实发送者（这一点由 Telegram 保证，伪造不了）
→ 网关按码找到用户，写入 `telegram_user_id`。

码的约束：

- 有效期 10 分钟，一次性，用掉即删
- 一人同时只有一个有效码，重新生成会作废前一个
- 比较用 `crypto/subtle.ConstantTimeCompare`
- 码只在生成时回显一次，库里存哈希（与开放接口凭据同样的处理）
- 私聊里收到不匹配的文本 → 不回显任何与码有关的信息，只回一句通用提示

### 1.3 管理员代绑

管理员在「权限」页可以直接填 / 清空某人的 `telegram_user_id`，与现有的代绑 MFA
（`POST /users/:id/mfa/bind`）同一个信任假设：管理员声明"这个 Telegram 账号是这个人的"。
用于离职回收、换号、绑错这类情形。代绑与解绑都写审计。

### 1.4 认人，然后把判断交回原处

这是本设计最重要的一条：**Telegram 渠道只负责"认人"，不自己判断"能不能批"。**

`from.id` → 网关账户之后，剩下的全部交给控制台走的那条路：

```
DecideBlockFor(actor, ap) → BlockNone | BlockNotPending | BlockSelf | BlockNotInChain
```

`approval_decidability.go` 上的注释把理由写得很清楚：能不能决定一张单，**只有一处判断**，
它同时喂给待办列表，所以"按钮亮不亮"和"点下去放不放行"永远说的是同一件事。Telegram
再写一份禁自审、再算一次审批链，就是在这个系统里放进第二个答案 —— 两份实现迟早会在某次
只改了一边的提交里分叉，而分叉的方向是不可预测的：可能是 Telegram 拦下了控制台放行的，
也可能反过来。

所以新增的判断**只有一条**，是现有体系里确实没有的那条：

> `from.id` 能查到一个启用状态的**真人**网关账户（服务账号一律拒绝，与登录一致）

查不到就到此为止。查到了就带着这个 `*model.User` 走 `DecideApproval`，其余三种拒绝由
`DecideBlockFor` 给出，且 `DecideRefusal.Block.Reason()` 本来就带着给人看的具体文案 ——
"不在链上要去找管理员，自己发起的要去找同事，两件事完全不同"（原注释），这段区分正好
原样渲染成 Telegram 的 toast。

**审批人身份不来自消息体的任何字段** —— 只来自 `from.id` 到网关账户的映射。这是与魔方
回调最大的不同：魔方的 `approver[]` 是可信输入（魔方认证过），Telegram 的 `from` 是
Telegram 认证过的发送者，但它对应谁，只有网关的绑定表知道。

也因此 Telegram **不走** `DecideApprovalExternal`。那个函数存在的理由是魔方回调没有
`*model.User` 可用，只能拿一串飞书账号字符串去比对邮箱；Telegram 认完人手里就有真正的
用户对象，走控制台同一条路更短也更准。

## 2. 数据模型

### 2.1 迁移 0036

```sql
ALTER TABLE tbl_user ADD COLUMN telegram_user_id VARCHAR(32) NULL;
CREATE UNIQUE INDEX idx_user_telegram ON tbl_user (telegram_user_id);
```

MySQL 与 SQLite 的唯一索引都允许多个 NULL，所以"绝大多数人没绑"不会互相冲突。存字符串
而非整数：Telegram 的 user id 目前在 int64 范围内，但官方文档只承诺"至多 52 位有效
整数"，用字符串省掉一次无谓的边界讨论，也方便与 chat_id 同样处理。

绑定码不落主表，单独一张小表：

```sql
CREATE TABLE tbl_telegram_bind_code (
  user_id     BIGINT      NOT NULL,
  code_hash   VARCHAR(64) NOT NULL,
  expires_at  DATETIME    NOT NULL,
  created_at  DATETIME    NOT NULL,
  PRIMARY KEY (user_id)
);
```

主键是 `user_id` 而不是自增 —— "一人同时只有一个有效码"由主键保证，重新生成就是一次
upsert，不需要额外的清理逻辑去作废旧码。过期行由绑定时的 `expires_at` 判断，另有一个
随审批超时扫描搭车的清理（每 60s 那一轮）。

### 2.2 审批单上的外部任务 ID

复用现有的 `Approval.ExternalTaskID`（`size:128`），Telegram 侧存 `tg:<chat_id>:<message_id>`。

这么做有一个具体好处：**超时收起按钮不依赖当前的渠道设置**。「切换即失效」说的是回调的
决定权，但一张旧单该收起的卡片仍然应该收起 —— 从这个字段的前缀就能判断该用哪条路去
清理，即使设置早就切到了另一个渠道。

## 3. 配置

### 3.1 渠道选择器

新增 `approval.external.channel`，取值 `off | lark | telegram`。

现有的 `approval.external.enabled`（布尔）**保留但降级为兼容读**：升级后的第一次读取，
若 `channel` 未设置而 `enabled=true`，视作 `lark` 并写回 `channel`。不动 `enabled` 的
存量值，也不在界面上再暴露它 —— 一个"看着像开关、其实什么也不控制"的东西是 0030 迁移
注释里点过名的问题。

### 3.2 Telegram 配置项

| 键 | 说明 | 加密 |
|---|---|---|
| `approval.telegram.botToken` | Bot token | 是 |
| `approval.telegram.chatID` | 推送审批卡片的 chat（群或私聊） | 否 |
| `approval.telegram.mode` | `polling` \| `webhook` | 否 |
| `approval.telegram.webhookSecret` | `setWebhook` 的 secret_token | 是 |
| `approval.telegram.callbackBaseURL` | webhook 模式下的公网根地址 | 否 |
| `approval.telegram.allowIPs` | webhook 模式的来源 IP 白名单（可空） | 否 |

`botToken` 与 `webhookSecret` 加入 `encryptedSettingKeys` 与 `isSecretKey`，沿用"空值 =
保持原值、库里只存密文、接口不回显"的既有规则。

群通知是**另一套独立配置**（与 `notify.lark` 和 `approval.external` 的关系一致）：

| 键 | 说明 | 加密 |
|---|---|---|
| `notify.telegram` | 开关 | 否 |
| `notify.telegram.botToken` | 可与审批 bot 相同，留空则复用审批的 | 是 |
| `notify.telegram.chatID` | 通知群 | 否 |

## 4. 出站：审批卡片

新增 `service/telegram.go`，与 `webhook.go` 里的魔方对接平级。

`sendMessage` 的载荷：

- `chat_id`：配置值
- `text`：`env/tier · 实例/库 · 风险等级 · 发起人 · 原因 · 命令`，命令过
  `sqlutil.RedactSecrets` 后再 `clip`。Telegram 单条消息上限 4096 字符，命令截到 800，
  整体再兜一次底 —— 超限时 Telegram 返回 400，而这是一条"卡片发不出去"的静默失败。
- `parse_mode`：`HTML`，命令放在 `<pre>` 里，且**先做 HTML 转义**。SQL 里出现 `<` 和 `&`
  是常态（`WHERE a < b`），不转义会让整条消息被 Telegram 拒绝。
- `reply_markup.inline_keyboard`：两个按钮，`callback_data` 分别是
  `ap:<ApNo>:a` 与 `ap:<ApNo>:r`。

`callback_data` 上限 64 字节，`AP-` 前缀的单号远在其内。这个字段不承载任何权限信息 ——
它只是"哪张单、什么动作"，权限完全由 §1.4 的三个条件决定。它是网关自己发出去、Telegram
原样带回的，外人无法凭空构造（要构造得先能让 bot 发消息）。

推送与魔方一致：**异步、best-effort，失败不阻塞建单**。失败写 WARN 日志，控制台里的
审批路径照常可用。

出站目标是 `api.telegram.org`，走现有的 SSRF 防护客户端 —— 需确认 `validateOutboundURL`
放行公网地址（它拦的是内网），并为自建 Bot API server 的情形留一句文档说明。

## 5. 入站

### 5.1 webhook 模式

新增 `POST /api/v1/approvals/telegram/callback`，与现有魔方回调同级、同样在鉴权中间件
之外。

认证用 `setWebhook` 时设置的 `secret_token`：Telegram 在每个请求带
`X-Telegram-Bot-Api-Secret-Token` 头。比较用常数时间。**失败朝严**：`channel != telegram`、
或 secret 未配置、或不匹配、或 IP 不在白名单 → 一律 403，与
`VerifyExternalCallback` 的行为逐条对齐（含"开关关掉时端点也必须关掉"这一条 —— EA1 说的
就是这个）。

注意 Telegram 不接受非 2xx 的重试语义：返回非 200 它会重投。**处理失败要返回 200 加
一条日志**，只有鉴权失败才 403（那种情况重投多少次都一样）。

### 5.2 polling 模式

启动一个后台 goroutine 循环 `getUpdates`，`offset` 取上一轮最大 `update_id + 1`，
`timeout=30` 走长轮询。

这条路不需要公网入口，对内网部署的网关是现实得多的选择。它的代价必须写清楚：

- **Telegram 只允许一个 `getUpdates` 连接**。多实例部署时第二个进程会一直收到 409，
  两边互相踢。当前设计**不解决**这个问题，只在启动日志和设置页上写明"polling 模式限单
  实例"。多实例环境应当用 webhook。
- 进程重启期间的点击不会丢：`offset` 未确认的 update 会在下次 `getUpdates` 重投。但
  **重投意味着同一次点击可能被处理两次** —— 幂等性由 §6 保证。
- 切到 polling 时要先 `deleteWebhook`，否则 Telegram 不会给 `getUpdates` 任何东西；
  切到 webhook 时相应地停掉 goroutine 并 `setWebhook`。这两件事在设置保存后触发。

两种模式最终都把 `callback_query` 交给同一个 `HandleTelegramCallback`，安全校验只有一
份实现。

#### 5.3 私聊绑定消息

同一条入站路径还要认一种消息：私聊里发来的纯文本（绑定码）。判据是 `message.chat.type
== "private"` 且文本匹配码的格式。命中就走 §1.2 的绑定流程，**不命中就丢弃且不回显任何
提示** —— 群里的普通聊天不该触发任何逻辑。

**绑定依赖 bot 在运行**：bot token 来自 `approval.telegram.botToken`，而只有
`channel = telegram` 时 polling goroutine 才启动、webhook 才注册。所以渠道不是 telegram
时，控制台里的「绑定 Telegram」入口**置灰并说明原因**，而不是让人生成一个发出去没人收的
码 —— 那会表现为"按了没反应"，而这类静默失败最难排查。管理员代绑不受此限（它不经过
bot）。

## 6. 决定流程

`HandleTelegramCallback` 的顺序：

1. 解析 `callback_data` → ApNo + 动作
2. `from.id` → 网关账户；查不到 / 已停用 / 服务账号 → toast 拒绝 + 审计，结束。
   这条审计没有网关账户可归属，`actor` 记 `telegram:<from.id>`、`operator` 记
   `Telegram(未绑定)` —— 与失败登录把输入的邮箱记进 `actor` 是同一个取舍：**认不出是谁，
   也要留下"有人试过"**。一个陌生 ID 反复点生产库工单的按钮，是需要被看见的事。
3. 按 ApNo 取工单；不存在 → toast 提示，结束
4. `DecideApproval(actor, ap.ID, approve)`
5. 按返回分支渲染 toast：
   - 成功 → 「已批准，请回控制台执行」/「已驳回」
   - `ErrAlreadyDecided` → 回显当前状态（**幂等**：polling 重投、用户连点两下都落在这里）
   - `*DecideRefusal` → 直接用 `Block.Reason()` 的文案
6. `answerCallbackQuery` 把 toast 发出去
7. `editMessageText` 把卡片改成终态文案并去掉按钮

第 4 步之后的一切（执行、审计、通知、发布流水线联动、`ClaimApproval` 的原子占位）都由
既有实现承担。**这一节真正新增的代码只有"认人"和"改卡片"**，中间那段是调用。

第 6 步不能省。不调 `answerCallbackQuery`，Telegram 客户端上那个按钮会一直转圈，点击者
不知道发生了什么 —— 而这是一个刚刚可能执行了 DROP 的动作。

### 6.1 审计里要看得出是哪个渠道批的

`DecideApproval` 目前把 `actor.Name` 直接当 operator 传给 `finalizeApproval`。Telegram
批的和控制台批的会因此在审计里长得一模一样。

小幅改动：抽出 `DecideApprovalAs(actor, id, approve, operatorLabel string)`，
`DecideApproval` 变成 `DecideApprovalAs(actor, id, approve, actor.Name)`。Telegram 传
`actor.Name + "(Telegram)"`，仍然 `clip(…, 128)` —— `tbl_audit_log.operator` 是
`size:128`，而"由外部输入决定审计写不写得进去"是 `DecideApprovalExternal` 注释里已经
栽过一次的坑。

权限判断一处都不动，只是把写死的 operator 变成参数。

## 7. 超时收起

现有 `cancelExternalApproval` 在内部超时自动驳回后，PATCH 魔方收起飞书卡片。Telegram 的
对应动作是 `editMessageText` + 去掉 `reply_markup`。

按 §2.2，从 `ExternalTaskID` 的 `tg:` 前缀判断走哪条路，**不读当前渠道设置** —— 否则
切换渠道会留下一批永远挂着的、点了没反应的卡片。

## 8. 群通知

独立于审批卡片：`notify.telegram` 开关控制，复用现有 `notify.lark` 的触发点（新审批单、
导出完成 / 失败、发布完成 / 失败 / 等待处理）。纯文本消息，**不带按钮**，带一个回控制台
的深链（`notify.consoleURL`）。

同样过 `RedactSecrets`。一个失败不影响另一个渠道，也不影响业务路径。

## 9. 前端

**设置 · 审批**：现有的「外部飞书审批」区上方加一个渠道选择器（关 / 飞书 / Telegram），
选中哪个就只展开哪一组配置。Telegram 组含 bot token、chat id、模式、webhook secret、
回调根地址、IP 白名单，外加一个「发送测试卡片」按钮（复用 `POST /settings/lark/test`
的形状，新增 `/settings/telegram/test`）。

切换渠道时**必须给一句明确警告**：「切换后，已在旧渠道待审的工单点击将不再生效，需回
控制台处理。」这是「切换即失效」的直接后果，不写出来就会变成一次静默的故障。

**设置 · 通知**：加 Telegram 群通知开关与 chat id。

**账户菜单**：加「绑定 Telegram」，交互与现有 MFA 自助绑定一致 —— 弹窗显示一次性码和
bot 的用户名，说明"用你自己的 Telegram 私聊发给它"，绑定成功后显示已绑定状态与解绑入口。

**权限页**：用户行上加代绑 / 解绑 Telegram，与代绑 MFA 并列。

## 10. 失败朝严清单

沿用整个网关的原则，逐条明确：

| 情形 | 行为 |
|---|---|
| `channel != telegram` | webhook 端点 403，polling goroutine 不启动 |
| bot token 为空 | 不推送，WARN 日志；建单照常 |
| `secret_token` 未配置（webhook 模式） | 端点 403，拒绝一切 |
| `from.id` 查不到账户 | 拒绝 + toast + 审计 |
| 账户是服务账号或已停用 | 拒绝 + toast + 审计 |
| 账户不在审批链 | 拒绝 + toast + 审计（`BlockNotInChain`） |
| 绑定码过期 / 不匹配 | 不绑定，不回显任何码相关信息 |
| 卡片推送失败 | WARN；不阻塞建单 |
| `editMessageText` 失败 | WARN；工单状态已经落定，卡片不同步不回滚决定 |

## 11. 测试

沿用 `internal/bootstrap` 的端到端 harness 风格（参考 `lark_test.go`、
`external_approval_test.go`）：

- 未绑定的 `from.id` 点击 → 工单仍为 pending
- 绑定了但不在审批链 → 工单仍为 pending
- 发起人自己点击且 `allowSelfApprove=false` → 拒绝（`BlockSelf`）
- 打开 `allowSelfApprove` 后同一次点击 → 通过（证明走的确实是 `DecideBlockFor`，
  而不是另写了一份判断）
- 同一次 `callback_query` 投递两次 → 只执行一次（polling 重投）
- `channel=lark` 时 Telegram 端点返回 403（切换即失效）
- `channel=off` 时两个端点都 403
- webhook secret 不匹配 → 403
- 出站载荷里不含明文口令（对齐 `external_approval_redact_test.go` 的断言强度：断言原始
  请求体里明文一个字节都不出现）
- 含 `<` `&` 的 SQL 能正常推送（HTML 转义）
- 绑定码：过期不生效、用过一次即失效、重新生成作废旧码

## 12. 升级与兼容

- `channel` 未设置且 `enabled=true` → 回填为 `lark`，行为与升级前一致
- `channel` 未设置且 `enabled=false` → `off`
- 存量工单的 `ExternalTaskID` 没有 `tg:` 前缀，走原有的魔方清理路径，不受影响
- 迁移只加列和新表，不改任何现有列，可与旧二进制共存一次（新列为 NULL 时旧代码读不到
  也不用到）—— 但仍遵守 README 的顺序：备份 → 停旧进程 → migrate → 起新进程

## 13. 明确不做

- **多实例 polling**：不做分布式锁 / 选主。多实例请用 webhook，文档写明。
- **Telegram 里发起工单**：只做决定，不做提单。提单要带上实例、库、SQL 正文和 MFA，
  聊天窗口不是合适的入口。
- **富交互**：不做"驳回时填理由"的多轮对话。驳回不强制填理由（与控制台一致），理由
  为空即可。
- **消息更新的实时同步**：控制台里批准后，Telegram 卡片只在超时清理和下一次点击时更新，
  不做主动轮询同步。
- **群成员即审批人**：不提供"群里所有人都能批"的降级选项。§1 就是为了排除它。

## 14. 实现后要补的文档

- `docs/adr/` 新增一条 ADR（本仓库的惯例是架构决策进 ADR，这份 spec 是过程文档）
- `docs/external-approval-setup.md` 增加 Telegram 一节：建 bot、拿 chat id、两种模式
  怎么选、绑定流程怎么跟用户讲
- README 的「审批」与「系统设置」两节同步
