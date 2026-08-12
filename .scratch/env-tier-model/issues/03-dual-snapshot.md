# 03 · 审计/审批双快照 + 展示层归属改环境

Status: done(代码完成;上线前需确认下游 webhook 契约,见 Comments)

## What to build

把「归属/展示」这一半调用点改为 environment,并给历史记录补上 tier 快照,使审计链能解释「当时为何按这个等级管控」。

### 双快照

`model.Approval` 现有 `Env string`(`model.go:248`)语义变更并扩展:

- `Env` → 存 **environment code**(如 `prod-hk`),回答「在哪里执行的」
- 新增 `TierCode string` → 存**执行当时**的 tier code,回答「当时按什么等级判的」

审计日志同理(检查 `tbl_audit_log` 是否也需要同样两列)。

**不可变原则**:两个快照写入后**永不回溯修改**。日后 environment 改绑其他 tier,或实例切换 environment,历史记录保持原值 —— 改动历史即伪造审计链。

旧数据的 `TierCode` 留空;展示层遇空值时按 `Env` 反查当前绑定并标注为推断值(见 PRD Out of scope)。

### 展示/归属改 environment

| 位置 | 处理 |
|---|---|
| `service/gateway.go:242` | `Approval.Env = conn.Env`(已是 environment code)+ 新增 `TierCode = tierOf(conn).Code` |
| `service/async_exec.go:63`、`service/export.go:151` | `conn.Env + "-" + conn.Name` 保持不变,天然变为 `prod-hk-tongcha` |
| `service/webhook.go:147` | payload `"env"` 保持送 environment code;**新增** `"tier"` 字段 |
| `service/webhook.go:438` | 测试卡片样例数据同步更新 |

### 契约影响

`webhook.go:147` 的 payload 是**对外契约**(飞书审批魔方消费)。新增 `tier` 字段是向后兼容的加法,但 `env` 字段的取值范围会从四个固定值扩大为任意 environment code —— 需确认下游是否对该字段做过枚举校验。若做过,此项需先与对接方确认再上线。

## Acceptance criteria

- [x] 新建审批单同时落 environment code 与 tier code 两个快照
- [x] 把某 environment 改绑到另一个 tier 后,**历史审批记录的 tier 快照不变**
- [x] 实例切换 environment 后,历史记录的 environment 快照不变
- [x] 旧数据(tier 快照为空)在审批页与审计页能正常渲染,不报错
- [x] webhook payload 含 `env` 与 `tier` 两字段,现有订阅方不受破坏
- [x] 实例显示名带上 environment(`prod-hk-tongcha`)—— 见下方核对结果,原验收描述前提有误
- [x] `go build ./...` / `go vet ./...` / `go test ./...` 通过

## Blocked by

02(需要 `tierOf` 解析)

## Comments

对外契约变更(webhook payload 的 `env` 取值范围扩大)需要人确认下游影响,故本单倾向 `ready-for-human` 而非 AFK。

**2026-08-06 · 实现完成**

- `model.Approval` 加 `TierCode`;`Env` 由 size16 放宽到 32(environment code 宽度)
- `model.AuditLog` **同时加 `Env` 与 `TierCode`** —— 原来审计行连 env 都没有,只记了「判为 high」却没记「按什么等级判的」。两列都并入链哈希 payload,否则快照可被改而不断链,等于没有证据效力。老行按旧 payload 形状哈希、保持原值不重算(与 0008 `operator` 列的先例一致,仓库内没有重算校验器)
- `appendAudit` 在**取 auditMu 之前**解析 tier(锁内做 DB 读会拖住全进程审计写入);解析失败留空而不是失败,丢审计行是更坏的结果
- `createApproval` 同理落双快照
- webhook payload 加 `"tier"`,`TestLark` 样例卡同步
- 迁移 `0013_dual_snapshot.sql`
- 测试:`bootstrap/dual_snapshot_test.go`(7 个),核心是三条「值**没有**变」的断言

### 上线前需要你确认(唯一遗留项)

`webhook.go` payload 的 `env` 字段取值范围已从四个固定值扩大为任意 environment code。`tier` 是新增字段,向后兼容;**风险只在 `env`**:若审批魔方对该字段做过枚举校验,新建环境后的审批单会被下游拒收。需与对接方确认后再上线。代码里已在 payload 处留注释指回本单。

### 两处与工单描述不符的核对结果

1. **实例显示名的验收条件前提有误**。原文举例 `prod-hk-tongcha` vs `prod-sh-tongcha`,但 `Connection.Name` 有全局唯一索引(`model.go:164` `idx_connection_name`),两个环境**不可能**有同名实例,显示名本来就不会撞。实际改进是显示名从 `prod-tongcha` 变成 `prod-hk-tongcha`,即标明具体集群。测试按真实约束重写。

2. **`tbl_connection.env` 列宽是个真 bug**。它是 `size:16`,而 environment code 允许 32 位。超长 code 在 MySQL 上会截断或报错,而**截断后的 code 解析不到 environment → 解析不到 tier → 无规则**。已在模型与 0013 迁移里一并放宽,并加了 31 字符 code 的往返测试。本单原文没提到这一处。
