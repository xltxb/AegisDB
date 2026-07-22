# 09 · Webhook 外推（持久化 + HMAC 签名 + 指数退避重试 + 测试事件）

Status: ready-for-agent

## What to build

管理员配置 Webhook 把网关事件外推到 SIEM/IM：可编辑推送地址、签名密钥、订阅事件（拦截/审批/高危执行/登录）、重试与启停并落库；事件发生时按 HMAC-SHA256 签名投递，失败按指数退避重试；可发送测试事件验证连通性并查看投递结果。

- 后端：`tbl_webhook_config(endpoint, secret, events, retry_max, enabled)`；`GET/PUT /settings/webhook` 配置持久化；事件分发器在 04 拦截/审批/执行与 01 登录处挂钩，发送 `X-Vela-Signature = HMAC-SHA256(secret, body)`，失败 `backoff(i)=2^i` 秒重试至 `retry_max`；`POST /settings/webhook/test` 发送测试事件并返回投递结果。
- 前端：系统设置内 Webhook 区——推送地址/签名密钥/事件订阅（多选）/重试/启停编辑保存；测试按钮 + 成功/失败提示与投递日志。

## Acceptance criteria

- [ ] Webhook 配置（地址/密钥/事件/重试/启停）可编辑并落库（FR-AUD-03）
- [ ] 订阅事件触发时实际投递，请求头含 `X-Vela-Signature`（HMAC-SHA256）
- [ ] 投递失败按指数退避重试，最多 `retry_max` 次
- [ ] 发送测试事件可验证连通性并查看投递结果（FR-AUD-04）
- [ ] 关闭启停开关后不再投递

## Blocked by

- 04（拦截/审批/执行事件源）
