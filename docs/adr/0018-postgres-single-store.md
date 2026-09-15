# ADR 0018:网关自身的存储收敛到 PostgreSQL 一种

- 状态:已采纳
- 日期:2026-09-15
- 相关:`backend/internal/bootstrap/db.go`、`migrate.go`、`config.go`、
  `backend/internal/bootstrap/automigrate_guard_test.go`、
  `backend/internal/testsupport/db.go`、`backend/migrations/0001_init.sql`、
  `docker-compose.yml`、`backend/run-dev.sh` / `run-dev.bat`;取代 ADR 0016 的欠账一节

## 背景

网关**自己的**元数据库从前有两条路:开发与测试跑纯 Go 的 SQLite(双击一个 bat 就起来,
零依赖),生产跑 MySQL 8。`APP_ENV` 顺手兼了两件事 —— 它既选部署档位,又选存储引擎。

于是 schema 也有了两个权威源:SQLite 那边由 GORM 的 `AutoMigrate` 照 `db.go` 里一份
手工维护的 `allModels` 建表,MySQL 那边由 `migrations/*.sql` 建表。**两份定义,各自演进**,
而没有任何东西比对过它们。

`db.go` 里记着这笔账最终收到的账单:

> `tier_code` 改名只做在一边。AutoMigrate 在已经填好的 `env` 列旁边,照模型又建了一个
> **空的** `tier_code`。而空值在两处规则查询里都读作「放行」(`CapabilityLevel` → allow、
> `matchCommand` → off)。网关于是带着一套失效的管控上线,**健康检查全绿**。

这不是"两边不一致"那么轻。一个管控网关最不该有的失败方向,就是悄悄朝"放行"倒 —— 而
双权威源恰好是造出这种失败的机器:两边都合法、都能启动、都不报错,差别只在真正下判断
的那一刻才显出来。

ADR 0016 记了同一台机器的另一面:因为 SQL 迁移在测试里**从没被执行过**,每个迁移文件的
第一次执行就是生产 `migrate` 的那一刻。一周之内被咬两次(1267 排序规则、1064 保留字)。
那篇 ADR 当时写下的"没有做的事"是:

> 在 CI 里拿真的 MySQL 跑一遍迁移。……没做的原因是当前没有可用的 CI MySQL 实例,而在
> 本机引入一个会破坏"零依赖启动"。

也就是说,**零依赖启动这个卖点,是那三道静态闸和那两次生产故障的直接成本**。本 ADR 是
去把这笔账付掉。

## 决定

网关自身的存储只有 PostgreSQL 一种,开发、测试、生产都是它。

### 1. `APP_ENV` 不再挑引擎

它只剩部署档位的职责:演示数据(仅 dev)、生产的 JWT 强度校验、CORS 与出站 SSRF 的松紧。
`VELA_DB_DRIVER` 这个开关连同它的歧义(yaml 里的 `database.driver` 明明写着却总被覆盖)
一并删除。配置里 `mysql_dsn` + `sqlite_path` + `auto_migrate` 三个键合成一个 `postgres_dsn`,
环境变量覆盖键是 `VELA_PG_DSN`。

### 2. schema 只由 `migrations/*.sql` 定义

`AutoMigrate` 整条路拆掉,`allModels` 随之消失。`automigrate_guard_test.go` 盯着 `db.go`
里不再出现 `allModels` / `autoMigrate` / `shouldAutoMigrate` —— 这条路走回来的成本太低,
得有个东西拦着。

`internal/model` 里的 GORM 标注**不再用于建表**。建表形状的那几个(`type:` / `size:` /
`index:` / `uniqueIndex:`)从此谁也不读,只有读写行真正需要的那几个还在干活
(`column:` / `primaryKey` / `autoIncrement` / `default:` / `-`)。注解写错照样
是错:一个读文档的人会据此认为某处有唯一性,而库里没有。所以这次把它们与 baseline SQL
对齐了(`idx_meta_table_uniq` / `idx_meta_col_uniq` 补上 UNIQUE,SQL 里从来不存在的
`idx_meta_table_scope` 去掉)。**两者不一致时,SQL 是对的,标注是 bug。**

### 3. 44 个增量迁移压成一份 baseline

`0001_init.sql`,整体幂等(36 条 `CREATE TABLE IF NOT EXISTS` + 48 条
`CREATE [UNIQUE] INDEX IF NOT EXISTS`),重跑只出 NOTICE。压缩的权威源是**那些 SQL 文件**,
不是模型 —— 上面那笔账说的正是模型会漂。

代价是:那 44 个文件里附带的运维知识跟着没了(DEPLOY.md 里"某个多条 ALTER 的迁移跑到
第 5 条炸了怎么办"那套手工恢复步骤)。那套步骤已从文档里删掉,因为它指着的文件只在 git
历史里 —— 让运维照着去找一个不存在的文件,比不写还糟。

### 4. 迁移串行化换成 advisory lock

`GET_LOCK('vela_schema_migrate', 60)` 换成 `pg_try_advisory_lock(hashtext(...))`。用 `try`
而不是阻塞版:拿不到锁就**当场退出并说明另一处正在迁移**,而不是把部署钩子静静挂住 60 秒
再报一个没头没脑的超时。锁是会话级的,所以它固定在一条专用 `*sql.Conn` 上持有到结束 ——
`SetMaxOpenConns(1)` 保证不了连接的**同一性**,池里换一条连接就等于悄悄放了锁。

### 5. 目标库驱动一个字没动

**这件事只关于网关自己那一个库。** 被纳管的目标库能力完全不变:MySQL / MariaDB / TiDB /
PolarDB、PostgreSQL / DWS / GaussDB、Oracle 七种引擎照旧,`internal/gateway/realdb.go`
没有改动,SQLite 作为目标库驱动也还在。四份规范派生的 87 条规则、按引擎的方言判定、
元命令翻译,全部照旧。

把这句写进 ADR 是有必要的:"网关不再支持 MySQL"这句话有两种读法,而其中一种等于把产品
的核心能力删掉。

## 代价 —— 明确写下来

**零依赖启动没有了。** 这是本次决定付出的唯一一笔真钱,而且它落在每天都要付的地方:

- 跑服务要先 `createdb vela_gateway`。
- **跑测试要先 `createdb vela_test`**,而且必须有一个能连上的本机 PostgreSQL。
- 双击一个 bat 就能看到界面的那条路没了。`run-sqlite.bat` / `run-mysql.bat` 换成
  `run-dev.sh`(macOS/Linux)与 `run-dev.bat`(Windows),两者都假设库已经建好。

换回来的是:**`migrations/` 里的 SQL 现在每跑一次测试就被真的执行一遍**。测试用
`internal/testsupport` 在真实 PG 上各建一个独占 schema(不是各建一个 database ——
`CREATE DATABASE` 要复制模板库,几百个测试会把时间拖成分钟级),跑完删掉,可并行。
连不上是**失败不是 skip**:静默跳过的测试是假绿,而这套测试的全部价值就在于它们真的
对 PostgreSQL 跑过。

ADR 0016 那笔欠账到此结清 —— 不是靠搭一个 CI MySQL,而是靠取消"测试跑的库和生产跑的库
不是同一种"这个前提本身。那三道静态闸仍然留着,但性质变了:它们从"唯一的防线"退成
"在写下 SQL 的那一刻就报错,而不是等测试跑到那里"。`TestMigrationsDeclareCollation`
随 MySQL 一起退役(PG 没有 per-table charset/collation 这个坑),保留字表换成 PostgreSQL 的
—— 两边的保留字并不重合,而不重合的那部分正好最容易踩:`user` / `order` / `limit` /
`offset` / `authorization` 在 MySQL 里做列名合法,在 PG 里全是保留字。

## 存量数据

本次迁移**没有存量数据要搬** —— 生产尚未上线,开发库是演示数据,直接重建。所以这里
没有写数据迁移脚本,也没有双写或回切方案。

真要搬旧库的话,审计哈希链是需要单独决定的那一部分:见 ADR 0019。
