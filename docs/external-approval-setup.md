# 外部飞书审批(审批魔方)启用手册

把高危命令审批从站内点单升级为「审批魔方推飞书交互卡片 → 人在卡片上批 → 回调驱动网关执行」。
站内审批仍作兜底,两条通道由原子认领保证同一单只被决策一次。

- 设计与决策:`docs/adr/0003-external-lark-approval-integration.md`
- 这些都是**运行时设置**(存库、经界面配置),不是部署期环境变量。

## 1. 前置条件

- 一个可用的审批魔方(`approval.bssrvc66.com` 类)账户,拿到:
  - 服务地址 `baseURL`(**必须 HTTPS**,Bearer 走明文有中间人风险)
  - 调用令牌 `token`(Bearer)
  - 目标 AI 分组 `ai_group`
- 网关的**回调根地址**必须能被审批魔方主动访问到(公网或内网互通),用于接收审批结果回调。
- 约定语义(厂商已确认):HTTPS 可用;拒绝也回调(`approved:false`);不强制多人审批,任一审批人通过即生效。

## 2. 配置项(设置 › 审批 › 外部飞书审批)

| 设置键 | 含义 | 备注 |
|---|---|---|
| `approval.external.enabled` | 总开关 | 默认 `false` |
| `approval.external.baseURL` | 审批魔方服务地址 | 必须 https |
| `approval.external.token` | 调用令牌(Bearer) | **加密落库**,界面不回显;留空=保持原值 |
| `approval.external.aiGroup` | AI 分组 `ai_group` | |
| `approval.external.callbackBaseURL` | 网关回调根地址 | 完整回调地址 = `<根地址>/api/v1/approvals/lark/callback` |
| `approval.external.callbackSecret` | 回调密钥 `X-Callback-Secret` | **加密落库**,界面不回显;**必填**(未配则一律拒绝回调) |
| `approval.external.callbackAllowIPs` | 回调来源 IP 白名单 | 可选,逗号分隔 IP/CIDR;留空则仅凭密钥校验 |

> `token` / `callbackSecret` 以 AES 加密存储,`GET /settings` 永不回传明文;界面「已配置 · 留空保持不变」即表示已有值。

## 3. 启用步骤

1. 登录管理员账户 → **设置 › 审批 › 外部飞书审批**,打开开关。
2. 填写:服务地址、令牌、AI 分组、回调根地址、回调密钥(可选:来源 IP 白名单)。
3. 面板会实时显示**完整回调地址**,把它登记到审批魔方(作为该调用方的 `callback_url` 目标 / 回调白名单)。
4. 保存。

## 4. 灰度与验证

- 建议先只在 **dev / gli** 环境验证闭环(先把这些实例的连接配好、开关打开)。
- 验证一条:在这些环境对一条会触发审批的高危命令下发(如 `DROP TABLE ...`):
  1. 命令被拦截,生成审批单;
  2. 审批魔方在飞书推出交互卡片;
  3. 在卡片上点「通过」→ 审批魔方回调网关 → 命令被网关代执行、审计留痕、发起人收到通知;
  4. 点「拒绝」→ 审批单置 rejected、不执行。
- 闭环无误后再放开 staging / prod。

## 5. 安全说明

- **回调是能触发生产执行的高危入口**,鉴权 fail-closed:未配 `callbackSecret` 则拒绝一切回调;`X-Callback-Secret` 常量时间比较;可选来源 IP 白名单。
- **禁自审兜底**:审批魔方不强制「非发起人审批」,网关侧会比对回调里的审批人与发起人账户——若审批人全部就是发起人本人,则按 `approval.allowSelfApprove`(默认关)处理,默认拒绝并置 rejected,SoD 不因外部对接降级。
- **幂等**:同一单重复回调只返回当前状态,不会重复执行;站内审批与外部回调竞争时只有一方生效。
- 令牌/密钥加密落库;出站调用沿用 SSRF 策略(`webhook.allow_private`)与禁重定向的受控客户端。

## 6. 故障排查

| 现象 | 排查 |
|---|---|
| 卡片没推出来 | 开关是否开;`baseURL`/`token`/回调根地址是否都填(缺一则不外发);看服务日志 `external approval dispatch failed` |
| 回调返回鉴权失败 | `callbackSecret` 是否与登记给厂商的一致;是否配了 IP 白名单但来源 IP 不在其中 |
| 回调提示审批单不存在 | 关联键是回调里的 `external_task_id`(= 网关 `ApNo`),确认厂商原样带回了我们发的 `external_task_id`/`request_id` |
| 点了通过但没执行 | 是否命中禁自审(审批人=发起人且 `allowSelfApprove` 关);目标连接是否已被删除(会如实记 warn 不执行) |

## 7. 后续(Phase 2,未启用)

执行结果 `/reply` 回帖飞书、内部超时 `PATCH` 回写 cancel——待厂商确认回传的 `om_` message_id 能否命中原卡片后再做,见 `.scratch/lark-approval-integration/issues/04-*`。
