# AegisDB 数据库网关 · 存储库从 MySQL 搬迁到 PostgreSQL SOP

> 一次性的**停机**搬迁。程序是网关二进制自带的子命令 `aegisdb migrate-store`,只读 MySQL、写一个空的 PG 库,
> 搬完自动核对。MySQL 原样保留,作为回退。设计取舍见 `docs/adr/0020-store-on-postgresql.md`。

## 零、一页速查

```bash
# 演练、正式都一样:先停网关,再搬
sudo systemctl stop aegisdb
cd /opt/aegisdb && set -a; . ./aegisdb.env; set +a

./aegisdb migrate-store \
  --from-mysql "$AEGISDB_MYSQL_DSN" \
  --to-postgres "host=PG地址 port=5432 user=aegisdb password=… dbname=aegisdb sslmode=disable" \
  --tz Asia/Shanghai
# 退出码 0 = 搬完且核对通过;非 0 = 不要切换,MySQL 原样可用

# 切换:写进 aegisdb.env(不是 config.yaml!)
echo 'AEGISDB_DB_DRIVER=postgres' >> aegisdb.env
echo 'AEGISDB_POSTGRES_DSN=host=PG地址 port=5432 user=aegisdb password=… dbname=aegisdb sslmode=disable' >> aegisdb.env
set -a; . ./aegisdb.env; set +a
./aegisdb migrate          # 在 PG 上跑一遍(幂等,应当什么都不做)
sudo systemctl start aegisdb
```

## 一、前置条件

| 项 | 要求 | 怎么确认 |
|---|---|---|
| 网关版本 | 已升级到**含 `migrate-store` 的版本**,且在 MySQL 上正常跑过 | `./aegisdb version`;搬迁程序要求源库迁移账本与本版本**完全一致**,少了多了都拒绝 |
| PostgreSQL | 16+(最低 13),专用空库 | `psql -c 'select version()'` |
| 库与账号 | 库属主就是网关账号 | `CREATE ROLE aegisdb LOGIN PASSWORD '…'; CREATE DATABASE aegisdb OWNER aegisdb;` |
| citext 扩展 | 可用(PG 自带的 contrib;13+ 是受信扩展,库属主能自己建) | 建表时自动 `CREATE EXTENSION IF NOT EXISTS citext`。若 DBA 收紧了扩展权限,请 DBA 预先在该库执行这一句 |
| 网络 | 网关所在机器能连 PG | `psql "host=… dbname=aegisdb user=aegisdb" -c 'select 1'` |
| 时区 | 知道网关机器的时区(MySQL 的时间不带时区,`--tz` 必填) | 网关机器上 `timedatectl`;生产 DSN 里是 `loc=Local` 时就是机器时区,通常 `Asia/Shanghai` |
| 磁盘 | PG 空间 ≥ MySQL 存储库数据量的 1.5 倍 | 源库 `SELECT SUM(data_length+index_length) FROM information_schema.tables WHERE table_schema='aegisdb'` |

## 二、事前:估时与演练(不停机)

### 1. 在生产上只读预估

```bash
./aegisdb migrate-store --from-mysql "$AEGISDB_MYSQL_DSN" --tz Asia/Shanghai --estimate
```

`--estimate` 只读源库:列出每张表的行数,并把审计链完整验一遍。最后一行是这一趟的用时,大头是审计链校验,
正式搬迁时这一步还会再做一次。

**审计链必须验得过。** 如果报"源库审计链在本程序里验不过",但网关界面上的链校验是通过的,那几乎可以肯定是
`--tz` 填错了。先停下核对时区,**不要**加 `--accept-broken-chain` 硬搬:时区错了,所有时间都会整体偏移,
而内容核对发现不了。

### 2. 用生产备份完整演练一次

1. 把最近一次 MySQL 备份恢复到一台演练用 MySQL。
2. 起一个演练用的空 PG 库。
3. 对它们跑完整的 `migrate-store`,记下总用时。**这就是正式停机窗口的主体。**
4. 在演练机上用 `AEGISDB_DB_DRIVER=postgres` 起一个网关,按第五节的清单点一遍。

参考数据(本地 Docker,MySQL 8 与 PG 16 同机,4 路并发):**200 万行审计总用时约 3.5 分钟**(其中拷贝 1 分 48 秒,其余是两边审计链校验与内容核对),1000 万行估计 18 分钟上下,
时间大致随审计行数线性增长。生产以演练实测为准。

## 三、正式搬迁(停机窗口内)

| # | 步骤 | 命令 / 动作 | 通过标准 |
|---|---|---|---|
| 1 | 通知、停所有网关实例 | `sudo systemctl stop aegisdb`(每台) | `systemctl is-active` 为 inactive |
| 2 | MySQL 做一份备份 | 按现有备份流程(mysqldump / xtrabackup) | 备份完成 |
| 3 | 搬迁 | 见第零节 `migrate-store` | 退出码 0,最后一行"搬迁完成,核对通过" |
| 4 | 切换配置 | 往 `aegisdb.env` 加 `AEGISDB_DB_DRIVER=postgres` 与 `AEGISDB_POSTGRES_DSN=…` | `grep AEGISDB_DB_DRIVER aegisdb.env` |
| 5 | 在 PG 上跑 migrate | `./aegisdb migrate` | 日志 `driver=postgres`,`schema up to date` |
| 6 | 启动网关 | `sudo systemctl start aegisdb` | 启动日志 `driver=postgres` |
| 7 | 验证 | 第五节清单 | 全部通过 |

**为什么切换必须写在 `aegisdb.env`:** 升级包会覆盖 `configs/config.yaml`。如果切换写在那里,下一次升级就会悄悄
连回旧 MySQL,在一份停在搬迁时刻的旧数据上继续接写入。生产环境下网关也不认配置文件里的 `driver`,只认
`AEGISDB_DB_DRIVER`。

**第二道保险:** 搬迁成功后,程序会在 MySQL 里建一张标记表 `aegisdb_store_moved`。之后网关如果连到这个 MySQL
上启动(serve / init / migrate),会直接拒绝并报错。日志里看到"已于 … 搬到 PostgreSQL,不能再在它上面启动",
说明 `aegisdb.env` 的切换丢了。

## 四、`migrate-store` 做了什么、会怎样失败

1. **预检**:
   - 源库迁移账本与本版本一致。
   - 源库没有搬迁标记。
   - 源库审计链验得过。
   - 目标 PG 版本不低于 13,且存储库表都是空的。
2. **建表**:只跑 PG 的建表迁移,不写参考数据(源库里已经有了)。
3. **拷贝**:每张表一条 SELECT 流式 `COPY` 进 PG,原样带 id;大表先开工,`--workers` 路并发(默认 4)。
4. **收尾**:把自增序列推到 max(id),执行 `ANALYZE`。
5. **核对**,全部通过才在源库留标记、退出码 0:
   - 每张表行数相等。
   - 每张表内容摘要相等:逐列逐值,与行序无关。
   - 审计链在 PG 上重算,结论与源库一致。
   - 源库各表行数与开始时相同。不同说明搬迁期间有写入,即网关没停干净。

| 报错 | 原因 | 处理 |
|---|---|---|
| 源库还缺 N 个迁移 | 网关没升级到本版本,或升级后没跑 migrate | 先在 MySQL 上 `./aegisdb migrate` |
| 源库有本版本不认识的迁移 | 用的二进制比库旧 | 换成与生产网关同版本的二进制 |
| 源库审计链在本程序里验不过 | 几乎都是 `--tz` 错了 | 核对时区;确属已知的断链,才用 `--accept-broken-chain` |
| 目标 PG 版本太旧 | 低于 13 | 换实例 |
| 目标库的 X 里已经有数据 | 上次没搬完 | 确认目标就是本次的新库后,加 `--reset` 重跑(只清空存储库表) |
| 源库里已有搬迁标记表 | 这个库已经搬过 | 真要重搬(例如回退后再来),先 `DROP TABLE aegisdb_store_moved` |
| X.Y 有一行含 NUL 字节 | MySQL 能存、PG 的文本存不下 | 按报出的主键在源库处理该行后重跑 `--reset` |
| 核对没通过 / 网关没停干净 | 搬迁期间有写入,或其它不一致 | 停干净所有实例,`--reset` 重跑 |

任何失败都不会动 MySQL 里的业务数据。唯一的写入是成功后建的那张标记表。失败时直接按回退处理,即什么都不用改,
启动网关就行。

## 五、切换后验证清单

- [ ] 用普通账号登录;大小写不同的邮箱也能登录(与 MySQL 时一致)。
- [ ] 已经登录的人不用重新登录(会话随库搬过来了)。
- [ ] 审计页点「链校验」通过,行数与搬迁前一致。
- [ ] 审计页按关键字搜一条已知命令,能搜到(不分大小写)。
- [ ] 命令行在 dev 实例执行一条 `SELECT 1`,审计页出现新行,链校验仍通过(序列已对齐、新行接得上)。
- [ ] 审批单列表、发布单列表、连接列表的条数与搬迁前一致。
- [ ] `./aegisdb verify-audit` 退出码 0。

## 六、回退

切到 PG 后发现问题,要回到 MySQL:

1. 停网关。
2. 从 `aegisdb.env` 删掉 `AEGISDB_DB_DRIVER=postgres` 与 `AEGISDB_POSTGRES_DSN`。
3. 在 MySQL 上 `DROP TABLE aegisdb_store_moved;`。这是有意的一步:删了它,网关才肯在 MySQL 上启动。
4. 启动网关。

**代价:** 切到 PG 之后产生的数据(审计、工单、发布单……)不在 MySQL 里,需要人工补录。所以回退窗口越早越好。
MySQL 建议保留并停写 2 周,确认 PG 稳定后再下线。

## 七、搬迁之后的日常

- 例行升级不变:停服、换包、`./aegisdb migrate`、启动。PG 的迁移在 `migrations/pg/`,同样内嵌在二进制里。
- 备份改为 PG 的备份(`pg_dump -Fc aegisdb` 或实例级物理备份)。
- `verify-audit`、巡检脚本不用改。

## 附录:`migrate-store` 参数

| 参数 | 说明 |
|---|---|
| `--from-mysql` | 源 MySQL DSN(go-sql-driver 格式,与 `AEGISDB_MYSQL_DSN` 相同);也可用环境变量 `AEGISDB_MIGRATE_FROM_MYSQL` |
| `--to-postgres` | 目标 PG DSN(libpq 键值串或 `postgres://` URL);也可用 `AEGISDB_MIGRATE_TO_POSTGRES` |
| `--tz` | **必填**。写下这些数据的网关所在时区,如 `Asia/Shanghai` |
| `--workers` | 同时拷贝的表数,默认 4 |
| `--estimate` | 只读预估:行数 + 审计链,不碰目标 |
| `--reset` | 目标里有上次没搬完的存储库表时,先清空再搬 |
| `--accept-broken-chain` | 源库审计链本来就验不过、且已知原因时仍然搬(核对时要求两边断在同一行) |

退出码:0 = 搬完且核对通过;1 = 没搬成或核对没过。
