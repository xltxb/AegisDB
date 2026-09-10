# PRD · 环境与分层标签解耦(标签绑定制)

Status: done(01–04 全部实现;webhook 契约已于 2026-08-12 确认无需改动,见文末「遗留」表)

> 本文由 2026-08-04 两轮需求分析收敛而成。实现拆单见本目录 `issues/01..04`。
> 决策记录见文末「已定决策」。
>
> **2026-09-11 以代码为准校准**:模型与判定解耦已全部落地;与本文原稿不一致的实现细节、以及尚未闭合的口子,
> 集中在「交付现状」一节。

> **2026-08-06 语义纠正**:本文以下所有把 `gli` 写作「灰度」、`staging` 写作「演练UAT」的地方**都是错的**。
> 正确含义见文末「内置分层标签的含义」。代码从未错 —— 错的一直只是标签。

## Problem Statement

实例分层(`prod` / `gli` / `staging` / `dev`)当前是**写死的四个字符串**,同时承担了两个互相冲突的职责:

1. **管控等级** —— 能力矩阵(`tbl_role_capability`)与高危命令字典(`tbl_risk_command`)都按它分行,风险判定按它查表。
2. **实例分组** —— 实例归属、左侧树分组、审计/审批记录里的归属快照。

两个职责挤在一个字段上,导致「我要再加一个生产集群」这种诉求无法表达:新增 `prod-hk` 必须新造一个分层,而新分层没有规则行 —— 而本仓库两条查找路径都是**查无此行即放行**:

- `repository.go:258` 能力矩阵无行 → `LevelAllow`(注释原文:*no rule configured for this cell = allow*)
- `gateway/risk.go:297-303` 风险字典无该 env 的行 → `RiskOff`

所以「加一个分层」在当前模型下等价于「造一个任何人可以直接 DROP TABLE 且无需审批的环境」。`service/admin.go:229` 的 `validEnvs` 白名单(注释 ED5)正是为堵死这条路而存在,代价是分层彻底不可扩展。

**既有证据**:上一次新增分层(GLI)留下了 `bootstrap/seed.go:40-86` 的 `seedGliEnv` —— 一段把 staging 的能力矩阵行与风险字典行整体克隆给 gli 的一次性回填代码。本需求本质上就是把这段一次性代码变成可重复的产品能力。

## Solution

把一个字段拆成两层概念:

- **分层标签 tier** —— 管控等级,规则的载体。能力矩阵与风险字典按 tier 分行。
- **环境 environment** —— 实例分组。一个 tier 可绑定 **N 个**环境。

```
tbl_env_tier      prod / gli / staging / dev / …   ← 规则按它存
      │ 1:N
tbl_environment   prod-hk / prod-sh / uat-结算 / … ← 实例按它归属
      │ 1:N
tbl_connection
```

新建 `prod-hk` 环境绑到 `prod` 标签,**不复制任何规则行**,立即拥有完整的 prod 管控,也不存在「空规则窗口期」。

### 命名冲突(必读)

`Connection.Tags` 字段**已经存在**,含义是**数据访问范围标签**(`router.go:105,119,133`、`TagEditModal.vue`、`seed.go:156` roleTags),决定角色/用户能访问哪些库。与本需求的「分层标签」完全无关。

**实现中一律用 `tier` 指代分层标签,禁止复用 `tag` 一词**,避免与既有 Tags 语义相撞。中文文案里可称「分层标签」。

### 迁移成本:近乎为零

只要默认环境的 code 与标签 code 同名(`prod` 环境绑 `prod` 标签):

| 表 | 处理 |
|---|---|
| `tbl_connection.env` | 现有值 `"prod"` 直接就是合法 environment code → **不回填** |
| `tbl_role_capability.env` | 现有值就是 tier code,列语义从「环境」变「标签」→ **不动数据** |
| `tbl_risk_command.env` | 同上 → **不动数据** |

整个迁移 = 新增两张表 + 插入 8 行种子(4 标签 + 4 同名环境)。`seedGliEnv` 可从启动路径退役为历史迁移。

## 核心产出:每一处 `conn.Env` 归属哪一层

实施时最容易出错的地方 —— 同一个字段,一半调用点该换 tier,另一半该换 environment。

### → 换成 tier(规则判定 / 管控强度)

| 位置 | 用途 |
|---|---|
| `service/gateway.go:57,117,142,219` | `EvaluateFor` 三层判定 |
| `service/async_exec.go:46`、`service/export.go:138` | 同上 |
| `gateway/risk.go:392,400` | 能力矩阵 + 风险字典查表 |
| `service/gateway.go:704` | 强制 MFA(`conn.Env != model.EnvProd`) |
| `service/gateway.go:562` | 脚本扫描基准(`ScanStatement(model.EnvProd, …)`) |
| `repository/repository.go:1014` | 待审批统计(`tbl_connection.env = 'prod'`) |
| `service/admin.go:21` `connEnvMeta` | 显示层级名 + 默认连接角色 |
| `components/terminal/TerminalSession.vue:161` | 终端红色高危警告 |

### → 换成 environment(归属 / 展示 / 快照)

| 位置 | 用途 |
|---|---|
| `service/async_exec.go:63`、`service/export.go:151` | `conn.Env + "-" + conn.Name` 拼实例显示名 |
| `service/gateway.go:242` | `Approval.Env` 审批记录快照 |
| `service/webhook.go:147` | 飞书卡片 payload `"env"` |
| `components/terminal/DbTree.vue` | 左侧树分组 |
| 前端各处 `${c.env}-${c.name}` | 实例显示名(含 `ExportView` 可搜索选择器) |

注:实例显示名会自然从 `prod-tongcha` 变为 `prod-hk-tongcha` —— 这正是多生产环境的意义。

## 数据模型

```
tbl_env_tier                      分层标签 = 管控等级
  code              PK size:16    prod / gli / staging / uat / dev / …
  display_name      size:64
  sort_order        int           UI 排序(prod 在最前)
  require_mfa       bool          ← 替 gateway.go:704 的 == EnvProd
  danger_banner     bool          ← 替 TerminalSession.vue:161
  counts_in_pending bool          ← 替 repository.go:1014
  scan_baseline     bool          ← 替 gateway.go:562;全局唯一
  strict_nowhere    bool          ← 无 WHERE 拦截,2026-09 由全局开关改为按分层(迁移 0030、ADR 0013)
  conn_layer        size:64       ← 替 connEnvMeta 的 "L1 核心 · 写"
  default_role      size:64       ← 替 connEnvMeta 的 dba_l2 / developer

tbl_environment                   环境 = 实例分组
  code              PK size:32    prod-hk / prod-sh / uat-settle
  display_name      size:64       ← 中文名放这里;code 只能是 [a-z0-9-]
  tier_code         size:16 idx   → tbl_env_tier.code
  sort_order        int

tbl_connection.env  → tbl_environment.code(列不改名,语义变为环境)
tbl_role_capability.env / tbl_risk_command.env → **已于迁移 0016 改名为 `tier_code`**
  (原稿说"列不改名,数据不动";数据确实没动,但列名跟着语义一起改了 —— 留着叫 env 的规则表列,
   下一个读代码的人还会以为规则挂在环境上)
```

`conn_layer` / `default_role` 挂在 tier 上,用于替换 `service/admin.go:21` 的硬编码映射。注意 `admin.go:69` 当前会在切换分层时**覆盖**实例的 `Layer` 与 `DefaultRole`,这个派生关系必须跟着搬进 tier 表。

## Scope

**In**

- `tbl_env_tier` / `tbl_environment` 两张表 + 迁移 + 种子(4 tier + 4 同名 environment)
- tier CRUD(新增时**必须**克隆模板 tier 的全套规则行,单事务)
- environment CRUD(新增零克隆;删除时实例强制迁移到指定环境,单事务)
- 消除 8 处 tier 硬编码判定,改读属性位
- 审计/审批记录**双快照**:environment code + 当时的 tier code
- `UpsertRiskCommand` 改为**服务端按全部 tier 展开**(根治下述 bug)
- 前端:tier/environment 管理页、规则两页列动态化、DbTree 两级分组、连接编辑的环境切换、未知 tier/environment 兜底显示
- 回归测试:克隆事务、空规则防护、删除迁移、双快照、bug 复现用例

**Out(后续)**

- 环境级别的额外属性(地域、机房、联系人)
- tier 属性位的更细粒度(如按 tier 配审批链层数)
- 历史记录的 tier 快照回填(旧数据 tier 快照留空,按 environment 反查并标注「推断值」)

## 必须一并修的现存 bug

`views/RiskRulesView.vue:132` 新增高危命令时只写 `{prod, staging, dev}` —— **漏 gli**;而 `repository.go:542` `UpsertRiskCommand` 只写入 map 里给出的 key。

**后果**:管理员在后台新增的任何高危命令,对灰度实例完全不生效(无行 → `RiskOff` → 放行)。加 GLI 时改了 seed 的回填,却漏了运行时这条路径。

分层可自定义后,这个 bug 会从「漏一个」放大为「漏全部新 tier」,因此必须在本需求内根治:**写入路径由服务端按当前全部 tier 展开,不由前端传 map**。

## 风险与防护

| 风险 | 防护 |
|---|---|
| 新 tier 无规则行 = 无管控实例 | 建 tier 必须选模板并在**同一事务**内克隆完能力矩阵 + 风险字典;克隆失败整体回滚 |
| 删除持有 `scan_baseline` 的 tier → 脚本扫描器**静默失效**(任何脚本都报无风险) | `scan_baseline` 全局唯一且非空;删除持有者时强制转移,拒绝裸删 |
| 删除 environment 后历史记录悬空 | 记录为字符串快照,不设物理外键;前端遇未知 code 降级显示原文 + 中性配色(当前 `DbTree.vue:126` `t(envMeta[k].label)` 遇未知 key 会抛异常) |
| 切换实例环境污染审计链 | **历史快照不可回溯修改**;切换只影响此后的新记录 |
| tier 全删光 | 至少保留一个 tier 与一个 environment,否则无法建连接且 `scan_baseline` 无处安放 |

## 已知 trade-off

分层显示名从 i18n key(`envProd` / `envGli` / …)变为库内字符串后,**自定义 tier / environment 的名字无法国际化**,中英界面显示同一字面值。内置四个 tier 保留 i18n 回退,自定义的只能用管理员输入值。

## 交付现状(2026-09-11 以代码为准校准)

模型本身完全按本文落地:两张表、克隆事务、空规则防护、删除迁移、双快照、规则写入由服务端按全部 tier 展开,
后端判定路径 8 处**全部**改读 `tierOf(conn)`,且解析不出分层时一律 fail-closed(不按放行处理)。
以下是与原稿不一致、或尚未闭合的部分。

### 与原稿不一致

| 原稿 | 实际 |
|---|---|
| `tbl_role_capability.env` / `tbl_risk_command.env` **列不改名** | 迁移 0016 把两列都改名为 `tier_code`(数据未动)。超出本单范围,但语义更清楚 |
| environment code 举例 `uat-结算` | code 受 `^[a-z0-9][a-z0-9-]{0,31}$` 约束,**中文建不出来**;中文名请填 `display_name` |
| `seedGliEnv` 可从启动路径退役为历史迁移 | **未退役**,仍挂在 `Seed` / `InitDatabase` / `Migrate` 三处(注释已改口解释原因) |
| 能力矩阵第 7 个维度 `explain` | 其后又加了第 8 个 `release`(能否**发起发布**,与语句本身能否跑是两个问题) |
| 内置四个 tier | 实际五个:`prod` / `gli` / `staging` / `uat` / `dev`(见文末「内置分层标签的含义」) |

### 尚未闭合(均已建 GitHub issue)

| 问题 | 后果 |
|---|---|
| `correctBuiltinTiers` 逐行补建内置 tier(而非按表为空判定) | 管理员删掉的 `uat` 会在下次 `migrate` 时连同同名环境、staging 规则一起**复活**;且复活的行不做 ADR 0013 要求的 `strict_nowhere` 二次 UPDATE,`dev` 会以拦截态回来。issue 01 的 Comments 明确承诺过不这样做 |
| 前端仍有 2 处按环境名判生产 | `RiskInspector.vue` 用环境 code 查以 tier code 为键的字典 → 第二个生产集群的「受限命令」列表为空;`ExportView.vue` 的红色生产警告按 `env === 'prod'` → `prod-hk` 不出警告。正确写法是 `tierOf(env).dangerBanner`(`TerminalSession.vue` 已如此) |
| `tierLabel` 对内置 code **先**取 i18n、从不读 `displayName` | 管理员把 `staging` 改名"预发布-A",库里保住了,页面永远显示译文 —— 后端特意用 `wrongBuiltinNames` 保护改过的名字,前端把这层保护抵消了 |
| tier code 正则允许 32 字符,而 `EnvTier.Code` 等列是 `size:16` | SQLite 开发库能建 20 字符的 tier,同一操作在 MySQL 上报 Data too long |
| `DeleteEnvTier` 的三项检查在事务外(check-then-act) | 并发建环境可造出指向已删 tier 的环境(后果 fail-closed,属可用性问题);另不处理 `tbl_pipeline.tier_code` 悬空 |
| `CountProdInterceptions` 按**当前**绑定 join,而非审计行自己的 `tier_code` 快照 | 环境改绑后历史命中数随之漂移 —— 双快照(issue 03)的意义正是让统计不随当前绑定变化 |

## 已定决策(2026-08-04)

| # | 决策 | 结论 |
|---|---|---|
| 1 | 新建 tier 的初始规则来源 | **从现有 tier 克隆**(选模板),与 `seedGliEnv` 既有做法一致 |
| 2 | prod 的 4 处特殊语义如何泛化 | **tier 属性位**,代码判断属性而非名字 |
| 3 | 删除时的处理 | **允许删,实例强制迁移**(删 environment→实例迁移;删 tier→要求无 environment 绑定) |
| 4 | tier 本身是否可新增 | **可新增**,保留克隆事务 + 空规则防护 |
| 5 | 审计/审批历史快照 | **environment 与 tier 双存**,可解释「当时为何要审批」 |
| 6 | DbTree 分组 | ~~tier 一级、environment 二级~~ → **已于 2026-08-06 推翻,见下** |

### 决策 6 的修订(2026-08-06)

实际层级定为 **environment 一级 → 数据库类型 二级 → instance 三级**,tier 退出树的层级。

起因是一个本单未预见的维度:估算里默认一个环境一种数据库,而实际上同一个集群会同时跑 MySQL / PostgreSQL / Oracle,「哪个引擎」决定了命令怎么被解析、用哪个驱动连,和「哪个集群」是并列的导航问题,不是列里扫一眼的属性。四层(tier → env → type → instance)嵌套过深,取舍后砍掉 tier 这一级。

**原决策的理由并没有作废** —— 「顶层看得出哪些是生产」仍然成立,只是换了载体:环境行的颜色点取自它绑定的 tier(`dotForEnv`),tier 名放在该行的 hover tooltip 上。危险度提示一点没丢,少的只是一行标题。

连带影响:`tierSectionLabel` / `BUILTIN_SECTION_LABEL` 与 `prodEnv`/`gliEnv`/`stagingEnv`/`devEnv`、`prodRow`/`gliRow`/`stgRow`/`devRow` 八个 i18n key 随之失效,已删除。

## 待定(2026-08-06 已全部定案)

- ~~谁能管理 tier / environment~~ → **独立一级菜单** `envtier`(仅 admin)。升级旧库时按「已有 `rules` 者获得 `envtier`」回填,否则无人能进入该页面。
- ~~新增高危命令时各 tier 的默认等级~~ → **按属性位分档**:持 `scan_baseline` 或 `require_mfa` 的 tier 默认 `high`,其余 `off`。猜错方向的代价不对称:多一次审批 vs 刚被标为高危的命令在生产 tier 免审执行。实现于 `repository.defaultRiskLevel`。
- ~~`tbl_environment` 主键~~ → **用 code**,`tbl_connection.env` 零回填如期成立。

## 内置分层标签的含义(2026-08-06 定案)

**环境的名字不决定它是什么环境,分层标签决定**;规则只挂在标签上,不挂在环境名上。这一点模型本来就是对的 —— 错的是标签写的字。

| code | 含义 | 此前被错写为 |
|---|---|---|
| `prod` | 生产环境 | — |
| `uat` | 演练环境 | **本次新增**(演练此前没有自己的标签) |
| `gli` | **法务环境** | 灰度 · GLI |
| `dev` | 开发环境 | 测试 · DEV |
| `staging` | **预发布环境** | 演练UAT · STAGING |

这五个写死在库里(`builtinTiers` + 迁移 `0014`)。

**只有标签错了,code 从来没错**,所以这次纠正不搬任何一行规则、不动任何一个连接 —— 只改显示名 + 补一个标签。已定的两件事:

- `gli` 的管控档位**保持宽松档不变**(不强制 MFA / 无红色警告 / 不计入待审批)。法务库确实是真实数据,但收紧会立刻改变线上实例的判定结果,那是运维的决定,分层页现在可以做;本次纠正只改名。
- `uat` 的规则**从 staging 克隆** —— staging 当年正是顶着「演练UAT」这个名字持有那套规则,克隆它等于把演练规则交还给真正的演练标签。

升级已有库时,只替换**本产品自己写下的**旧标签(见 `wrongBuiltinNames`);管理员改过名的标签保持原样 —— 修自己的错不是覆盖别人决定的理由。

## ~~遗留~~(2026-08-12 已全部确认)

| # | 事项 | 结论 |
|---|---|---|
| 1 | webhook `payload.env` 取值范围扩大 | **不成问题**。是否需要审批由策略与环境决定,需要时才把审批信息发给审批魔方 —— 下游不对 `env` 做枚举校验。**无需改动** |
| 2 | `gli`(法务环境)的管控档位 | **保持宽松档**。收紧与否属运维决策,分层页随时可调 |
| 3 | 新增高危命令的默认等级 | **改为要求显式指定生效环境**,不再由服务端猜。见下 |
| 4 | 纯 EXPLAIN 按读放行是否够 | **单独加一档**:`explain` 成为能力矩阵的独立维度。见下 |

### 3 的落地(2026-08-12)

新增命令时逐个分层指定等级,「放行」也会**写入一条明确的 `off` 记录** —— 缺行与 `off` 对引擎是一回事,但只有后者在页面上表现为「有人做过这个决定」。预填仍按原默认值,常见情况仍是一次确认。

同时修掉一个此前引入的回归:`UpsertRiskCommand` 对未给出的分层一律写默认值,会**覆盖既有等级**(与 R11「只动选中环境」的约定相悖)。现改为「已有行保持不动,只有完全没有行的分层才补默认值」—— 两个性质同时成立:新命令不留空洞,老命令的部分更新不误伤。原测试只是碰巧通过(prod 的默认值恰好等于种子值),已加强。

### 4 的落地(2026-08-12)

`explain` 成为能力矩阵的第 7 个维度(role × explain × tier),种子一律 `allow` —— 即当前行为,加这一档是为了**能够**收紧,不是替使用方做决定。

关键在于组合方式:计划同时受 `select` 与 `explain` 两道门约束,**取更严的那个**。只按 `explain` 判会让这次改动变成一次放松 —— 该维度种子是 `allow`,于是一个被刻意禁掉 SELECT 的角色反而获得了读取它读不到的表的执行计划的能力。拆分一项权限,不能发出原本被拒绝的东西。
