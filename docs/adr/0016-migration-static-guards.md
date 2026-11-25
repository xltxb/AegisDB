# ADR 0016:迁移的 MySQL 专属缺陷,只能靠静态闸挡住

- 状态:已采纳
- 日期:2026-09-10
- 相关:`backend/internal/bootstrap/migration_tables_test.go`、
  `migration_collation_test.go`、`migration_reserved_words_test.go`、
  `backend/internal/bootstrap/migrate.go`

## 背景

开发和测试环境跑的是 SQLite,生产跑的是 MySQL 8。这不是一个可以顺手改掉的选择 ——
本地零依赖启动(双击 `start-dev.bat`)是这个项目一开始就定下的事。

代价是:**`migrations/` 里的 SQL 从来没有被任何一套测试真正执行过**。SQLite 那边走的
是 GORM 的 AutoMigrate,根本不读这些文件。于是每个迁移文件的第一次执行,就是在生产上
第一次 `migrate` 的那一刻,而那时人已经在部署窗口里了。

一周之内被咬了两次:

| 时间 | 症状 | 成因 |
|---|---|---|
| 2026-09-09 | `ERROR 1267 Illegal mix of collations`,界面上表现为"拦截次数 0" | `0001_init.sql` 建表只写了 `CHARSET`,没写 `COLLATE`;MySQL 在这里不继承库的排序规则 |
| 2026-09-10 | `ERROR 1064`,`migrate` 停在 0035 第 3 条语句 | 列名叫 `databases`,那是 MySQL 8 的保留字;SQLite 接受这个名字 |

两次的共同点值得单独写下来:

- **SQLite 不会报**。第一件事它没有排序规则这个概念,第二件事它接受那个列名。整套测试
  跑绿,说明不了任何事。
- **第一件事根本不报错**。建表、插入、查询全部成功,直到某天有人写下第一条跨那两批表的
  JOIN;而那次错误还在 handler 里被 `_` 吞掉,界面上只是一个 0 —— 没人会把 0 当故障。
- **迁移中途失败没有事务**。见 `migrate.go`:语句逐条执行,`schema_migrations` 那一行
  **只在全部成功之后**才写。所以失败的迁移不算已应用,下次 `migrate` 会从第一条重跑 ——
  这既是好事(可以就地改这个文件,不必补一个新的),也是陷阱(**每一条语句都必须可重跑**)。

## 决定

### 一、在写下 SQL 的那一刻挡住

既然没法在测试里执行这些 SQL,就把已知的坑做成对**文本**的检查。现有三道:

| 闸 | 挡什么 |
|---|---|
| `TestMigrations_AlterTargetsExistingTables` | `ALTER TABLE` 改一张没建过的表 |
| `TestMigrationsDeclareCollation` | 建表不写 `COLLATE`(1267 的来源) |
| `TestMigrationsAvoidReservedWords` | 列名/表名是 MySQL 保留字(1064 的来源) |

每一道都验过"会咬人":临时把缺陷塞回去,确认当场失败并给出可照做的提示,再撤销。
**一条从没失败过的守卫,和没有守卫是一回事。**

每一道也都带一个"解析到 0 个东西就 Fatal"的自检 —— 正则一旦失配,这类测试会静静地
检查空集然后永远绿着,那比没有更糟。

### 二、保留字要换名字,不是加反引号

`` `databases` `` 在 MySQL 上是合法的。但那意味着此后每一处引用都得记得加引号,忘一次
就又是 1064。所以 `TestMigrationsAvoidReservedWords` 接受反引号(它确实合法),而错误
提示要求的是改名。

只拦**保留字**,不拦非保留字:`status`、`comment` 这类做标识符是合法的,把它们也拦掉
会逼着人给再普通不过的列名改名 —— 一条开始拦正常写法的规则,很快就会被绕过去。

### 三、每条迁移语句都必须可重跑

因为失败的迁移会从第一条重来。建表一律 `CREATE TABLE IF NOT EXISTS`;`ALTER` 要么
幂等,要么放在这个文件里**唯一的**那条语句上。

0035 这次是侥幸:前两张表已经建成了,而它们是 `IF NOT EXISTS`,所以改完直接重跑
`migrate` 就行。换成两条 `ADD COLUMN` 就不是侥幸了。

## 没有做的事

**在 CI 里拿真的 MySQL 跑一遍迁移。** 那是唯一能真正覆盖这类问题的办法,静态闸只能挡住
*已经知道*的坑 —— 下一个 MySQL 专属差异照样会漏过去。没做的原因是当前没有可用的 CI
MySQL 实例,而在本机引入一个会破坏"零依赖启动"。

这是一笔明确的欠账,不是一个已经解决的问题。真正的判据很简单:**下一次被咬的时候,
看是又一类新差异,还是这三道闸里漏出去的。** 如果是前者,就该去搭那个 MySQL 了。
