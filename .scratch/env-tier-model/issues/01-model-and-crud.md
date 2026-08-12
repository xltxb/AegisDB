# 01 · 数据模型 + tier/environment CRUD

Status: done

## What to build

两张新表、迁移、种子,以及带安全防护的 CRUD。本单**不改**任何现有判定逻辑(那是 02),只让新模型可用且现有行为完全不变。

### 模型

```go
// model.EnvTier —— 分层标签,管控等级的载体。规则按它分行。
// 注意:与既有 Connection.Tags(数据访问范围)无关,禁止在命名上混用 tag。
type EnvTier struct {
    Code            string `gorm:"primaryKey;size:16"`
    DisplayName     string `gorm:"size:64;not null"`
    SortOrder       int
    RequireMFA        bool
    DangerBanner      bool
    CountsInPending   bool
    ScanBaseline      bool   // 全局唯一且必须有且仅有一个
    ConnLayer       string `gorm:"size:64"` // 替 connEnvMeta 的 "L1 核心 · 写"
    DefaultRole     string `gorm:"size:64"` // 替 connEnvMeta 的 dba_l2 / developer
}

// model.Environment —— 实例分组。一个 tier 绑 N 个环境。
type Environment struct {
    Code        string `gorm:"primaryKey;size:32"`
    DisplayName string `gorm:"size:64;not null"`
    TierCode    string `gorm:"size:16;index;not null"`
    SortOrder   int
}
```

`tbl_connection.env` 语义变为 environment code(**列不改名、不回填**)。

### 迁移与种子

- 新迁移文件 `0012_env_tier.sql`(MySQL);sqlite 走 AutoMigrate
- 种子 4 个 tier:`prod`(ScanBaseline=true, RequireMFA=true, DangerBanner=true, CountsInPending=true)、`gli`、`staging`、`dev`;`ConnLayer`/`DefaultRole` 从 `service/admin.go:21` `connEnvMeta` 的现有映射逐字搬入
- 种子 4 个**同名** environment,各绑同名 tier —— 这是 `tbl_connection.env` 零回填的前提
- 幂等(比照 `seedSchema`),已存在则跳过
- `bootstrap/seed.go:40-86` `seedGliEnv` 从启动路径退役(保留函数供历史迁移调用,加注释说明已被 tier 模型取代)

### CRUD 与防护

**Tier**
- 建:**必须**传模板 tier code。单事务内:写 tier 行 + 克隆模板的**全部** `tbl_role_capability` 行(role×capability)+ 全部 `tbl_risk_command` 行。任一步失败整体回滚 —— 半成品 tier 等于无管控环境。
- 删:若仍有 environment 绑定 → 拒绝(`ErrBadRequest` + 明确 msg)。若持有 `ScanBaseline` → 拒绝,要求先转移。
- 改:`ScanBaseline` 置 true 时,同事务把其他 tier 的该位清零(保证全局唯一);置 false 时若无其他持有者 → 拒绝。

**Environment**
- 建:选一个已存在 tier,**不克隆任何规则行**。
- 删:必须传目标 environment code;单事务内把该环境下所有 connection 的 `env` 改为目标值,再删环境。目标环境不存在或等于自身 → 拒绝。
- 至少保留一个 tier 与一个 environment,删到零 → 拒绝。

### 替换 validEnvs

`service/admin.go:229` 的 `validEnvs` map 改为查 `tbl_environment` 是否存在该 code。`CreateConnection`/`UpdateConnection` 的校验语义不变(未知值仍拒绝),但数据源从常量变为表。

`connEnvMeta`(`admin.go:21`)改为:environment code → tier → 取 `ConnLayer`/`DefaultRole`。

## Acceptance criteria

- [x] 两张表在 sqlite(AutoMigrate)与 MySQL(迁移文件)均生效
- [x] 种子幂等:重复 `init` / 重启不产生重复行
- [x] **升级既有库后,所有现有连接与规则行为完全不变**(env 值未被改动,判定结果与升级前一致)
- [x] 建 tier 时克隆的规则行数 = 模板 tier 的行数(能力矩阵与风险字典各自校验)
- [x] 克隆中途失败 → 事务回滚,不留下无规则的 tier
- [x] 有 environment 绑定的 tier 不可删;持有 ScanBaseline 的 tier 不可删
- [x] ScanBaseline 全局有且仅有一个,任何写入路径都无法破坏该不变式
- [x] 删 environment 会把其下 connection 原子迁移到目标环境
- [x] 删到零 tier 或零 environment 被拒绝
- [x] `go build ./...` / `go vet ./...` / `go test ./...` 通过

## Blocked by

无。这是本 feature 的第一单。

## Comments

**2026-08-06 · 内置标签语义纠正(五个标签)**

本单原文写的「种子 4 个 tier:prod / gli / staging / dev」**含义是错的**,已纠正为五个,详见 PRD「内置分层标签的含义」:

`prod` 生产 · `uat` 演练(**新增**) · `gli` **法务**(原写作灰度) · `dev` 开发 · `staging` **预发布**(原写作演练UAT)

只有显示名错,code 从未错,所以不搬规则行、不动连接。落地:

- `builtinTiers` 改为五条;`mirrorTierRules(src, dst)` 从原 `backfillGliEnv` 泛化出来,gli 与 uat 都用它从 staging 克隆
- `correctBuiltinTiers`:升级旧库时补 uat(连同它的环境与规则行)+ 替换两个错标签。**只替换本产品自己写下的旧值**,管理员改过名的保持原样
- 迁移 `0014_five_tiers.sql`(MySQL);每条 UPDATE 都以旧值为条件,`INSERT … SELECT … NOT EXISTS` 保证可重跑
- **迁移 `0012` 未改动** —— 已经在既有安装上跑过的迁移不能改写,由 0014 纠正
- 全仓 13 处「GLI = 灰度」的注释/测试断言一并修正(含 `gli_env_test.go`)
- 测试:`tier_correction_test.go`(4 个:五标签含义 / uat 受管控 / 升级补 uat 与改名 / **管理员改过的名字不被覆盖**)

三处旧测试因为拿 `uat` 当「未知环境」示例、或断言 `L3 演练UAT` 而失败,已按真实语义改写 —— 这正是这类测试该起的作用。

**2026-08-06 · 实现完成**

- 模型 `model.EnvTier` / `model.Environment`(`model.go`),`allModels` 注册走 AutoMigrate
- 迁移 `migrations/0012_env_tier.sql`;`backfillEnvTiers` 同时挂在 `Seed` 与 `Migrate` 两条路径(比照 `backfillGliEnv`,升级库走 migrate 而非 seed)
- 种子按**表为空**判定而非逐行补,否则管理员删掉的 tier 会在重启时复活
- CRUD:`repository.go`(`CreateEnvTierFrom` 单事务克隆、`clearOtherBaselines`、`DeleteEnvironmentMoving`)+ `service/envtier.go` + `handler/envtier.go` + 路由(读开放,写 `rules` 菜单 + admin)
- `validEnvs` 白名单删除,改由 `s.connEnvMeta` 查 `tbl_environment` 兜底;`connEnvMeta` 硬编码映射搬进 tier 表
- 测试:`bootstrap/env_tier_test.go`(10 个,黑盒)+ `repository/envtier_clone_test.go`(6 个,事务/回滚/原子性)
