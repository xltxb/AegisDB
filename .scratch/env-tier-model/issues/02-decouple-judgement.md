# 02 · 判定路径改走 tier + 根治规则漏分层 bug

Status: done

## What to build

把 8 处按 tier 名硬编码的判定改为读 tier 属性位,并根治高危命令写入漏分层的现存 bug。这是本 feature 安全相关的核心单。

### 判定入口:connection → environment → tier

新增一个解析helper(建议 `service` 层,带轻量缓存或直查 —— 现有规则读取**无缓存**,`gateway/risk.go:292` 每次直查 DB,保持一致即可):

```
tierOf(conn) → model.EnvTier
```

所有判定改为先解析出 tier,再用 tier code 查规则表。

### 逐处替换(全部为「换成 tier」)

| 位置 | 现在 | 改为 |
|---|---|---|
| `service/gateway.go:57,117,142,219` | `EvaluateFor(…, conn.Env, …)` | 传 `tierOf(conn).Code` |
| `service/async_exec.go:46`、`service/export.go:138` | 同上 | 同上 |
| `service/gateway.go:704` | `conn.Env != model.EnvProd` | `!tierOf(conn).RequireMFA` |
| `service/gateway.go:562` | `ScanStatement(model.EnvProd, sql)` | 持有 `ScanBaseline` 的 tier code |
| `repository/repository.go:1014` | `tbl_connection.env = 'prod'` | join 到持有 `CountsInPending` 的 tier |

`gateway/risk.go:392,400` 的形参语义由「env」改为「tier code」,改注释与形参名(`env` → `tier`),避免后续再次混淆。

**`gateway.go:562` 是本单最高风险点**:若解析不到 `ScanBaseline` tier,不可 fallback 到空字符串 —— `matchCommand` 找不到任何行会返回 `RiskOff`,导致脚本扫描器对任何脚本都报「无风险」且**无任何报错**。必须走 `unavailableVerdict` 同款处理:读不到就拒绝/报错,不静默放行(比照 `gateway/risk.go:429` ED3 的既有立场)。

### 根治 UpsertRiskCommand 漏分层

**现状 bug**:`views/RiskRulesView.vue:132` 新增命令只传 `{prod, staging, dev}`(漏 gli),而 `repository.go:542` 只写入 map 里给出的 key → 新增的高危命令对灰度实例完全不生效。

**改法**:写入路径由服务端按当前**全部** tier 展开,不再信任前端传来的 map。

- `Repo.UpsertRiskCommand(command, levels)`:先查全部 tier,对每个 tier 取 `levels[tierCode]`,缺失则用默认等级补齐,保证**一次 upsert 后该命令在每个 tier 都有行**
- 默认等级取值见 PRD「待定」项,建议:持有 `ScanBaseline` 的 tier 用 `high`,`RequireMFA` 的 tier 用 `high`,其余 `off`
- `PatchRiskLevel`(`repository.go:554`)保持单格更新语义不变

同理检查能力矩阵的写入路径,确认新增角色时会为全部 tier 建行。

## Acceptance criteria

- [x] 新增 tier 后,该 tier 下的实例立即受完整管控(能力矩阵 + 风险字典均命中,非 allow/off 兜底)
- [x] `RequireMFA=false` 的 tier 下实例不再强制 MFA;`true` 的强制 —— 且与 tier **名字**无关
- [x] **回归用例**:后台新增一条高危命令后,查库确认该命令在**每个** tier 都有行(复现并锁死 gli bug)
- [x] 删除/切换 ScanBaseline 后,脚本扫描仍按新基准工作
- [x] **负向用例**:强制构造「解析不到 ScanBaseline tier」的场景,断言脚本扫描**报错而非返回全部无风险**
- [x] 待审批统计按 `CountsInPending` 属性聚合,不再按 'prod' 字面量
- [x] 现有 `connection_policy_test.go:73`(遍历四个 env)等测试全绿
- [x] `go build ./...` / `go vet ./...` / `go test ./...` 通过

## Blocked by

01(需要 tier 表与属性位)

## Comments

**2026-08-06 · 实现完成**

- 解析入口 `Services.tierOf(conn)` / `tierCodeOf(conn)`(`service/envtier.go`)。**直查 DB 不加缓存**,与 `gateway/risk.go` 每次直查规则一致;加了缓存会出现「规则改动立即生效、属性位要等失效」的错位。
- `gateway/risk.go` 形参 `env` → `tier`(`matchCommand` / `capabilityLevelUnion` / `Evaluate` / `EvaluateRoles` / `EvaluateFor` / `ScanStatement`),注释写明查表键是分层标签不是环境。
- 新增导出的 `gateway.Unavailable(engine, sql, err)`,让 service 层「tier 解析不出来」时复用 ED3 的拒绝口径,而不是自己造 verdict。
- 8 处判定改造:`RiskCheck` / `strictestVerdict` / `execJudged` / `SubmitScriptForApproval` / `async_exec` / `export` 全部先解析 tier,解析失败一律拒绝;`checkMFA` 改读 `RequireMFA`(解析失败按「要求 MFA」处理,失败方向朝严);`ScanScript` 改读 `ScanBaselineTier()` 且**签名加 error**,handler 两处改为报错而非返回空扫描结果。
- `CountProdInterceptions` 改 join 到 `counts_in_pending`,不再比 `env = 'prod'` 字面量。
- **gli bug 根治**:`UpsertRiskCommand` 改为服务端按 `tbl_env_tier` 全量展开,前端传的 map 只作取值来源;顺带修了原实现不 normalize key(`"PROD"` 会另起一行)的隐患。
- 测试:`bootstrap/tier_judgement_test.go`(8 个)。

### 需要你拍板的一处默认值

PRD「待定」里「新增高危命令时各 tier 的默认等级」我按 02 的建议实现为 `defaultRiskLevel`:持有 `ScanBaseline` 或 `RequireMFA` 的 tier 默认 `high`,其余 `off`。理由是猜错方向的代价不对称 —— 多一次审批 vs 刚被标为高危的命令在生产 tier 上免审执行。若你想改成「取 ScanBaseline tier 的等级」,只需改 `repository.go` 的 `defaultRiskLevel`。

### 本地联调时发现并修掉的一个缺陷(2026-08-06)

`Connection.Layer` / `DefaultRole` 是**从 tier 派生但存在行上**的值,只在建/改连接时写入。分层模型让它有了三条新的失效路径:改 tier 的 `conn_layer`、环境改绑到别的 tier、删环境导致实例迁移到别的 tier —— 这三种都不碰 connection 行,控制台就会一直显示一个**已经不再管辖该实例的分层**的层级名。

改法:不信任存下来的值,在控制台唯一的实例读取出口 `AccessibleConnections` 里按当前 tier 重算(`withTierDefaults`)。列仍保留,作为环境解析不到时的兜底 —— 那时没有 tier 可问,最后已知值好过空白。这两个字段不参与任何鉴权(判定自己解析 tier),所以只是展示正确性问题。

回归:`TestTierDefaults_FollowTheTierRatherThanTheStoredCopy` 覆盖上述三条路径。

### 一处与工单描述不符的核对结果

工单要求「确认新增角色时会为全部 tier 建行」。实际本仓库**没有角色新增接口**(角色只在 seed 里定义,路由只有 PATCH/PUT),而新建 tier 时 `CreateEnvTierFrom` 会克隆全部能力矩阵行,所以这条不存在缺口,`SetMatrix` 未改。
