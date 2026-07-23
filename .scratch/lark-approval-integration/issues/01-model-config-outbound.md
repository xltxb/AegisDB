# 01 · 模型字段 + 配置项 + 出站发起

Status: ready-for-agent

## What to build

打通「建单即向审批魔方发起」的出站半程,并加好模型/配置基础。

- **模型**:`model.Approval` 新增 `ExternalTaskID string`(唯一索引)、`LarkMessageID string`。sqlite 走 AutoMigrate;MySQL 加迁移文件(下一个编号)。
- **配置项**(seed + settings,敏感值加密):`approval.external.enabled`(bool,默认 false)、`approval.external.baseURL`、`approval.external.token`(Bearer,加密)、`approval.external.aiGroup`、`approval.external.callbackSecret`(加密)、`approval.external.replyResult`(bool,默认 false)。token/secret 复用 `crypto.EncryptSecret`,读时解密;`GetSettings` 不回传明文(比照 webhook secret 的 `secretsSet` 处理)。
- **出站发起**:在 `createApproval()` 持久化成功后,若 `enabled`,调审批魔方 `POST {baseURL}/api/v1/approvals`(`Authorization: Bearer <token>`),按 ADR 字段表映射(`user`=发起人账户、`message_id`=`gw-<ApNo>`、`external_task_id`=`request_id`=`ApNo`、`callback_url`=网关回调地址、`payload`=结构化)。响应 `task_id` 回写 `Approval.ExternalTaskID`。
  - 异步/best-effort:发起失败**不**能让建单失败(单已建、站内审批仍可用),失败记日志 + 可选审计。
  - 复用现有出站 HTTP 客户端与 `webhook.allow_private` SSRF 策略、超时、Bearer。
- **回调地址来源**:`approval.external.callbackBaseURL`(或复用现有站点根 URL 配置)拼 `/api/v1/approvals/lark/callback`。

## Acceptance criteria

- [ ] `Approval` 新字段迁移在 sqlite(AutoMigrate)与 MySQL(迁移文件)均生效
- [ ] `enabled=true` 时建高危单会 POST 审批魔方,`ExternalTaskID` 落库;`enabled=false` 时不外发
- [ ] token/callbackSecret 加密落库,`GetSettings` 不回传明文
- [ ] 审批魔方不可达/超时不影响建单成功与站内审批
- [ ] `go build ./...` / `go vet ./...` 通过

## Blocked by

None

## Comments
