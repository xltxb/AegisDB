# 大表索引变更自动走 OSC —— 设计

- 日期:2026-09-19
- 相关:ADR 0011(MySQL 在线加索引)、`backend/internal/osc/`、`backend/internal/service/pipeline.go`
- 状态:**已实现(2026-09-19)**,见 `docs/superpowers/plans/2026-09-19-osc-auto-route.md`
  与 ADR 0011 的「第二个入口:发布流水线」一节

执行过程中这份设计被修正过四处,都记在 ADR 里,这里只列出来:

1. **急停开关漏了流水线这条路。** 本文档第一节把 `osc.enabled` 当成"接在 HTTP 接口上的
   闸",而第三节新增的执行阶段绕过了它 —— 设计写下的时候,流水线这条发起路径还不存在。
   修法是让两条路共用同一个 `enabled` 闭包。
2. **回调失败时不能无条件交回 `driveRelease`。** 它会把已经 failed 的阶段当成"处理过了"
   跳过,循环走完落到 `finishRelease(success)`:迁移失败的发布单最后显示成功。
3. **续跑要异步。** 回调挂在迁移的退出路径上,而 `driveRelease` 可能同步跑上 90 分钟,
   期间 `IsRunning`/`Abort` 对一个已经结束的任务说谎。
4. **判定说明只记有信息量的那些。** 原设计让每条语句都写一行,普通 DML 的日志翻倍,
   而阶段日志有 20000 字的上限。

## 这份文档要解决的问题

发布流水线(CI/CD)的执行阶段现在把 SQL 原样下发。一条 `ALTER TABLE t ADD INDEX` 落在
一张八百万行的表上时,原生 DDL 虽然不阻塞读写(MySQL 5.6+ 的 `ALGORITHM=INPLACE,
LOCK=NONE`),但换不来三件事:**中途限流、从库跟得上、cut-over 那一下的 MDL 可重试**。
`internal/osc` 已经能提供这三件事,但它只有一条入口:人去 OSC 控制台手工发起。

目标是让**发布单自己走对那条路**:表大到一定程度时,索引变更自动改走 OSC;而这件事
必须是可关、可单次覆盖、并且**事后说得清这一次到底走的哪条路**。

同时补一个更小的东西:OSC 的总开关现在只能改配置文件加重启,**关不掉一个正在出事的
特性**。

## 五条已经定下来的决定

这些是设计的前提,不在实现阶段重新讨论。

| # | 决定 | 理由 |
|---|---|---|
| 1 | **只有加索引 / 删索引走 OSC** | OSC 的能力范围就是这些(ADR 0011 刻意收窄)。改列类型、改字符集会引入类型转换与默认值回填的语义问题,扩它是另一个项目 |
| 2 | **该走却走不了时:直发,但把这件事说出来** | 原生加索引本来就是在线的 —— "没走 OSC"是失去了那三件事,不是干了一件危险的事。让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立 |
| 3 | **全局设置定策略,发布单可单次覆盖** | 策略归平台,例外归发起人,而例外进审计 |
| 4 | **逐条路由,串行跑** | 一张单里的每条语句各自判断,命中的逐个走 OSC |
| 5 | **发布单的审批链就是授权** | OSC 是执行手段,不是一道独立的权限。变更本身已经过了 review/approve |

第 5 条**放宽了一道现有的边界**:OSC 的 HTTP 发起接口限平台管理员(ADR 0011,那条守卫
做过变异验证)。自动路由接上之后,一个非管理员点「确认执行」就会发起一次 OSC 迁移 ——
在生产库上建影子表、拷全表、改表名。这不是实现漏出来的,是有意为之,所以要写进 ADR 0011,
而不只是活在代码里。

**这里有一个边界要说清楚:并非每张单都有审批阶段。** 低风险变更走的流程可能根本没有
`approve` 这一步(`stageExecute` 的复判也只在判定要求审批时才检查审批单)。那种单子上,
"审批链就是授权"实际等于"执行闸那个人就是授权"。这是决定 5 的真实含义,不是它的例外 ——
接受它,或者把大表索引变更所在的流程配上审批阶段,而后者是流程配置的事,不是代码的事。

## 一、急停开关(可独立交付)

`osc.enabled` 留在配置文件里当**前提**,`tbl_setting` 里的 `osc.enabled` 当**急停**,
两者相与:

```go
h.AttachOSC(runner, func() bool {
    return cfg.OSC.Enabled && repo.SettingBool("osc.enabled", true)
})
```

**方向是不对称的,这是有意的。** 配置关着时后台怎么拨都打不开 —— 打开它的前提是一次
对着有从库的实例的演练(ADR 0011),那是人做的事,界面上点一下不构成那个前提。而**关**
要快:一次迁移正在把从库拖垮时,人要挡住后续发起,而不是先去重启网关。

默认 `true`(即不额外拦)。界面上那个开关必须说明它只能关不能开,否则人会以为拨一下
就能用。

改动:`bootstrap/router.go` 一行、设置页一个开关、中英文案各一条。

## 二、路由判定(新包 `internal/oscroute`)

一个纯函数层。输入一条语句 + 引擎 + 表的估算行数 + 策略 + 单次覆盖,输出:

```go
type Decision struct {
    UseOSC bool
    Reason string // 人话,直接进阶段日志
    Schema string // 以下三个仅当 UseOSC 为真
    Table  string
    Alter  string // 交给 osc.StartRequest 的 alter 子句
}
```

放进独立包而不是塞进 `service`,理由与 `osc` 把"采集"和"判定"分开是同一个:这一层的
每条规则都能让一次变更走上另一条路,它必须能被单独测透,而不必搭一套流水线。

### 识别哪些语句是索引 DDL

只认这几种形状(MySQL 方言):

```
ALTER TABLE [schema.]t ADD  [UNIQUE|FULLTEXT|SPATIAL] {INDEX|KEY} ...
ALTER TABLE [schema.]t DROP {INDEX|KEY} name
CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX name ON [schema.]t (...)
DROP INDEX name ON [schema.]t
```

**`CREATE INDEX` / `DROP INDEX` 要翻译成等价的 `ALTER TABLE` 子句**,因为 OSC 的
`StartRequest` 收的是 alter 子句。不翻译的话这两种写法永远走不到 OSC —— 而它们很常见,
静默地走不到比明确不支持更糟。

用受限的模式匹配,不引入 SQL parser:**认错的代价有限** —— 顶多是"走了"或"没走"OSC,
而 OSC 自己的 `Preflight` 会对着真实例再拒一次(非 MySQL、无主键、有外键、有触发器……)。
一条 `ALTER TABLE t ADD COLUMN c INT, ADD INDEX i (c)` 这种**混合子句**一律不认:它超出
第 1 条决定的范围,而把它拆开是另一件事。

### 行数从哪来

`osc.Gather` 已经读 `information_schema` 的 `TABLE_ROWS`,直接用它的 `EstimatedRows`。

**不做 `COUNT(*)`。** 八百万行上要跑几十秒,而它换来的精度在这里没有价值:边界上误判
的后果是"走了/没走 OSC",两边都不危险(见决定 2)。InnoDB 的估算值可能偏差可观,这一点
写进设置页的说明,而不是靠一次昂贵的精确统计去掩盖。

拿连接的方式复用 `bootstrap.oscConnect(repo)` 那条 `osc.ConnectFunc` —— service 层
需要它,所以 `Services` 要持有 `*osc.Runner`(见第三部分的接线)。

### 判定顺序

```
单次覆盖 skip        → 不走(理由:发起人本次选择直发)
单次覆盖 force       → 走(跳过行数判断,但仍要通过语句识别)
autoRoute 关         → 不走
非 MySQL             → 不走
不是索引 DDL         → 不走
行数 < 阈值          → 不走(理由带上实际行数)
否则                 → 走
```

覆盖排在最前:它是人对这一次的明确指令,而策略是对一类情况的默认。

## 三、执行阶段挂长任务

### 数据模型

迁移 `0004` 加三列一个索引:

```sql
ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS exec_cursor INT    NOT NULL DEFAULT 0;
ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS osc_job_id  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tbl_release        ADD COLUMN IF NOT EXISTS osc_mode    VARCHAR(8) NOT NULL DEFAULT '';

-- 回调要按任务反查"是哪个阶段在等它"。没有索引的话,每个任务结束都要全表扫一遍阶段表。
CREATE INDEX IF NOT EXISTS idx_release_stage_osc_job ON tbl_release_stage (osc_job_id);
```

`exec_cursor` 是"这个阶段已经执行完的语句条数",`osc_job_id` 是"此刻挂着哪个 OSC 任务"
(0 = 没挂),`osc_mode` 是发起人对这一单的单次覆盖(`""` / `force` / `skip`)。

回调按 `osc_job_id` 反查阶段。**反查可能查不到**(任务是从 OSC 控制台手工发起的,不属于
任何发布单)—— 那是正常情况,不是错误,回调直接返回。

### 状态机

```
stageExecute:
  ① 人工闸(现有逻辑,不变)
  ② 执行前复判(现有逻辑,不变)
  ③ 拆句,从 exec_cursor 开始逐条:
       ├ 判定不走 OSC → 直发(现有逻辑)→ 审计 → exec_cursor++
       └ 判定走 OSC   → Runner.Start → 记 osc_job_id 与 exec_cursor → 返回 waiting
  ④ 全部执行完 → success

OSC 任务走到终态 → Runner 的 OnFinish 回调 → 找到挂着它的阶段:
       ├ done    → exec_cursor++、osc_job_id 清零 → driveRelease → 回到 ③ 继续下一条
       └ failed/aborted → 阶段 failed,日志点名那个任务
```

**回调而不是轮询。** `waiting` 在这套流程里本来就是"挂在一件外部的事上"(审批阶段就是
这么用的),沿用它比发明一个后台扫描器省一个组件。`osc` 包不认识 pipeline —— `Runner`
只暴露一个 `OnFinish func(*Job)` 注入点,谁关心谁自己接。

### 三个必须一起处理的细节

**1. 日志会被覆盖。** `driveRelease` 每次都用 `out.log` 覆盖阶段的 `log` 字段。跨
`waiting` 恢复时,如果 `stageExecute` 从空 builder 开始,**前面几条的执行记录会消失** ——
而那正是一次跨了几小时的执行最需要留下的东西。所以恢复时以 `st.Log` 作为日志前缀。

**2. `waiting` 现在有两种意思。** 一种是"等人点确认执行",一种是"等 OSC 任务跑完"。
界面必须分得开:前者要给「确认执行」按钮,后者给了也没用 —— 按下去只会让人以为自己
推进了什么。判据是 `osc_job_id > 0`。这与 ADR 0011 里"中止按钮跟着 `running` 走而不是
跟着 `status` 走"是同一类问题。

**3. 中止发布单要连带中止 OSC 任务。** 否则单子停了、迁移还在拷全表。`AbortRelease`
在停一张单时,若它的执行阶段挂着 `osc_job_id`,调 `Runner.Abort`。

`Abort` 只叫得停**本进程**手上的任务(ADR 0011:`IsRunning` 说的是"这台网关没在推进它",
不是"没有人在推进它")。多副本下另一台副本跑着的任务停不掉,此时发布单照常中止,而那个
任务留成残局被列出来 —— 谎称已经停掉它比留着它更糟。

### 重启之后

网关重启后,阶段停在 `waiting` 且它挂的任务已经死了(`IsRunning` 为假且状态是中间态)。
这跟 OSC 自己的残局是**同一种东西**,按同一种方式呈现:发布单详情把它标成"挂着的迁移
任务已经没有进程在推进它",并链到 OSC 页面去收拾残局(那里已经有影子表的清理指引)。

不自动重试,也不自动失败:一个跑到一半的迁移留下的是影子表和一段没追平的 binlog,
要由人看一眼再决定。

## 四、设置、覆盖与界面

### 设置项(`tbl_setting`)

| key | 默认 | 说明 |
|---|---|---|
| `osc.enabled` | `true` | 急停开关。与配置文件里的 `osc.enabled` 相与 |
| `osc.autoRoute.enabled` | `true` | 大表索引变更自动走 OSC |
| `osc.autoRoute.minRows` | `2000000` | 超过这个估算行数才走 |

`autoRoute` 默认开着是安全的:OSC 的总开关默认关着,所以在没打开 OSC 的部署上它只会
走到"直发并说明"那条路 —— 不改变任何现有行为,只多一行日志。

### 单次覆盖

`ReleaseReq` 加 `oscMode: "" | "force" | "skip"`,落在 `tbl_release` 的一列上,并进审计。
提单表单上是一个三选一,默认"按策略"。

### 界面

- 设置页:三个设置项,急停开关旁边写明"配置里关着时这里打不开"
- 提单表单:单次覆盖
- 发布单详情:执行阶段逐条显示走了哪条路,走 OSC 的那条链到任务;挂着任务时不给
  「确认执行」按钮,显示任务进度
- 阶段日志逐条写明:`第 2 条 · 约 830 万行 · 走 OSC(#17)` / `第 3 条 · 直发(OSC 未启用)`

## 五、测试策略

| 层 | 怎么测 |
|---|---|
| 路由判定 | 纯函数,测透:四种索引 DDL 的识别、`CREATE INDEX` 的翻译、混合子句不认、阈值边界(正好等于阈值不走)、覆盖优先级、非 MySQL |
| 急停开关 | 配置关 + 设置开 = 关;配置开 + 设置关 = 关;两者都开 = 开 |
| 状态机 | 对着真 PostgreSQL(`testsupport`)跑:一张三条语句的单,中间那条走 OSC,断言 `exec_cursor` 推进、日志不丢、第三条在任务完成后才执行 |
| 中止连带 | 停一张挂着任务的单,断言那个 OSC 任务也被叫停 |
| 端到端 | 一张单挂在**真 OSC 任务**上(真 MySQL)跑完 |

每条承重断言都要做变异验证 —— 尤其"日志不丢"和"第三条在任务完成后才执行"这两条,
它们都可能在实现正确性被破坏后仍然绿。

## 六、明确不做的

- **不扩 OSC 的能力范围。** 改列、加列、改字符集仍然直发
- **不做流水线阶段级的配置覆盖。** 全局 + 单次两层够用;第三层等有人真的需要
- **不做多个 OSC 任务并行。** 一张单里的任务串行跑 —— 并行会让几张大表同时拷贝,
  而限流是按单个迁移算的
- **不自动清理残局。** 与 ADR 0011 一致:列出来,删表由人做

## 七、已知风险

1. **估算行数不准。** InnoDB 的 `TABLE_ROWS` 可能偏差可观,边界附近的表会时走时不走。
   代价有限(两边都不危险),但设置页要写明这是估算值。
2. **一张单可能挂很久。** 三条大表索引串行跑,单子可能挂几小时到一天。界面要让人看得出
   它在等什么、等了多久。
3. **授权边界放宽了。** 见决定 5。ADR 0011 要回写这一条。
4. **模式匹配认不出的写法会静默直发。** 缓解:阶段日志逐条写明走的哪条路,所以"本该走
   却没走"看得见,而不是悄悄发生。
