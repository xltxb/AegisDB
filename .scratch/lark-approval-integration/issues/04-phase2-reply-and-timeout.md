# 04 · Phase 2 · 超时回写取消

Status: ready-for-human（超时 PATCH 回写 cancel 已交付；/reply 回帖飞书仍未做，待厂商确认 om_ message_id）

## 范围决定(2026-07-24)

- **功能 A(执行结果回帖 `/reply`)已移除**:`/api/v1/reply` 是审批魔方给 AI-Agent 用的转发接口,不是我方所需;执行结果由站内通知告知发起人即可。`om_`/`message_id` 相关一并作废(`Approval.LarkMessageID` 变为未用的调试元数据,可保留或后续清理)。
- **关联键统一为对方 `task_id`**(我方 `Approval.ExternalTaskID`,发起响应带回、回调也带回),不用 `message_id`。
- Phase 2 因此只剩**功能 B:超时回写取消**。

## What was built

- **超时取消**:`SweepApprovalTimeouts` 的 `auto-reject` 分支,认领 pending→expired 成功后,对有 `ExternalTaskID` 的单 best-effort 异步调 `PATCH {baseURL}/api/v1/approvals/{task_id}/status`,body `{task_id, approver:"gateway-timeout", approval_status:2}`(2=cancel),Bearer;复用加密配置 + SSRF 客户端。失败仅记日志。
- 正确性说明:即便不做本功能,幂等也已兜底(超时置 expired 后旧卡片被点→回调查到非 pending→不执行);功能 B 仅让飞书卡片同步收敛(观感)。

## Acceptance criteria

- [x] 外部单被内部超时 auto-reject 时,向审批魔方 `PATCH …/{task_id}/status` 回写 cancel(approval_status=2、Bearer)
- [x] best-effort 异步,失败仅记日志,不影响超时作废本身
- [x] 未配置外部审批 / 无 `ExternalTaskID` 时不外发
- [x] 回归 `TestExternalApproval_TimeoutCancelsExternalCard`;全量 `go test ./...` 通过

## Blocked by

None(功能 A 已移除,不再有 open questions)

## Comments

- 已实现并测试通过。
- 清理:功能 A 移除后遗留的 `Approval.LarkMessageID` 字段(及 migration 0006 的 `lark_message_id` 列、`SetApprovalLarkMessage`)与 `approval.external.replyResult` 配置键已删除。
