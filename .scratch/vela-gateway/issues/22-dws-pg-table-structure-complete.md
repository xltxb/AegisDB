# 22 · DWS / PostgreSQL 表结构要完整:约束 / 注释 / 表空间 / **分布键 · 行列存**

Status: ready-for-human（已实现，**未在真实 DWS 或 PostgreSQL 上验收**）

关联 issue 16(表 DDL 通道)、issue 21(Oracle 的同一件事)。

## 需求

"dws 表结构查看也一样需要完整的信息"。

## 问题在哪

`pgTableDDL` 原来只做两件事:`information_schema.columns` 拼 CREATE TABLE,再补
`pg_indexes.indexdef`。于是**约束、注释、表空间、存储参数全都没有**。

对 DWS 来说还少了两件更要命的、原生 PostgreSQL 上根本不存在的东西:

- **分布键**(`DISTRIBUTE BY HASH / REPLICATION / ROUNDROBIN`)。它决定数据怎么散在
  各 DN 上,是 DWS 上性能问题的第一来源。一份不写分布键的"表结构",在 DWS 语境下
  不叫完整。
- **行存还是列存**(`reloptions` 里的 `orientation`)和压缩级别。

另外 `information_schema` 那条路本身也拼不准:它把类型和长度拆成两列,重新拼出来的
只有 `character varying(100)` 这一档,`numeric(10,2)`、数组、自定义类型都还原不了。

## 实现

`backend/internal/gateway/pg_tableddl.go`(新)+ `objects.go` 的 `pgTableDDL`。

改读 `pg_catalog`,每一项都让 PostgreSQL 自己渲染,不自己拼:

| 内容 | 来源 |
|---|---|
| 列类型(含长度/精度/数组/自定义类型) | `format_type(atttypid, atttypmod)` |
| 默认值 | `pg_get_expr(adbin, adrelid)` |
| 约束(主键/唯一/外键/检查) | `pg_constraint` + `pg_get_constraintdef(oid)` |
| 索引 | `pg_indexes.indexdef` |
| 表注释 / 列注释 | `obj_description` / `col_description` |
| 表空间 / 存储参数 / UNLOGGED / 临时表 | `pg_class` + `pg_tablespace` |
| **分布方式与分布列(DWS)** | `pgxc_class.pclocatortype` + `pcattnum` |
| 分区 | `pg_get_partkeydef`(PG10+)/ `pg_partition`(DWS) |

以上除 `pgxc_class` / `pg_partition` 外,PG 9.2 起都有 —— DWS/GaussDB 在这条线上。

三个刻意的取舍:

- **主键/唯一约束自带的同名索引不重复列。** 它们在约束段已经说清楚了,索引段再列一遍
  会让人以为索引比实际多一倍。pg_dump 也是这么分的。
- **`pgxc_class` 查不到时静默跳过,不记 Note。** 它在原生 PostgreSQL 上根本不存在,
  那条查询**必然**失败 —— 给原生 PG 的用户看一行"分布信息读取失败"是纯粹的噪音。
  这与"缺口要看得见"不矛盾:那条原则针对的是**本该有却没读到**的东西。
- **`information_schema` 那条老路留作退路**,不是删掉。pg_catalog 是主路;万一某个
  发行版/权限组合下它走不通,退回去至少还能给出列,而不是给出一个"查不到"。退路的
  输出会写明**为什么退到了这里、以及少了什么**。

`sqlIdent` / `sqlLit` / `qualifiedName` / `errBrief` 从 issue 21 的 Oracle 文件里提出来
共用(ANSI 引用规则两边一样,各写一遍就会各错一遍)。

## 输出长这样(DWS)

```
-- 表空间:tbs_app   存储:orientation=column, compression=low   分布:HASH("id")   分区:RANGE (created_at) · 12 个分区

CREATE TABLE "public"."t_order" (
  "amount" numeric(10,2) DEFAULT 0 NOT NULL,
  ...
)
WITH (orientation=column, compression=low)
TABLESPACE "tbs_app"
DISTRIBUTE BY HASH("id");

-- 约束 / -- 索引 / -- 注释
```

## 回归测试

`backend/internal/gateway/pg_tableddl_test.go`(新,6 组):完整渲染、原生 PostgreSQL
(没有分布键/存储参数时那几行不该出现)、UNLOGGED 与临时表、约束后盾索引不重复、
分布类型字母到子句的映射(含"认不出来的原样带出")、缺口 Note 的位置。

取数与渲染**故意分开**,理由同 issue 21:仓库里没有 PostgreSQL 也没有 DWS。

## 待真实环境验收

- **所有 SQL 都没有在真实 DWS / PostgreSQL 上跑过。** 重点:
  - `pgxc_class.pcattnum::text` 的实际文本形态(按空格分隔的属性号假设);
  - `pclocatortype` 的字母在你们那版 DWS 上的取值(H/R/N 之外的);
  - `pg_get_partkeydef` 在 DWS 上不存在时是报错还是别的(现在按报错静默跳过);
  - `array_to_string(c.reloptions, ', ')` 在列存表上的实际内容。
- lib/pq 在**非事务**下单条语句失败不会污染连接,所以这几条"试着查"的语句是安全的;
  若将来把这段包进事务,必须改成先探测存在性。

## 没有做

- 触发器、序列/自增(`GENERATED … AS IDENTITY`)、继承表、外表。理由同 issue 21:
  要加就主路和退路一起加。
