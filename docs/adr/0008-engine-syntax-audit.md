# ADR 0008:七个引擎的语法支持边界

- 状态:已采纳
- 日期:2026-08-28
- 相关:`backend/internal/gateway/risk.go`、`session_scope.go`、`dialect.go`、
  `realdb.go`、`pkg/sqlutil/plsql.go`

## 背景

一个用户报"DWS 的 `EXPLAIN PERFORMANCE` 没有显示结果"。查下来是:DWS 特有的裸选项
`PERFORMANCE` 没被识别,有效动词成了 `PERFORMANCE` 这个不存在的词,于是既不按读处理
(执行侧不取结果集),又按写判定(还可能被拦),而且 `planOnly` 仍是 true —— 它其实
会真的执行被包住的语句。

一个引擎特有的关键字没被认出来,同时造成了体验问题和判定被绕过。于是把同一类问题
在七个引擎(MySQL / PolarDB / TiDB / PostgreSQL / DWS / Oracle / MongoDB)上扫了一遍。

## 网关不解析 SQL,它只需要在三件事上不出错

这是这份自查的边界,也是本仓库一贯的立场:**网关不做完整 SQL 解析,也不该做。**
一个半吊子的解析器会在每个引擎的方言上出新的错,而它要回答的问题其实只有三个:

| 问题 | 判错的后果 |
|---|---|
| 这条语句**会不会返回结果集**? | 走 Exec 而非 Query —— 语句跑了,页面上什么都不显示 |
| 这条语句**需要什么权限**? | 无谓地拦人,或者放过不该放的 |
| 这条语句**会不会改数据**? | 安全洞 |

所以"支持某引擎的语法"在这里的准确含义是:**它日常会用到的语句,这三个问题都答对**。
不是"能解析它的全部语法"。

### 补记:第一个问题根本不该靠猜

自查提交之后,一句质疑把这份文档的结论往前推了一步:

> Web 命令行是直连数据库的,语法本来就由数据库校验,平台凭什么让一条合法命令
> "没有结果"?

问得对。平台从没做语法校验 —— 那条 DWS 的 `EXPLAIN PERFORMANCE` 确实合法、也确实
在服务端执行了,平台只是**没去取它的结果集**。而"这条语句返回不返回行",是三个问题
里唯一**不必靠平台判断**的那个:执行一次,数据库自己会说。

于是路由从两条变成三条:

| 情形 | 怎么走 | 为什么 |
|---|---|---|
| 已知的读 | Query | 照旧 |
| 已知的写 | Exec | 拿得到影响行数,写操作最该看到的就是它 |
| **动词表接不住** | **Query,由数据库定夺** | 有列就显示行,没列就如实说"执行成功" |

第三条把"补动词表"这件永远慢一步的事,变成了一次性的结构改动:再出现哪个引擎的
新方言关键字,结果集也不会再被丢掉。判定仍然对每条语句分类(那是产品存在的理由,
认不出来一律按最严的写来判,方向不变),变的只是**要不要取结果集**这一个决定。

**这里没有 Query 失败后回退 Exec 的逻辑,是有意的**:Query 报错时无法判断语句到底
执行了没有,再跑一次就可能是重复执行 —— 对一条 INSERT 来说,重复执行比报错严重得多。
报错就照实报错。

## 本次自查修掉的

| 引擎 | 问题 | 类别 |
|---|---|---|
| DWS | `EXPLAIN PERFORMANCE` 未识别 | 全部三类 |
| PostgreSQL / DWS | `EXPLAIN ANALYSE`(英式拼写)未识别 | 会改数据却判成读 |
| PG / DWS / MySQL 8 | `TABLE t`、`VALUES (…)` 不在读动词里 | 有结果集却走 Exec |
| PG / DWS | `FETCH` 不在读动词里 | 同上 |
| MySQL | `HELP '…'` 不在读动词里 | 同上 |
| PG / DWS / MySQL / Oracle | 会话级设置(`SET search_path`、`ALTER SESSION SET …`)按写/DDL 判 | 无谓拦人 |
| PolarDB | PostgreSQL 版被路由到 MySQL 驱动 | 连不上 |
| MongoDB | `getMore` / `listDatabases` / `dbStats` / `collStats` / `currentOp` / `watch` 判成写 | 无谓拦人 |
| Oracle | `BEGIN … END;` 匿名块(无 `/`)被按块内分号切碎 | 语句跑不起来 |

### 会话级设置为什么单列一档

`SET` 这个关键字底下藏着影响面完全不同的东西:

```sql
SET search_path TO dwd          -- 只影响这条连接
SET GLOBAL read_only = 0        -- 改整个服务器
SET PASSWORD FOR … = '…'        -- 改口令
SET ROLE dba_admin              -- 提权
ALTER SESSION SET …             -- 只影响这条连接
ALTER SYSTEM SET …              -- 改整个实例
```

所以不是"SET 放行",而是逐条认出**确实只作用于本会话**的那些(`SessionScoped`),
认不出来就当没放宽 —— 安全的方向。

放宽到什么程度也是有讲究的:它和 plan-only 的 EXPLAIN 用同一种短路,**包括跳过
高危字典**,理由一样 —— 字典匹配的是文本里的那个词,运维为了拦 `ALTER TABLE` 写下
"ALTER",不会是想连 `ALTER SESSION SET NLS_DATE_FORMAT` 一起拦掉。而在 Oracle 上,
切 schema 正是读数据的前置步骤,拦掉它只读用户就什么都干不了。

但闸还是要过:只读被明确拒绝的角色,连会话设置也不放行。**放宽的是走哪道闸,不是
不走闸。**

## 明确不支持的(有意为之)

| 情形 | 为什么 | 怎么办 |
|---|---|---|
| MySQL 的 `DELIMITER //` | 它是 mysql 客户端的指令,不是服务端 SQL,服务器本来就不认 | 去掉 DELIMITER,直接粘存储过程体 |
| Oracle 块后面还跟别的语句、且不带 `/` | 合并就会吞掉后面那条,而被吞掉的那条没人判过 —— 总纲是"过切安全,合并危险" | 按 Oracle 惯例加一行 `/`,部署脚本本来就这么写 |
| `COPY … TO STDOUT` 按写判 | 和 `COPY … FROM file` 是同一个动词,方向相反,判成写是安全的那一边 | 用导出通道 |
| `PRAGMA` 按写判 | `PRAGMA x` 读、`PRAGMA x = y` 写,同一个词 | —— |
| MySQL 的 `CHECK / CHECKSUM / OPTIMIZE TABLE` 按写判 | 它们是维护操作,不是查询 | —— |

## 合并 PL/SQL 块的代价,以及为什么可以接受

把块整体当一条语句,严格模式的"无 WHERE 变更"检查就读不到块里的 `DELETE` 了 ——
它读的是语句的首动词。这个代价不是新的(`DECLARE` 与 `CREATE PROCEDURE` 块早就
如此),而真正兜底的那层仍然有效:**高危字典对整块文本做正则扫描**,块里的 DROP、
DELETE 照样命中并转审批。已用测试钉住。

## 加新引擎时照着走一遍

1. `engineFamily` 认得出它吗?注意名字里同时带两种兼容标记的(PolarDB 就是)。
2. 它有没有**独有的关键字**会让 `ParseVerb` 解析出一个不存在的动词?
   (DWS 的 `PERFORMANCE` 就是这么漏的。)
3. 它有没有**返回结果集但关键字不叫 SELECT** 的语句?(`TABLE`、`VALUES`、`FETCH`)
4. 它有没有**看起来像读、其实会执行**的语句?(`EXPLAIN ANALYZE/PERFORMANCE`)
5. 它的会话设置长什么样?会不会被判成写或 DDL?
6. 它有没有块结构会被分号切碎?

对应的回归在 `internal/gateway/dialect_audit_test.go` 与 `pkg/sqlutil/plsql_begin_test.go`,
按引擎分组,新引擎往里加一行即可。
