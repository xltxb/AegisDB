# ADR 0022:移除 SQLite(存储库与目标库),后端测试改跑 PostgreSQL

- 状态:已采纳
- 日期:2026-11-26
- 相关:ADR 0016、ADR 0020、`backend/internal/testpg/`、`backend/migrations/pg/`、`backend/internal/model/rule_ref.go`

## 背景

SQLite 在这个项目里有两个角色:本地开发 / 测试的存储库,以及一种很少用的目标库。生产早已只用 MySQL / PostgreSQL
(ADR 0020)。保留它的代价:

- 测试跑的是 GORM AutoMigrate 生成的表,不是生产执行的 SQL 迁移。ADR 0016 记录的几次线上迁移事故(保留字、排序规则、
  列宽)都是 SQLite 测试看不出来的。
- SQLite 单写者的行为与生产不同,历史上多次出现"本地被压住、生产才暴露"的问题。

## 决定

### 一、存储库只剩 MySQL 与 PostgreSQL

- dev(及未识别的环境名)默认存储库改为 PostgreSQL。`AEGISDB_DB_DRIVER` 只认 `mysql|postgres`;配置里写 `sqlite`
  启动即报错并说明已不再支持。`SQLitePath`、`auto_migrate` 的建表作用、`run-sqlite.bat` 一并删除。
- `Migrate` 没有 AutoMigrate 回落:任何环境的表结构都只来自 `migrations/`(MySQL)与 `migrations/pg/`(PostgreSQL)。
- 本地开发:Windows 用 `run-pg.bat`(Docker 容器);macOS / Linux 装 PostgreSQL 后设 `AEGISDB_POSTGRES_DSN`。
  不提供 SQLite 开发库的搬迁工具,旧开发库的数据不保留。

### 二、目标库不再支持 SQLite

- 后端引擎族、驱动、缓存与风险规则里的 SQLite 分支删除,`go.mod` 去掉 `glebarez/sqlite`、`glebarez/go-sqlite`、
  `modernc.org/sqlite` 及其独占的间接依赖。前端引擎识别、界面分支与词条同步删除。
- **存量连接不删不改。** `engine` 含 sqlite 的连接仍留在列表里,但连接、执行、导出、测试连接都以
  「不支持的数据库引擎」被拒,不 panic;新建 / 编辑时不能再选。其它不属于 MySQL / PostgreSQL / Oracle 的引擎同理。
- `model.RuleSQLiteFileOp`(`sqliteFileOp`)常量及其文案**保留**,只删判定逻辑:历史审计行里存着这个规则键,
  删掉它旧记录会显示成原始键。
- `/open/sql-review` 不再接受 `dialect=sqlite`。
- 无法真实执行的连接(含上述存量 SQLite 连接),导出在生产环境直接失败,不再生成模拟数据行。

### 三、后端测试改跑 PostgreSQL

- 新包 `internal/testpg`(只被 `_test.go` 引用):测试进程用本机 PG 程序 `initdb` 到临时目录,只监听
  127.0.0.1 TCP 启动临时实例,每个用例从"只迁移"或"迁移 + 播种"模板库 `CREATE DATABASE … TEMPLATE` 克隆独立库,
  结束时只删自己建的 `aegis_t_*` 库。把 SQLite 当目标库的用例改以同一实例上另建的 PG 库为目标库,断言的网关行为不变。
- 覆盖变量:`AEGISDB_TEST_PG_BIN` 指定 PG 程序目录;`AEGISDB_TEST_STORE_PG`(libpq 键值串,不含 dbname)指向现成实例,
  此时不再起临时实例。
- 只测 SQLite 专属行为的用例(如 ATTACH / VACUUM INTO 被拦)随功能删除。

## 后果

- **跑后端测试需要 PostgreSQL ≥ 13 的程序**(`initdb` / `pg_ctl` / `postgres`;13 起才有 `DROP DATABASE … WITH (FORCE)`)。
  找不到程序又没设覆盖变量时测试**失败**而不是跳过,报错写明安装方法。Windows 开发者需先装 PG 官方包。
- 测试跑的是生产同一套 PG 迁移,模型与迁移的漂移会直接暴露;以迁移为准修正。
- 每个含数据库用例的测试包各起一次临时 PG(约 1–2 秒)。
- 升级前创建的 SQLite 目标连接会一直显示在列表里却不可用,需要管理员自行删除。
- 历史迁移 SQL 里提到 SQLite 的注释、较早的 ADR 与阶段性报告保持原样,它们记录的是当时的事实。
