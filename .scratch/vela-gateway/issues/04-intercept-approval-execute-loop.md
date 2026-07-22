# 04 · 高危拦截 → 审批 → 代执行 闭环

Status: ready-for-agent

## What to build

打通高危命令的完整闭环。DBA 在 PROD 输入高危命令（如 `DROP TABLE`）→ 风险引擎拦截 → 终端内嵌"已拦截"提示 → 弹出提交卡（必填操作原因）→ 生成审批单（`AP-xxxx`）+ 审计 ID → 审批人在待办通过/拒绝 → 通过后由网关代执行并回显，全程审计留痕。同一命令在 DEV 直接放行、在 PROD 拦截，体现环境分层差异。

- 后端：风险引擎补全 `ActionApprove` / `ActionDeny` 路径；严格模式——无 WHERE 的 DELETE/UPDATE 强制升级为 high。`tbl_approval(ap_no, connection_id, env, instance, command, initiator_id, reason, risk_level, status, audit_id, ...)` + `tbl_approval_step(approval_id, step_order, approver_id, status, ...)`。命中高危时 `/risk/check` 返回错误码 `42200` + `ap_no`。端点 `GET /approvals?scope=mine|all`、`POST /approvals/:id/approve`、`POST /approvals/:id/reject`；通过后网关代执行原命令并写审计（executed），拒绝写审计（rejected）。
- 前端：终端 InterceptModal（展示待执行命令/环境/实例/风险标签，必填操作原因 → 提交审批）；`/approvals` 页 + `approvals` store（list mine/all、approve、reject、submit）；卡片展示命令/发起人/目标/原因/审批链，通过/拒绝乐观更新。

## Acceptance criteria

- [ ] PROD 输入 `DROP TABLE` 被拦截，终端显示拦截块，`/risk/check` 返回 42200 + ap_no（FR-TERM-03 / FR-APPR-01）
- [ ] 提交卡强制填写原因，生成审批单（AP-xxxx）与审计 ID（FR-APPR-02/03）
- [ ] 同一命令在 DEV 直接放行执行、PROD 进入审批（环境分层差异验收点）
- [ ] 审批待办支持"待我审批/全部"，可通过/拒绝；通过后由网关代执行并回显（FR-APPR-04）
- [ ] 严格模式开启时，无 WHERE 的 DELETE/UPDATE 被拦截
- [ ] 被拦截、被拒绝、被执行的命令均写入审计

## Blocked by

- 03（风险引擎放行通路 + 审计 + 终端）
