# PRD · 外部飞书审批(审批魔方)对接

Status: done（Phase 1 全部落地；Phase 2 的超时回写已做、`/reply` 回帖未做）

> 设计基线:`docs/adr/0003-external-lark-approval-integration.md`(含真实回调报文、字段映射、端点逻辑、禁自审兜底)。
> 实现拆单见本目录 `issues/01..04`;启用手册见 `docs/external-approval-setup.md`。
>
> **2026-09-11 以代码为准校准**:Problem Statement 保留原始意图;Solution / Scope / Acceptance criteria 已按实际交付重写,
> 与原稿不一致处集中在「验收校准」。**最重要的一条**:回调**不再驱动执行**(ADR 0010 推翻了"批了就跑"),
> 它只把工单置为 approved / rejected。

## Problem Statement

高危命令审批目前只能在**站内审批页**点通过/拒绝;飞书侧仅有单向通知卡片,审批人必须登录网关控制台才能处理,链路长、响应慢。业务方希望审批人**直接在飞书交互卡片上审批**,结果自动回调驱动网关执行。

## Solution

对接审批魔方(`approval.bssrvc66.com`)作为**外部交互审批 + 回调**提供方,功能开关控制,与站内审批**并存**:

- **出站**:命中高危建单后,`POST /api/v1/approvals` 发起,推飞书交互卡片。`user`=发起人网关账户,`message_id`=网关生成 `gw-<ApNo>`,`external_task_id`=`ApNo`(回调关联主键)。
  出站范围比原稿宽:终端/脚本的高危命令之外,**执行窗口申请、含敏感字段的导出、发布单的审批阶段**同样外发。
  payload 除环境外还带 `tier`(分层),命令与口令全量脱敏。
- **入站**:回调端点 `POST /api/v1/approvals/lark/callback`,鉴权用 `Authorization: Bearer <callbackSecret>`
  (或 `?secret=`,常量时间比较)+ 可选 IP 白名单 —— **不是原稿写的 `X-Callback-Secret` 头**;按 `external_task_id` 关联;
  决策取 `approved` 布尔;`ClaimApproval` 幂等;**通过只把工单置为 approved,不执行任何命令**,
  由有权者回控制台点执行(ADR 0010);审计 operator=审批人邮箱(截断 128 字节)。
- **禁自审兜底**:审批魔方不强制非发起人审批 → 网关侧比对 `user` vs `approver`,沿用 `approval.allowSelfApprove`(默认 false),SoD 不降级。
- **额外闸门**:总开关关闭时回调端点一并拒绝(不靠残留密钥存活);本地已记录厂商 `task_id` 时,回调必须带且匹配,否则 `40300`。
- **对账**:双通道由 `ClaimApproval` 保证单赢;内部超时(`approval.onTimeout=auto-reject`)与发布单终止作废审批时,
  向审批魔方 `PATCH …/status` 回写 cancel —— 这条原列为 Phase 2,**已实现**。
- **与飞书群通知的区别**:「设置 › 通知」里的 `notify.lark*` 是另一套东西 —— 只往自定义机器人推一张
  「有新审批单」的卡片(可带控制台回链、可 HMAC 签名),不接收回调、不影响审批结果。两套可同时开。

## Scope

**In(已交付)**
- `Approval` 加 `ExternalTaskID` / `LarkMessageID` 字段
- 出站发起(功能开关、加密 token、SSRF 复用、脱敏)
- 回调端点(独立鉴权、幂等、禁自审兜底、总开关联动、task_id 交叉校验)
- 配置项 `approval.external.*`(7 项,token 与 callbackSecret 加密落库不回显)
- 超时 / 作废时 `PATCH …/status` 回写 cancel(原 Phase 2)
- httptest 回归(stub 审批魔方 API + 模拟回调 + wire 级脱敏断言)

**Out(仍未做)**
- 执行结果 `POST /api/v1/reply` 回帖飞书(用回传 `om_` message_id)——待厂商确认能否命中原卡片
- 站内审批降级为纯 fallback(两条通道目前平等并存)
- 回调侧的重放保护(依赖密钥 + IP 白名单,无 nonce/时间戳)

## Rollout

功能开关默认关;先在 **dev / gli** env 灰度验证回调闭环,再放开 staging/prod。

## Vendor-confirmed constraints

1. HTTPS 入口可用。
2. 拒绝也回调(`approved:false`)。
3. 不强制多人审批,任一审批人通过即生效。

## Acceptance criteria(按实际交付重述)

- [x] 高危命令建单后,`approval.external.enabled=true` 时向审批魔方发起,`ExternalTaskID` 落库
- [x] 回调 `approved:true` → 单据 approved、审计 operator=审批人;**不执行命令**,执行由有权者在控制台发起(ADR 0010)
- [x] 回调 `approved:false` → 单据 rejected、不执行
- [x] 错误 / 缺失回调密钥(`Authorization: Bearer` 或 `?secret=`)或非白名单来源 → 拒绝
- [x] 重复回调幂等(第二次只回当前 status)
- [x] 站内审批与外部回调竞争时,仅一方生效(另一方 `ErrAlreadyDecided`)
- [x] `token` / `callbackSecret` 加密落库,不回传明文
- [x] 超时自动驳回与发布单终止时,向审批魔方回写 cancel
- [x] `go build ./...` / `go vet ./...` 通过;httptest 回归通过
- [ ] 审批人集合 ⊆ 发起人本人 且 `allowSelfApprove=false` → 记 rejected、不执行
      —— **有缺口**:`approver` 为空数组时当前直接放行(见「验收校准」)

## 验收校准(原稿 vs 实际交付)

| 原稿说法 | 实际交付 |
|---|---|
| 回调鉴权用 `X-Callback-Secret` 头 | 改为 `Authorization: Bearer <callbackSecret>`,也接受 `?secret=`;常量时间比较 |
| 回调 `approved:true` → **命令执行** | ADR 0010 推翻:回调只置 approved,执行由有权者在控制台点「执行」 |
| Phase 2:内部超时 `PATCH` 回写 cancel | **已实现**(`cancelExternalApproval` / `PatchExternalStatus`) |
| 出站只覆盖"高危命令建单" | 还覆盖执行窗口申请、敏感字段导出、发布单审批阶段;payload 多一个 `tier` 字段 |
| 回调错误用 HTTP 403 / 404 | 一律 HTTP 200 + 业务码(与全站信封一致),厂商侧需按包体判断 |
| 禁自审兜底覆盖全部情形 | `approver` 为**空数组**时视为"无法判定"直接放行,SoD 落空且审计无审批人 —— 已建 GitHub issue,应改为拒绝 |
| 拒绝时记录 `reason` | `cb.Reason` 未落库,拒绝原因丢失 —— 已建 GitHub issue |
| `baseURL` / 回调根地址强制 HTTPS(ADR 0003) | 代码与设置页均不校验协议,`http://` 可保存 —— 已建 GitHub issue |
