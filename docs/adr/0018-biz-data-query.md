# ADR 0018:业务方数据查询 —— 第三个读行出口与授权来源

- 状态:已采纳
- 日期:2026-09-27
- 相关:`backend/internal/gateway/bizquery.go`、`backend/internal/gateway/backslash.go`、
  `backend/internal/service/bizquery_exec.go`、`backend/internal/service/query_grant.go`、
  `migrations/0043_query_grant.sql`、`migrations/0044_query_grant_approval.sql`、
  `.scratch/biz-data-query/PRD.md`、ADR 0006(开放接口)、ADR 0009(服务端脱敏)

## 背景

业务方(运营、财务、客服、法务)要查生产数据:只读、只查批给自己的库、有行数上限、超时自动取消。
授权由外部审批系统审批后经开放接口下发。完整需求见 PRD,这里只记下与既有 ADR
有冲突、或以后容易被"顺手改掉"的几个决定。

## 决定

### 一、`RealBizQuery` 是第三个读行出口(修订 ADR 0009)

ADR 0009 说全网关只有两处把行读出来(`RealRun`、`RealQueryEach`),脱敏只放那两处,新调用路径
天然被覆盖。业务查询没有复用它们,而是新开了 `gateway.RealBizQuery`,因为它要的东西那两处都给不了:

| 需要 | 为什么 `RealRun` 给不了 |
|---|---|
| 独占一条物理连接 | 会话变量(`max_execution_time`)、只读事务、KILL 看守都得落在同一个会话上,用完还要把会话变量恢复原值再还回连接池 |
| 只读事务 | `START TRANSACTION READ ONLY` / `BEGIN READ ONLY` / `SET TRANSACTION READ ONLY` —— 判定层之外那道由**数据库本身**把关的闸 |
| 读到 n 行即停并标记截断 | `RealRun` 的截断是终端的固定 200 行 |

**脱敏不变量仍然成立**:`RealBizQuery` 读完行后调用的是与 `RealRun` 同一个 `maskResultSet`,规则同一来源
(`SensitiveRulesProvider`),业务方没有任何原值旁路 —— 导出也走这一个出口。以后再加读行出口,
必须同样调用 `maskResultSet`,并在 ADR 0009 的出口表里登记。

**打码兜不住的形态直接拒绝(2026-09-28 补)**:`maskResultSet` 按选择列表的文本比对,兜得住
`t AS x`、`UPPER(t)`,兜不住经子查询 / UNION / CTE 改名后带出的敏感字段(`SELECT x FROM (SELECT
id_card AS x FROM t) s` 在真实 SQLite 上原值回传)。终端对此的态度是"顺手看到,可接受",导出则
改走审批(`MaskEvasionColumn`,见 ADR 0009)。业务查询两条路都不适用:它没有审批,而业务方拿到的是
可以导出的生产数据。所以判定层在只读检查之后调用同一个 `MaskEvasionColumn`,命中即拒绝
(`bizSensitiveEvasion`,40300,写审计,风险记高)。它只拦"文本里提到了敏感字段、又用了会改列名的
句式"那一种,与敏感字段无关的 CTE / 子查询照常执行。

**Oracle 的 EXPLAIN PLAN(2026-09-28 补)**:它要往 PLAN_TABLE 写计划,在只读事务里报 ORA-01456;看计划
又要调 DBMS_XPLAN,而判定层不放行任何 DBMS_ 包。执行层代为完成:判定层只认 `EXPLAIN PLAN FOR <只读查询>`
(`bizExplainForm`),执行层在同一会话的读写事务里用网关生成的 statement_id 写计划、`DBMS_XPLAN.DISPLAY`
读出、回滚。被解释的语句只解析不执行,事务里唯一的写是 PLAN_TABLE(会话级临时表)那几行,读完即回滚。

**被取消的连接不还回池子(同日)**:业务查询独占一条连接;超时 / 取消后 go-ora 的 break 会在连接上留下
ORA-01013,还回池子就落到下一个借到它的人(可能是别人的终端语句)头上。现在被打断的连接一律丢弃。

**从库(2026-09-29 补)**:数据源可配从库(`replica_*` 四列,迁移 0047)。配了之后数据查询的全部流量 ——
执行、导出、判定用的库名、库表下拉 —— 都打到从库(`service.bizTarget`);终端、审批执行、发布不走这条路,
它们要写,或要读刚写下的数据。从库连不上时回落主库(可用性优先),但只在"连不上"时回落:连上了、查询本身
报错或超时照实报错,把一条在从库上超时的慢查询再拿去主库跑一遍,恰好是配从库要避免的事。探测结果短暂缓存
(通 10 秒、不通 30 秒),从库挂了的时候不让每条查询都先等满 3 秒探测。由谁提供写进结果(`served`)和审计
operator。

### 二、字面量边界有歧义的语句,业务查询直接拒绝

网关的词法按 SQL 标准切字符串(反斜杠是普通字符),而 MySQL 系默认把 `\'` 当转义。两种理解下
"哪些是数据、哪些是语法"不同时,子查询可以被伪装成字符串,跨库 / 系统库 / 危险函数检查全部失明
(已在真实 MySQL 上复现)。业务查询**不猜**目标库的 sql_mode:两种理解不一致即拒绝
(`BackslashAmbiguous`)。终端不能这样一刀切(`'O\'Brien'` 是正常写法),改为两种理解都判、取更严
(`sqlReadings`)。

### 三、查询权只来自授权,授权只来自外部审批

业务查询角色在能力矩阵里写满 deny、挂占位标签(本系统"查无行 = 放行""无标签 = 不限实例"),
查询权只来自 `tbl_query_grant`。授权按 `externalId`(审批单号)幂等:同单重发不改、新单更新、
已被取代的旧单拒绝(`tbl_query_grant_approval`)。开放接口上一个外部系统只能看到、撤销自己下发的授权。

### 四、审计 operator 带执行统计(扩展 ADR 0006 的 operator 用法)

ADR 0006 定义 operator 为"授权者"(`API:<凭据名>`)。业务查询的执行 / 导出审计把统计附在 operator 上:
`查询授权 #12 · 20 行(已截断)· 19ms · 导出 CSV`。

- 为什么不加列:operator 已在审计哈希链的载荷里,事后改不了;为几项统计给哈希链表加列,要同时改
  载荷与校验,不值得。
- 仍然以"授权者"开头:`查询授权 #id` 回答的正是"凭什么执行",后面的统计是附注。
- 开放接口的下发 / 撤销审计照 ADR 0006,operator 为 `API:<凭据名>`。

## 后果

- 读行出口从两个变成三个。新增读行路径时,检查清单多一项:调用 `maskResultSet` 并登记。
- 业务方写 `'O\'Brien'` 会被拒,提示改用 `'O''Brien'`。
- 审计 operator 列的内容不再只是一个名字;按 operator 精确匹配授权者的查询要改用前缀匹配。
