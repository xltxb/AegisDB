# 21 · Oracle 表结构要完整:索引 / 约束 / 注释 / 表空间

Status: ready-for-human（已实现，**未在真实 Oracle 上验收**）

关联 issue 16(表 DDL 通道本身)。

## 需求

"oracle 表结构查看，要显示完整信息，包含索引，约束，comment，表空间等"。

## 问题在哪(三处，不是一处)

1. **权威路径本身就不完整。** `DBMS_METADATA.GET_DDL('TABLE', …)` 带列、约束、
   表空间、存储参数和分区,但**不带索引、不带注释** —— 这两类在 Oracle 眼里是
   "依赖对象",归 `GET_DEPENDENT_DDL` 管。所以即使权限齐全,看到的 DDL 也缺了
   看表结构时最常被问的两件事。
2. **降级路径几乎什么都没有。** 只读 `ALL_TAB_COLUMNS` 的 column_name / data_type /
   data_length / nullable,于是 `NUMBER(10,2)` 显示成 `NUMBER`、`VARCHAR2(50)` 显示成
   `VARCHAR2`,默认值、约束、索引、注释、表空间全部没有。生产上的账号常常执行不了
   DBMS_METADATA(它要 SELECT_CATALOG_ROLE 或对象属主权限),所以这条路才是很多人
   实际看到的那一条。
3. **失败原因被吞掉。** 原代码是 `if err := …; err == nil { return }`,GET_DDL 为什么
   不能用(权限不足?对象不存在?)没有任何地方说,人只能看到一份莫名其妙变简陋的输出。

## 实现

`backend/internal/gateway/oracle_tableddl.go`(新)+ `objects.go` 的 `oracleTableDDL`。

**权威路径**:GET_DDL 之后补两次 `GET_DEPENDENT_DDL`('INDEX' / 'COMMENT')。没有索引
或没有注释时 Oracle 抛 ORA-31608 而不是返回空,所以这里的错误当"本来就没有"处理,
不上报。

**降级路径**:从数据字典重建,覆盖

| 内容 | 视图 |
|---|---|
| 列(精度/标度/字符长度/CHAR 语义/默认值) | `ALL_TAB_COLUMNS` |
| 表空间 / 临时表 / IOT / 是否分区 | `ALL_TABLES` |
| 分区方式 · 分区键 · 分区数 | `ALL_PART_TABLES` / `ALL_PART_KEY_COLUMNS` / `ALL_TAB_PARTITIONS` |
| 主键 / 唯一 / 外键(含 ON DELETE)/ 检查约束 | `ALL_CONSTRAINTS` + `ALL_CONS_COLUMNS` |
| 索引(唯一 / BITMAP / DESC / 函数表达式 / 表空间 / 状态) | `ALL_INDEXES` + `ALL_IND_COLUMNS` + `ALL_IND_EXPRESSIONS` |
| 表注释 / 列注释 | `ALL_TAB_COMMENTS` / `ALL_COL_COMMENTS` |

三个刻意的取舍:

- **每段独立可失败。** 只被授予部分字典视图的账号很常见,读不到约束不该让整张表的
  结构一起消失。失败的段落降级成一行 `-- 约束未列出:…(ORA-00942)` 留在输出里 ——
  缺口要**看得见**。方向与规则判定层的"查不到就拒绝"相反,是故意的:这里是只读展示,
  不是闸。
- **LONG 列有退路。** `DATA_DEFAULT`、`SEARCH_CONDITION`、`COLUMN_EXPRESSION` 都是
  LONG,驱动/版本组合上可能读不回来;读失败就退到不含该列的查询,而不是让整段消失。
- **NOT NULL 的系统检查约束被过滤掉。** 它已经写在列定义上,再列一遍
  `CHECK ("X" IS NOT NULL)` 会把真正的业务约束淹掉;复合条件里的 IS NOT NULL 不会被
  误伤(有测试)。

失效的约束(`DISABLED`)和不可用的索引(`UNUSABLE`)会带状态标注 —— 它们看起来和正常
的一模一样,但什么都不保证。

## 前端

只改了 DDL 查看器宽度:`min(760px, 92vw)` → `min(1040px, 94vw)`,`max-height` 78vh →
86vh。一条带属主的 `ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY … REFERENCES …` 有
一百二十来字符,760px 下会被折断在标识符中间。高亮器本来就认 `--` 行注释,未改。

## 回归测试

`backend/internal/gateway/oracle_tableddl_test.go`(新,6 组):类型还原(NUMBER 精度/
标度、VARCHAR2 的 CHAR 语义、TIMESTAMP、CLOB)、NOT NULL 自动约束的识别边界、完整
渲染(表空间/分区/主键/外键 ON DELETE/检查约束/DESC 索引/函数索引/唯一索引/状态标注/
单引号转义/列注释)、降级说明与缺口 Note、只有列时的最小输出、标识符与字面量转义。

取数与渲染**故意分开**:仓库里没有真实 Oracle(唯一的真库是 SQLite,`dev-oracle` 是
host 10.0.0.9 的演示连接),渲染做成纯函数是唯一能把"输出到底长什么样"钉住的办法。

后端全量:gateway / handler / service / repository / review 通过,bootstrap 的
`TestObjects*` 通过。前端 `npm run build` 通过。

## 待真实环境验收

- **所有 SQL 都没有在真实 Oracle 上跑过。** 尤其:`(rc.owner, rc.constraint_name) IN
  (SELECT …)` 的多列 IN 子查询、`ALL_PART_KEY_COLUMNS.name`(它用 name 不是 table_name)、
  以及三个 LONG 列在 go-ora 下的实际行为。
- 权限受限账号上,哪些段落会真的降级(这正是这次改动想让人看见的东西)。
- 12c+ 的 `SEARCH_CONDITION_VC` 没有用,因为 11g 没有这一列;若确认只跑 12c+,可以换掉
  LONG 那条退路。

## 没有做

- **触发器**。`GET_DDL('TABLE')` 不含触发器,加 `GET_DEPENDENT_DDL('TRIGGER')` 只需一行,
  但降级路径要读 `ALL_TRIGGERS`(又一个 LONG 的 trigger_body),两条路一边有一边没有比
  两边都没有更糟。要加就两条一起加。
- 终端里的 `DESC <table>`(`oracle_sqlplus.go`)没动 —— 它是**刻意**对齐 SQL*Plus 的
  DESC 输出的,不是"表结构查看"这条路。
