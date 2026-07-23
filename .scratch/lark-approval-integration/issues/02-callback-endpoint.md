# 02 · 回调端点 + 外部决策路径(复用执行内核)

Status: ready-for-agent

## What to build

入站半程:接收审批魔方回调并驱动现有审批决策/执行内核。

- **路由**:`POST /api/v1/approvals/lark/callback`,**不挂用户 JWT 中间件**,独立鉴权。
- **鉴权**:`X-Callback-Secret` 头与 `approval.external.callbackSecret` **常量时间比较**(`crypto`/`subtle`);叠加**来源 IP 白名单**(复用/参照现有 `middleware.IPAllowlist` 思路,配置化)。缺失/不匹配 → 403。
- **报文解析**(见 ADR 样例):`{task_id, approved(bool), reason, message_id, request_id, external_task_id, question, user, ai_group, approver[], updated_at}`。
- **外部决策路径** `DecideApprovalExternal(externalTaskID, approvers []string, initiatorAccount, approved bool, reason, larkMsgID string)`:
  1. 按 `external_task_id` 查 `Approval`(备用 `request_id` / `task_id`);找不到 → 404。
  2. 非 `pending` → 200 返回当前 `status`(幂等)。
  3. **禁自审兜底**:`approver` 去重后全部等于发起人账户(`Approval.Initiator`/回调 `user`)时,按 `approval.allowSelfApprove`(默认 false):false → 记 rejected(自审被拒)不执行;true → 放行。
  4. `ClaimApproval(pending → approved/rejected)` 原子认领;失败 → `ErrAlreadyDecided`,返回当前状态。
  5. `approved:true` → 复用 `DecideApproval` 的执行内核(`Executor.Run(ap.Command)` + `SetApprovalResult` + 审计),审计 **operator = join(approver)**、备注 = `reason`;存 `LarkMessageID = message_id`。
  6. `approved:false` → 标记 rejected、存 reason。
  7. 通知发起人(复用现有 `notify`)。
- **响应**:`200 { code, msg, status }`(匹配对方 CallbackResponse 习惯:终态回当前 status)。

复用 `DecideApproval` 的 approve/reject 内核:把「认领 + 执行 + 写结果 + 审计 + 通知」抽成可被站内路径与外部路径共用的私有函数,避免逻辑二写。

## Acceptance criteria

- [ ] 正确 secret + 白名单来源 + `approved:true` → 命令执行、单 approved、审计 operator=审批人
- [ ] `approved:false` → 单 rejected、不执行、存 reason
- [ ] 错误/缺失 secret 或非白名单 IP → 403,不改任何状态
- [ ] 未知 `external_task_id` → 404
- [ ] 重复回调 → 第二次 200 返回当前 status,不重复执行(幂等)
- [ ] `approver ⊆ {发起人}` 且 `allowSelfApprove=false` → rejected、不执行
- [ ] 站内 `DecideApproval` 与回调竞争 → 仅一方生效
- [ ] `LarkMessageID` 存库(供 Phase 2)

## Blocked by

01(模型字段 + 配置)

## Comments
