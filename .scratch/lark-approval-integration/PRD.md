# PRD · 外部飞书审批(审批魔方)对接

Status: ready-for-agent

> 设计基线:`docs/adr/0003-external-lark-approval-integration.md`(含真实回调报文、字段映射、端点逻辑、禁自审兜底)。
> 实现拆单见本目录 `issues/01..04`。

## Problem Statement

高危命令审批目前只能在**站内审批页**点通过/拒绝;飞书侧仅有单向通知卡片,审批人必须登录网关控制台才能处理,链路长、响应慢。业务方希望审批人**直接在飞书交互卡片上审批**,结果自动回调驱动网关执行。

## Solution

对接审批魔方(`approval.bssrvc66.com`)作为**外部交互审批 + 回调**提供方,功能开关控制,与站内审批**并存**:

- **出站**:命中高危建单后,`POST /api/v1/approvals` 发起,推飞书交互卡片。`user`=发起人网关账户,`message_id`=网关生成 `gw-<ApNo>`,`external_task_id`=`ApNo`(回调关联主键)。
- **入站**:新增回调端点 `POST /api/v1/approvals/lark/callback`,`X-Callback-Secret` + IP 白名单鉴权;按 `external_task_id` 关联;决策取 `approved` 布尔;`ClaimApproval` 幂等;通过则复用现有执行内核代执行,审计 operator=审批人邮箱。
- **禁自审兜底**:审批魔方不强制非发起人审批 → 网关侧比对 `user` vs `approver`,沿用 `approval.allowSelfApprove`(默认 false),SoD 不降级。
- **对账**:双通道由 `ClaimApproval` 保证单赢;内部超时兜底 + best-effort `PATCH …/status` 回写 cancel。

## Scope

**In**
- `Approval` 加 `ExternalTaskID` / `LarkMessageID` 字段
- 出站发起(功能开关、加密 token、SSRF 复用)
- 回调端点(独立鉴权、幂等、禁自审兜底、复用执行内核)
- 配置项 `approval.external.*`
- httptest 回归(stub 审批魔方 API + 模拟回调)

**Out(后续)**
- Phase 2:执行结果 `POST /api/v1/reply` 回帖飞书(用回传 `om_` message_id)
- Phase 2:内部超时 → `PATCH …/status` 回写 cancel
- 站内审批降级为纯 fallback

## Rollout

功能开关默认关;先在 **dev / gli** env 灰度验证回调闭环,再放开 staging/prod。

## Vendor-confirmed constraints

1. HTTPS 入口可用。
2. 拒绝也回调(`approved:false`)。
3. 不强制多人审批,任一审批人通过即生效。

## Acceptance criteria

- [ ] 高危命令建单后,`approval.external.enabled=true` 时向审批魔方发起,`ExternalTaskID` 落库
- [ ] 回调 `approved:true` → 命令执行、审计 operator=审批人、单据 approved
- [ ] 回调 `approved:false` → 单据 rejected、不执行
- [ ] 错误/缺失 `X-Callback-Secret` 或非白名单来源 → 拒绝
- [ ] 重复回调幂等(第二次只回当前 status,不重复执行)
- [ ] 站内审批与外部回调竞争时,仅一方生效(另一方 `ErrAlreadyDecided`)
- [ ] 审批人集合 ⊆ 发起人本人 且 `allowSelfApprove=false` → 记 rejected、不执行
- [ ] token / callbackSecret 加密落库,不回传明文
- [ ] `go build ./...` / `go vet ./...` 通过;新增 httptest 回归通过
