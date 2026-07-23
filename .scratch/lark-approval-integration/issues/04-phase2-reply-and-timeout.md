# 04 · Phase 2 · 结果回帖 + 超时回写(可选/后续)

Status: needs-info

## What to build

Phase 1 闭环稳定后再做,非阻塞。

- **执行结果回帖**:审批通过并执行后,若 `approval.external.replyResult`,调审批魔方 `POST /api/v1/reply`(`{message_id: Approval.LarkMessageID, text: 执行结果摘要}`),把结果回帖到原飞书卡片。
- **超时回写**:内部 `approval.onTimeout` 触发自动驳回时,best-effort 调 `PATCH /api/v1/approvals/{ExternalTaskID}/status` 回写 cancel,让飞书卡片同步收敛。

## Open questions(需厂商/联调确认 → 故 needs-info)

- `/reply` 用回调回传的 `om_...` message_id 能否命中原卡片?(ADR Open items)
- 撤单/取消是否以 `approved:false` 回调,还是需要另处理?
- `PATCH …/status` 的 `approval_status` 取值(旧 schema 为 0/1/2)与出站回调 `approved` 布尔是否一致语义?

## Acceptance criteria

- [ ] `replyResult=true` 时执行后回帖成功(联调验证命中原卡片)
- [ ] 内部超时驳回时向审批魔方回写 cancel(best-effort,失败仅记日志)
- [ ] 上述 open questions 有厂商明确答复后再落实现

## Blocked by

02(回调端点,需 `LarkMessageID`);待厂商确认 open questions

## Comments
