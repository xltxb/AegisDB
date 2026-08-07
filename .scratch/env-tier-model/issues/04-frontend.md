# 04 · 前端动态化 + tier/environment 管理页

Status: done

## What to build

去掉 9 处硬编码分层数组,新增管理页,并让所有按分层查表的地方能容忍未知值。

### 类型与数据源

- `types/index.ts:3` `export type Env = 'prod' | 'gli' | 'staging' | 'dev'` → 放宽为 `string`(编译期枚举在动态分层下不再成立)
- 新增 `EnvTier` / `Environment` 类型与对应 API
- 应用启动时拉取一次 tier + environment 列表(放 Pinia store),各页面从 store 取,不再各自硬编码

### 逐处去硬编码

| 文件 | 现状 | 改为 |
|---|---|---|
| `DbTree.vue:111,124` | `['prod','gli','staging','dev']` | tier 一级、environment 二级(见下) |
| `DbTree.vue:82` `envMeta` | 写死 label + dot | 从 tier 取,**未知 key 兜底** |
| `PermissionsView.vue:84` `envCols` | 四列写死 | 按 tier 出列 |
| `RiskRulesView.vue:19,31,176,206,272` | 四处数组 + 两个联合类型 ref | 按 tier 出列 |
| `RiskRulesView.vue:132` | 新增命令传 `{prod,staging,dev}` | **不再传 map**,由服务端展开(见 02) |
| `ConnectionsView.vue:53` `envOpts` | 四个 i18n label | 从 environment 列表取 |
| `connectionImport.ts:15` `IMPORT_ENVS` | 批量导入校验 | 从 environment 列表取 |

### DbTree 分组

> **2026-08-06 修订**:原文按已定决策 6 写的是「tier 一级、environment 二级」。实际实现为 **environment 一级 → 数据库类型 二级 → instance 三级**,tier 退出树的层级 —— 修订理由见 PRD「决策 6 的修订」。以下为实际实现。

```
▾ 香港生产 prod-hk  ●红(颜色取自 prod 分层,hover 显示分层名)
   ▾ MySQL
        hk-billing
   ▾ PostgreSQL
        hk-report
▾ 上海生产 prod-sh  ●红
   ▾ PolarDB
        sh-orders
▸ 测试 · DEV        ●绿(收起)
```

- 类型按引擎目录顺序排(`engineLabels()`),不是字母序;不在目录里的引擎排最后
- 类型层**恒显示**,即使该环境只有一种引擎 —— 引擎决定命令怎么被解析,值一行
- 搜索同时匹配实例名 / environment code / environment 显示名 / 类型,命中后自动展开各级
- 默认展开:**第一个 tier 下的所有 environment**(原为「prod 开、其余关」的推广);解析不到 environment 的兜底组默认展开 —— 那种组最该被看见

### 规则两页的列数可变

`PermissionsView`(能力矩阵)与 `RiskRulesView`(风险字典)当前是固定四列布局。tier 可新增后列数可变,宽表需横向滚动 —— 容器加 `overflow-x: auto`,**页面 body 不得横向滚动**(与仓库既有做法一致)。

### 未知 tier / environment 兜底

删除后历史记录里的 code 成为悬空引用。`DbTree.vue:126` 当前的 `t(envMeta[k].label)` 遇未知 key 会**直接抛异常**。所有按 code 查表处必须兜底:显示原始 code + 中性配色,不崩、不空白。

### 管理页

新增 tier / environment 管理界面(位置取决于 PRD 待定项:复用 `rules` 菜单或独立菜单):

- tier:列表 + 新建(**必选模板 tier**,UI 上明示「将复制 X 的全部规则」)+ 属性位开关 + 删除(有 environment 绑定时禁用并说明原因)
- environment:列表 + 新建(选 tier)+ 删除(**必选迁移目标**,明示将迁移 N 个实例)
- `ScanBaseline` 在 UI 上是单选语义(切换即转移),不能同时勾两个

删除均为高危操作,需二次确认(比照 `RiskRulesView` 删除规则的既有交互)。

### i18n

新增 tier/environment 的显示名来自库内字符串,**无法国际化**(PRD 已知 trade-off)。内置四个 tier 保留 `envProd`/`envGli`/… 的 i18n 回退:code 命中内置值时走 i18n,否则用 `display_name` 字面值。

新增文案注意转义 `@` `|` `{`(见 vue-i18n 运行时编译约束)。

## Acceptance criteria

- [x] 全仓搜索确认无残留的 `['prod','gli','staging','dev']` 硬编码数组 —— 但**保留了一处**,见下方说明
- [x] 新建 environment 后,连接编辑下拉、导入校验、DbTree 立即可见(无需重新登录)
- [x] DbTree 分组正确(实际为 environment → 类型 → 实例 三层,见上方修订);搜索能命中 environment 与类型并自动展开
- [x] 规则两页在 tier 增至 8 个时横向滚动正常,**页面 body 不横向滚动**
- [x] **未知 code 兜底**:构造一条引用已删除 environment 的历史记录,审计/审批页正常渲染不报错
- [x] tier 删除按钮在有 environment 绑定时禁用并给出原因
- [x] environment 删除必须选迁移目标,并显示将迁移的实例数
- [x] ScanBaseline 无法同时勾选两个(UI 上做成单选语义:只能「设为基准」,没有关闭开关)
- [x] 新增高危命令后,规则页每个 tier 列都显示等级(不再有 gli 那样的空列)
- [x] `npm run type-check` / `npm run test:unit` / `npx playwright test` 全绿

## Blocked by

01(需要 API)、02(新增命令的服务端展开)

## Comments

**2026-08-06 · 实现完成**

管理页按你的决定做成**独立一级菜单**(而非并入规则中心)。

- 新增菜单 key `envtier`:seed 的 `menuKeys`/`menuMatrix`(仅 admin)、路由守卫从 `menu("rules")` 改为 `menu("envtier")`、前端 `navItems`/`ROUTE_ORDER`/`router`/`menuDefs` 同步
- **`backfillEnvTierMenu`**:菜单 key 无 RoleMenu 行 = 拒绝访问,所以升级旧库时没人(包括管理员)能进新页面,而唯一能授权的地方正是这个页面。回填按「谁已有 `rules` 就给谁 `envtier`」,不新增任何人的权限;仅在该 key 完全不存在时写入,不会撤销后又被重启复活
- 新 store `stores/envtier.ts` + 纯函数 `lib/envTierLabels.ts`(拆出来是为了能进 `tests/unit` 那条纯逻辑测试缝 —— store 会拉进 axios,单测环境跑不了)
- 去硬编码:`DbTree`(两级分组 + 未解析兜底组)、`PermissionsView`(列按 tier 动态 + 横向滚动)、`RiskRulesView`(tab/segment 按 tier + 新增命令不再传 map)、`ConnectionsView`(下拉与分组按 environment)、`connectionImport`(校验集由调用方传入)
- `TerminalSession` 红色警告改读 `dangerBanner` 属性位;解析不到 tier 时给**中等**警告而非最轻的绿色
- 新增 `EnvTiersView.vue`,含克隆说明、删除禁用原因 tooltip、迁移目标必选、基准转移二次确认
- 全局 `.scx` 横向滚动工具类(对齐既有 `.scy`)
- 测试:`tests/unit/envTierLabels.spec.ts`(9 个,全是「code 解析不到时怎么办」)、`tests/unit/connectionImport.spec.ts` +2、`e2e/env-tier-tree.spec.ts`(5 个)、后端 `envtier_menu_backfill_test.go`(3 个)

**2026-08-06 追加 · 层级改为 environment → 类型 → 实例**

见上方「DbTree 分组」修订与 PRD 决策 6 的修订。同一改动覆盖两处:

- `DbTree.vue`:顶层换成 environment,插入类型层;tier 只剩颜色点 + tooltip
- `ConnectionsView.vue`:表格分组行从单层 environment 改为 environment + 类型两级

清掉的死代码:`tierSectionLabel` / `BUILTIN_SECTION_LABEL`、store 上未被使用的 `isUnknownEnv`,以及 8 个失效 i18n key。

**顺带修掉一处硬编码残留**:`DbTree.vue` 的折叠状态初始值仍是 `{ gli: true, staging: true, dev: true }` —— 三个 environment code 写死在组件里,正是本单要消除的东西,当初漏了。是 e2e「分层加载失败」用例暴露的:环境列表拉不到时,这个残留值会把 `dev` 组收起,其中的实例就看不见了。现改为初始为空,默认值全部从环境列表推导;推导不到的组默认展开。

### 保留的一处硬编码(有意为之)

`connectionImport.ts` 的 `IMPORT_ENVS` 仍是四个内置值,但**不再是判定依据** —— `parseConnectionImport(text, envs)` 由调用方传真实环境列表,该常量只在调用方没传/传空(拉取失败)时兜底。理由:传空数组会导致整张表每行都报「无效,仅支持:」,而这只是前置友好校验,服务端才是权威。若你要求严格清零,把 `envs` 改成必填即可。

### 三处与工单描述不符的核对结果

1. **`RiskRulesView.vue:132` 的修法与工单预期不同**。工单说「不再传 map,由服务端展开」,我传的是**空 map** `{}`。因为 `upsertRiskCommand(command, env)` 的 `env` 参数在既有代码里还承担「只改某一格」的语义(`createRule` 依赖它,见 R11 注释:只动选中环境,否则会清掉其他环境的既有等级)。改成空 map 既触发服务端全量展开,又不破坏 `createRule` 那条路径。

2. **VSwitch 没有 `disabled` prop**。`PermissionsView` 一直在传 `:disabled="!isAdmin"`,实际被忽略 —— 非管理员的开关看起来可点。我在新页面改用 `isAdmin &&` 守卫回调而非依赖该 prop。这是既有小问题,没在本单范围内改 VSwitch。

3. **`.grouprow` 的配色类名与 tier 调色板不一致**(`warn` vs `warning`),已补 `.grouprow.warning` 与 `.grouprow.muted`。
