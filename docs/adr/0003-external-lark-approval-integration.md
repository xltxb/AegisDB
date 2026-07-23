# ADR-0003 · 外部飞书审批(审批魔方)对接

Status: Accepted(设计已定,实现待 Phase 1)
Date: 2026-07-23
Supersedes: 无
Related: `.scratch/lark-approval-integration/`(PRD + 实现工单)

## Context

现有高危命令审批为**站内决策**:命中高危 → `createApproval()` 建单 → 审批人在站内审批页点通过/拒绝(`DecideApproval`)→ 网关代执行。飞书侧当前只有**单向通知卡片**(`SendLarkApproval` 推到 `notify.larkWebhook`),不能在卡片上审批、也无回调。

需求:把审批升级为「审批魔方推飞书**交互卡片** → 人在卡片上批 → **回调**驱动网关执行」,同时**保留站内审批作为兜底**。

对接方为审批魔方网关(`approval.bssrvc66.com`,OpenAPI)。厂商已确认的约束:

1. 提供 **HTTPS** 入口(Bearer/回调不走明文)。
2. **拒绝也回调**(`approved:false`)。
3. **不强制多人审批**:一组多个审批人,**任一人通过即生效**(OR 语义,非会签)。

## Decision

引入审批魔方作为**外部交互审批 + 回调**提供方,**功能开关**控制,与站内审批**并存**;两条通道最终都汇聚到现有 `ClaimApproval` 原子决策内核,保证同一审批单只被决策一次。

### 数据模型

`model.Approval` 新增:

- `ExternalTaskID string`(审批魔方返回的 `task_id`;回调主关联键用我方回传的 `external_task_id`,见下)
- `LarkMessageID string`(回调带回的真实飞书消息ID `om_...`,供 Phase 2 `/reply` 回帖)

### 出站:发起审批

`createApproval()` 成功后,若 `approval.external.enabled`,`POST {baseURL}/api/v1/approvals`(Bearer):

| 对方字段 | 我方来源 |
|---|---|
| `user` | **发起人网关账户**(`Approval.Initiator` / 发起人 email) |
| `message_id` | **网关生成的确定性ID `gw-<ApNo>`**(派生自 ApNo → 重试幂等,对方若按 message_id 去重则不重复建单) |
| `messages` | `[{role:"user", content:"<env>/<instance>/<db> 高危命令待审批:<command> · 原因:<reason> · 风险:<risk>"}]` |
| `ai_group` | 配置 `approval.external.aiGroup` |
| `callback_url` | `https://<gateway>/api/v1/approvals/lark/callback` |
| `external_task_id` | 我方 `ApNo`(**回调关联主键**) |
| `request_id` | 我方 `ApNo`(备用关联键) |
| `payload` | `{env,instance,database,command,risk,initiator,reason,apNo}` |

响应 `task_id` → 写回 `Approval.ExternalTaskID`。

### 入站:回调端点

`POST /api/v1/approvals/lark/callback`(**不走用户 JWT 中间件**,独立鉴权)。

真实回调报文样例(厂商确认):

```json
{
  "task_id": "k8s_20260706_001",
  "approved": true,
  "reason": "同意",
  "message_id": "om_xxxxxxxxxxxx",
  "request_id": "19d9b884-b463-4865-b74a-69c8238973bf",
  "external_task_id": "a5d77756-de70-4053-8d36-7e2b2abecd98",
  "question": "变更 K8S 重启 ABC 服务 pod，业务异常",
  "user": "pax",
  "ai_group": "K8S_AI",
  "approver": ["herbert@tbu.net"],
  "updated_at": "2026-07-06T09:15:42.123Z"
}
```

字段处理:

| 字段 | 处理 |
|---|---|
| `approved`(**bool**) | 决策:`true→通过并执行`,`false→拒绝`(不再用 0/1/2) |
| `external_task_id` | **主关联键**(= 我方 ApNo);找不到 → 404 |
| `request_id` / `task_id` | 备用关联键 |
| `approver`(**数组**) | 审计「操作人」= `join(approver)` |
| `reason` | 写入审计 / 审批结果备注 |
| `message_id`(`om_...`) | 存 `Approval.LarkMessageID`(供 `/reply`) |
| `updated_at` | 决策时间 |

端点逻辑:

```
1. 校验 X-Callback-Secret(常量时间比较) + 来源 IP 白名单
2. 按 external_task_id 查 Approval;非 pending → 200 返回当前 status(幂等)
3. 【禁自审兜底】approver 全部 ∈ {发起人本人} 时,按 approval.allowSelfApprove 决定:
     false(默认)→ 记 rejected(自审被拒),不执行
     true         → 放行
4. ClaimApproval(pending → approved/rejected)  // 原子,防重放/双通道竞争
5. approved:true → 执行 ap.Command(复用现有执行内核 + 审计;operator=join(approver))
   approved:false → 标记 rejected,存 reason
6. 返回 200 { code, msg, status }
```

### 安全

- **HTTPS 强制**:出站与回调地址均 https。
- **鉴权**:出站 Bearer token、回调 `X-Callback-Secret` 均**加密落库**(复用 `crypto.EncryptSecret`);回调再加**来源 IP 白名单**。
- **幂等/防重放**:`ClaimApproval` 天然幂等;终态单二次回调只回状态不重复执行。
- **SSRF**:出站沿用现有 `webhook.allow_private` 策略与超时/重试。
- **禁自审兜底(关键)**:审批魔方不强制非发起人审批 → 网关侧用回调里的 `user`(发起人)与 `approver` 比对,沿用现有 `approval.allowSelfApprove`(默认 false)语义,**SoD 不因对接降级**。

### 配置项(settings,加密存敏感值)

- `approval.external.enabled`(bool,总开关)
- `approval.external.baseURL`(https)
- `approval.external.token`(Bearer,加密)
- `approval.external.aiGroup`
- `approval.external.callbackSecret`(X-Callback-Secret,加密)
- `approval.external.replyResult`(bool,Phase 2:是否把执行结果 `/reply` 回帖)

### 生命周期对账

- **双通道**:站内审批 + 外部回调都能决策,`ClaimApproval` 保证单赢,另一方得 `ErrAlreadyDecided`。
- **超时**:内部 `approval.onTimeout` 扫描仍作兜底;内部自动驳回时 **best-effort `PATCH /api/v1/approvals/{task_id}/status` 回写 cancel**,让飞书卡片收敛。
- **连接已删**:沿用现有「不执行、如实记 warn」。

## Consequences

**收益**
- 审批人可直接在飞书卡片上处理,无需登录网关控制台;结果自动驱动执行。
- 复用现有 `ClaimApproval`/执行/审计内核,改动集中在「出站发起 + 入站回调 + 两个模型字段」。
- 真实 `om_` message_id 经回调回传,`/reply` 回帖(Phase 2)有据可依。

**代价 / 风险**
- 决策权部分外移;两人复核依赖厂商 + 网关侧禁自审兜底(已缓解)。
- 回调端点是能触发生产执行的高危入口,鉴权(secret + IP 白名单)不可省。
- `message_id` 我方发 `gw-<ApNo>`、回调回 `om_...`,**不能用 message_id 做关联**(用 `external_task_id`)。

## Alternatives considered

- **自建飞书交互卡片 + 回调**:需自管飞书机器人权限、卡片协议、验签,工作量与维护成本更高;审批魔方已封装。
- **仅保留站内审批**:满足不了「飞书卡片上审批」的诉求。

## Open items(实现期确认,非阻塞)

- 撤单/取消是否以 `approved:false` 表达(当前按拒绝处理)。
- `/reply` 用回传 `om_` message_id 能否命中原卡片(Phase 2 落地时验证)。
