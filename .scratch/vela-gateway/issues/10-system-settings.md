# 10 · 系统设置（网关 / 审批超时 / 安全 / 通知 / 外观语言）

Status: ready-for-agent

## What to build

管理员在 `/settings` 配置全局策略并往返持久化：网关策略（默认策略/严格模式/执行超时）、审批（超时处理 auto-reject/auto-escalate/keep-waiting、默认审批人、超时升级）、会话与安全（会话有效期/空闲锁定/强制 MFA/IP 允许列表）、通知（Lark 频道/邮件/拦截即时通知）、外观与语言（中文/English、深/浅主题，与顶栏联动）。后台定时扫描逾期审批单按设置处理。

- 后端：`tbl_setting(k, v)` JSON KV；`GET /settings`、`PUT /settings`。审批超时定时任务扫描 `tbl_approval_step`，按设置 auto-reject / auto-escalate / keep-waiting 处理逾期单（FR-APPR-05）。严格模式/默认策略与 03/05 引擎共用。
- 前端：`/settings` 页 + `settings` store（gateway/approval/security/notify + fetch/save）。各分区往返持久化；语言/主题切换与顶栏联动并写设置；数据类内容（实例名/SQL/人名/邮箱/IP）不翻译。

## Acceptance criteria

- [ ] 网关策略（默认策略/严格模式/执行超时）保存并对引擎生效（FR-SET-01）
- [ ] 审批超时处理可配置，定时扫描按设置自动驳回/升级/保持等待逾期单（FR-SET-02 / FR-APPR-05）
- [ ] 会话与安全项（有效期/空闲锁定/强制 MFA/IP 允许列表）可配置保存（FR-SET-03）
- [ ] 通知项（Lark 频道/邮件/拦截即时通知）可配置保存（FR-SET-04）
- [ ] 语言/主题切换与顶栏联动并持久化；数据类内容保持原文不翻译（FR-SET-05 / FR-I18N-01/02）

## Blocked by

- 04（审批超时处理依赖审批单与审批步骤）
