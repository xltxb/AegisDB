# ADR 0019:SQL 解析器只叠加,不替代现有判定

- 状态:已采纳
- 日期:2026-11-05
- 相关:`backend/internal/sqlparse/`、`backend/third_party/omni/`、`.scratch/sql-parser/PRD.md`、
  `.scratch/sql-parser/corpus-report.md`、ADR 0018

## 背景

网关的分句、风险判定、规范检查、在线变更识别都靠手写扫描和正则,边角写法容易误判。用户要求 TiDB、MySQL、Oracle、DWS 都用上各自的解析器。

## 决定

### 一、解析器的结论只能让安全判定变严

解析成功且更严:可以取更严。解析成功但更松、解析失败、解析出 0 条语句:维持现有判定。要放宽某类判定,必须单独开工单,列出语句类型并补测试。

规范检查没有这个限制,解析成功就用 AST,失败退回正则。规范检查判错只影响提示。

**以后不要顺手把判定改成只信解析器。** 解析器读的是标准语法,不是目标服务器的读法,语料库里已经有这些证据(`TestPinnedBypassSamples` 钉住):

| 语句 | 解析器 | 实际 |
|---|---|---|
| `/*M! DROP TABLE t */` | 0 条语句 | MariaDB 执行 DROP |
| `SELECT 1 /*M! INTO OUTFILE '/tmp/a' */` | 只读 | MariaDB 写文件 |
| `SET STATEMENT a=1 FOR DROP TABLE t` | 语法错误 | MariaDB 执行 DROP |
| `DELETE FROM t WHERE 1=1` | 有 WHERE | 删全表 |
| `SELECT SLEEP(5)`、`SELECT f()` | 只读 | 有副作用 |

反过来,解析器也确实能补现有判定的漏:语料库第一次跑就发现 `SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '` 现有判定没看出来写文件。所以两边都要,取更严。

### 二、先分句,再解析

`SplitForJudge` 会解开 `/*M!` 注释,解析器在它之后才看得见注释体(`TestSplitBeforeParseSeesMariaDBCommentBody`)。接入调用方时不能把顺序倒过来,也不换掉分句器:它涉及 PL/SQL 块、`DELIMITER`、SQL*Plus 命令。

### 三、选型

| 引擎 | 解析器 | 为什么 |
|---|---|---|
| TiDB | `pingcap/tidb/pkg/parser` | 官方,已经在依赖树里 |
| MySQL | `bytebase/omni/mysql/parser` | 认触发器;TiDB parser 不认 |
| Oracle | `bytebase/omni/oracle/parser` | 手写,无 cgo,认包和包体 |
| DWS | PostgreSQL 解析器 fork 后扩展 | 没有现成的,见工单 05 |

MariaDB、PolarDB 走 MySQL 方言。MariaDB 专有语法(`SET STATEMENT`、`ADD COLUMN IF NOT EXISTS`)解析失败,退回现有逻辑。

### 四、omni 放精简副本,不直接依赖

直接 `require` 会把 `go-sql-driver/mysql`、`go-ora`、`x/crypto`、`x/net` 一起升级,Go 版本也要到 1.25.7,而解析器一个都用不到。升级数据库驱动是另一件事,不能夹带。副本只留四个目录,没有依赖,`go` 指令写 1.23.0,细节见 `backend/third_party/omni/README.md`。

代价是升级要手动拷贝,而且上游的 `go` 指令以后可能用上 1.23 没有的语法,到时再决定。

### 五、解析器要有界

调用方在请求路径上,所以 `Parse` 限制长度(1 MiB)和时间(2 秒),最多 64 个解析同时在跑,名额满了不解析。超时的 goroutine 杀不掉,让它跑完退出。

Oracle 解析器在 5 万层 `CASE` 上要 11 秒。Go 的栈溢出 `recover` 接不住,所以嵌套深度靠解析器自己的上限(omni 有 `maxNestingDepth`),`TestHostileInput` 钉住。

## 后果

- 第一批只加包和语料库,调用方都没接,行为不变。
- 语料库升级解析器时要重跑。解析器结论变了,`TestPinnedBypassSamples` 会失败。
- 判定路径接入解析器之前,先只比对不生效(工单 08)。
