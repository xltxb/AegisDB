# 部署 AegisDB 数据库网关(生产 · PostgreSQL)

## 1. 打包(前后端一体)

在仓库根目录执行(纯 Go,交叉编译到 Linux 无需 C 工具链;`VERSION` 不设则取 `git describe`):

```bash
VERSION="${VERSION:-$(git describe --tags --always --dirty)}"
rm -rf dist && mkdir -p dist/web dist/configs dist/migrations dist/deploy

# 打包前的闸:生产环境不得含模拟数据
(cd backend && go test ./internal/bootstrap/ \
  -run 'TestProductionServesNoSimulatedData|TestSimulationDefaultsToOff|TestSimulatedPathsAreDocumented' \
  -count=1 -timeout 10m)

# 前端(React 19;`npm run build` 会先跑 tsc 类型检查,再按路由分包)
(cd frontend && npm ci && npm run build) && cp -r frontend/dist/* dist/web/

# 后端
(cd backend && GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o ../dist/vela-gateway ./cmd/server)

# 配置、迁移、部署资产
cp backend/configs/config.prod.yaml dist/configs/config.yaml   # 二进制默认的 -config 路径
cp backend/migrations/*.sql dist/migrations/                   # 参考用,同样已内嵌进二进制
cp -r backend/docs dist/docs                                   # /docs 与 /openapi.yaml 按工作目录读 docs/openapi.yaml
cp deploy/vela-gateway.service deploy/vela.env.example dist/deploy/
cp deploy/vela.env.example dist/

tar -czf "vela-gateway-$VERSION-linux-amd64.tar.gz" -C dist .
```

产物在 `dist/`:

```
dist/
  vela-gateway          单一后端二进制(内置 API + 前端 UI + 内嵌 SQL 迁移)
  web/                  构建后的前端(后端按 web_dir 提供)
  configs/config.yaml   生产配置(env=prod:不灌演示数据、强制 JWT 强度、CORS 收紧;存储恒为 PostgreSQL)
  migrations/           参考 SQL 迁移(同样已内嵌进二进制)
  docs/                 OpenAPI 契约(在线接口文档 /docs 与 /openapi.yaml 从这里读)
  deploy/               systemd unit + 环境变量模板
  vela.env.example      环境变量模板(复制为 vela.env 并填写)
```

子命令:`vela-gateway version` | `migrate`(仅建/升级表) | `init`(迁移+引用数据+管理员) | 直接运行(服务)。

## 2. 准备主机(Linux systemd 部署)

```bash
sudo useradd --system --home /opt/vela-gateway --shell /usr/sbin/nologin vela
sudo mkdir -p /opt/vela-gateway
sudo tar -xzf vela-gateway-*-linux-amd64.tar.gz -C /opt/vela-gateway
sudo cp /opt/vela-gateway/vela.env.example /opt/vela-gateway/vela.env   # 填入 VELA_JWT_SECRET / VELA_PG_DSN
sudo chown -R vela:vela /opt/vela-gateway && sudo chmod 600 /opt/vela-gateway/vela.env
```

托管见 `deploy/vela-gateway.service`(其头部注释含完整安装步骤)。下面第 3–6 步在 `/opt/vela-gateway` 下执行。

## 3. 准备 PostgreSQL

网关自身的元数据存储只有 PostgreSQL 一种(开发与生产同一种,见 ADR 0018)。

建库与账号 —— 连到**任意**库(通常是 `postgres`)执行:

```sql
CREATE DATABASE vela_gateway;
CREATE USER vela WITH PASSWORD '你的密码';
GRANT ALL ON DATABASE vela_gateway TO vela;
```

然后**连到 `vela_gateway` 这个库**再执行下面这条 —— 它作用于库内的 schema,连在别的库上
执行只会改错对象(psql 里是 `\connect vela_gateway`,图形客户端里是切换连接):

```sql
-- 迁移要在 public schema 里建表,而 PG 15 起 public 不再默认对所有人可写。
GRANT CREATE, USAGE ON SCHEMA public TO vela;
```

DSN 用 libpq 的 key=value 或 URL 形式,例如:
`host=db.internal port=5432 user=vela password=你的密码 dbname=vela_gateway sslmode=require`

`sslmode` 在生产上明确写出来 —— 缺省的 `prefer` 会在服务端不支持 TLS 时**静默降级成明文**,
而凭据和全部审计内容都走这条连接。

建议把密钥放进 `vela.env`(由 `vela.env.example` 复制,`chmod 600`),后续命令统一 `set -a; . ./vela.env; set +a` 加载。

## 4. 迁移表结构(幂等)

按 `migrations/*.sql` 版本化建表,已应用的版本记录在 `schema_migrations` 表并自动跳过,可在每次发版时重复执行:

```bash
cd dist
set -a; . ./vela.env; set +a          # 载入 VELA_PG_DSN 等
./vela-gateway migrate
```

## 5. 初始化引用数据 + 管理员(仅首次)

写入引用数据(角色/菜单/能力矩阵/风险字典/默认设置,**无演示数据**)并创建平台管理员(`init` 会先自动跑迁移):

```bash
./vela-gateway init \
  --admin-email you@corp.io \
  --admin-password 'A-Strong-Passw0rd!' \
  --admin-name '平台管理员'
```

管理员凭据也可用环境变量 `VELA_ADMIN_EMAIL` / `VELA_ADMIN_PASSWORD` / `VELA_ADMIN_NAME`。
口令要求 **≥12 位且含大小写字母/数字/符号中的 ≥3 类**。`init` 可重复执行:引用数据只在空库写入;重复执行会重置该管理员密码,不会重复建号。

## 6. 运行

```bash
cd dist
set -a; . ./vela.env; set +a          # 至少包含 VELA_JWT_SECRET(≥32 位)与 VELA_PG_DSN
./vela-gateway -config configs/config.yaml     # 同时提供 API 与前端 UI,监听 :8080
```

> 生产环境若 `VELA_JWT_SECRET` 缺失 / 为占位值 / 短于 32 位 / 少于 8 种不同字符,服务会**拒绝启动**。
> 生产 + `database.seed=true` + 空库也会**拒绝启动**(seed 只给开发用)。
> TLS:前置反向代理终止,或设 `VELA_TLS_CERT`/`VELA_TLS_KEY`(yaml `server.tls_cert/tls_key`)让网关直接跑 HTTPS;
> 生产未启用 TLS 会打 WARN。
> 托管:用 `deploy/vela-gateway.service`(systemd,`WorkingDirectory=/opt/vela-gateway`)。

**前置反向代理时必须配 `server.trusted_proxies`**(yaml 列表,IP 或 CIDR):默认为空 = 不信任任何
`X-Forwarded-For`,客户端 IP 取 TCP 对端,于是登录 IP 白名单、登录频控、开放接口凭据的 IP 白名单、
审批回调的 IP 白名单看到的全是反代地址。填了非法值(非 IP/CIDR)会拒绝启动。

上传脚本与导出归档默认落在**工作目录**下的 `uploads/`、`export/`(运行时设置 `script.savePath` /
`export.savePath` 可改),启动时自动创建;OpenAPI 契约从工作目录 `docs/openapi.yaml` 读。
SPA 托管:`/assets/*` 带一年 `immutable` 缓存,`index.html` `no-cache`;带扩展名的未知路径与
`/api/` 下未知路径返回 404,不回退到 `index.html`。

浏览器打开 `https://部署主机`(或反代域名),用上一步创建的管理员登录。

## 环境变量一览

| 变量 | 说明 |
| --- | --- |
| `VELA_PG_DSN` | 网关自身存储的 PostgreSQL DSN(覆盖配置文件的 `database.postgres_dsn`)。生产**必填**:`config.prod.yaml` 里故意留空,而空 DSN 会被 libpq 读成"这台机器的默认库",所以留空 = 拒绝启动 |
| `VELA_JWT_SECRET` | JWT 签名密钥,**必填 ≥32 位、≥8 种不同字符、不在弱值黑名单**;未设 `VELA_SECRET_KEY` 时同时派生连接口令静态加密密钥 |
| `VELA_SECRET_KEY` | 连接口令与运行时密钥(审批魔方令牌等)的 AES-GCM 静态加密密钥(**强烈建议 ≥32 位**)。设置后与 JWT 密钥解耦,可安全轮换 JWT 密钥而不影响存量口令解密;**一经设定不可更改**(轮换会导致存量口令无法解密)。未设时启动打 WARN |
| `VELA_WEB_DIR` | 前端静态目录(默认取配置 `server.web_dir: web`);缺 `index.html`/`assets` 时打 WARN |
| `VELA_TLS_CERT` / `VELA_TLS_KEY` | 直接由网关终止 TLS 的证书/私钥(可选) |
| `VELA_WEBHOOK_SECRET` | 出站审计 Webhook 的 Bearer Token(随 `Authorization: Bearer` 发送;**不是 HMAC 签名**)。只在空库首次 seed/init 时写入 Webhook 配置行,之后以「设置 › Webhook」为准 |
| `VELA_WEBHOOK_ALLOW_PRIVATE` | `1/true/yes/on` 时允许 Webhook / 飞书 / 审批魔方的出站目标是内网、loopback、链路本地、CGNAT 地址。默认 dev 放行、prod 拦截(SSRF 防护);生产打开时启动打 WARN |
| `APP_ENV` / `VELA_ENV` | 部署档位:`prod`(别名 `production` / `release` / `live`)/ 其它(含 `dev` / `development` / `local`);未识别的值按 dev 并打 WARN。**它不挑存储引擎** —— 两档都是 PostgreSQL;它决定的是演示数据(仅 dev)、生产 JWT 强度校验、CORS 与出站 SSRF 的松紧 |
| `VELA_ADMIN_EMAIL` / `VELA_ADMIN_PASSWORD` / `VELA_ADMIN_NAME` | `init` 时的管理员凭据;口令 ≥12 位且含大小写 / 数字 / 符号中 ≥3 类 |

## 配置文件(`configs/config.yaml`)其余键

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `server.addr` | `:8080` | 监听地址 |
| `server.mode` | dev `debug` / prod `release` | gin 模式;`debug` 时业务日志(slog JSON)降到 Debug 级 |
| `server.cors_origins` | dev 两个本地源 / prod 空 | 空 = 不发 CORS 头(同源部署不需要) |
| `server.trusted_proxies` | 空 | 见第 6 步 |
| `database.postgres_dsn` | dev 指向本机 `vela_gateway` | 网关自身存储;被 `VELA_PG_DSN` 覆盖。不写 `user=` 时 libpq 回落到当前 OS 用户 |
| `database.seed` | dev true / prod false | 空库写演示引用数据 + 1 个管理员;prod + 空库 → 拒绝启动 |
| `jwt.ttl_hours` | 8 | 仅回落值;实际登录 TTL 由运行时设置 `security.sessionTTL`(4h / 8h / 24h)决定 |
| `gateway.exec_timeout_seconds` | 30 | 只在首次 seed 时写进运行时设置 `gateway.execTimeout` |
| `gateway.default_policy` / `gateway.strict_mode` | `strict` / true | 前者只被存储、判定不读;后者只在升级到 0030 时一次性折进分层 |
| `webhook.endpoint / secret / retry_max / enabled` | 空 / — / 5 / false | 只在空库首次 seed/init 时写入 Webhook 行;`webhook.events` **无效**(seed 固定订阅 `exec`) |
| `webhook.allow_private` | dev true / prod false | 同 `VELA_WEBHOOK_ALLOW_PRIVATE` |
| `secret_key` | 回落 `jwt.secret` | 同 `VELA_SECRET_KEY` |

运行时设置(`tbl_setting`,界面「设置」或 `PUT /api/v1/settings` 即改即生效)的完整清单见 README「系统设置」。

## 迁移与 `init` 的行为细节

- `migrate` / `init` 用 PostgreSQL 的 `pg_try_advisory_lock(hashtext('vela_schema_migrate'))` 串行,
  多台同时执行只有一台跑。用 `try` 加一个 **60 秒上限**的重试,而不是会无限阻塞的 `pg_advisory_lock`:
  超时报的是"另一个迁移正在跑",而不是一次没有任何输出的挂起。锁按**库**计,而且是会话级的
  —— 所以它固定在一条专用连接上持有到迁移结束(池里换一条连接就等于悄悄放了锁)。
- 迁移文件里的 `CREATE DATABASE` / `USE` 会被跳过;已应用版本记录在 `schema_migrations`。
- `migrate` 同时回填引用数据:`gli` / `uat` 环境、五个内置分层、流程模板与规则库、`executed_at`、`strict_nowhere`。
- `init` 重复执行会**重置**该管理员的密码,并强制其 active + admin 角色。

### 迁移只有一份 baseline

迁至 PostgreSQL 时,此前 44 个增量迁移压成了单一的 `0001_init.sql`(整份都是
`CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`,重复执行无害)。

这里从前有两段运维指南,随那 44 个文件一起删掉了,**不要再去 git 历史里把它们翻出来照做**:

- 一段讲"含多条非幂等 `ALTER` 的文件跑到第 N 条炸了怎么办"(注释掉前几条再重跑)。
  它点名的 `0013_dual_snapshot.sql`、`0020_api_client.sql`、`0033_exec_window_approval.sql`
  等文件都不存在了,而 baseline 里每一条语句都幂等 —— 那种中间态现在做不出来。
- 一段讲 `0039`–`0044` 那六个把 `key` / `database` / `sql` / `rows` 四个 MySQL 保留字列名
  改掉的迁移(ADR 0016 §二),以及新旧两版程序不能同时连同一个库的升级顺序。改名的结果
  (`api_key` / `db_name` / `sql_text` / `row_count`)已经是 baseline 里的列名,没有改名动作
  要执行了。

`TestMigrations_AltersAreIdempotentOrAlone` 仍然拦着**新**加的迁移:`ALTER` 要么每条都幂等,
要么一个文件只放一条(ADR 0016 §三)。

**升级顺序**仍然是"停旧进程 → `./vela-gateway migrate` → 起新进程"。反过来做,新二进制会对着
旧表结构跑;迁移之后也不要单独回滚二进制。

## 启动时的自动动作

- 上次进程遗留的 `running` 导出 / 异步任务标失败(结果未知,不重跑);`running` 发布单标失败、`waiting` 保留、`pending` 重新入队。
- 导出归档清理先跑一次(之后每小时;`export.retentionDays` 默认 3,0 = 永久;只删文件不删任务记录)。
- 审批人自检(见 README「启动自检」)。
- 元数据定时同步**启动时不跑**,开启后等满一个间隔才跑第一轮。

## 运维观测

- `GET /healthz`(无鉴权)返回 `{"status":"ok"}`;`GET /api/v1/gateway/stats`(需登录)返回最近 512 次请求的
  p50 / p95 与生产拦截计数。**没有** Prometheus `/metrics` 端点。
- 访问日志:gin 文本格式到 stdout,query 中的 `secret / token / access_token` 已脱敏。
  业务日志:slog **JSON** 到 stdout。
- 上线后值得盯的启动行:`simulation mode`、`审批人自检`、`生产环境未启用 TLS`、`VELA_SECRET_KEY 未设置`、
  `web_dir has no index.html/assets`、`未识别的环境名`、`webhook.allow_private`。
- Webhook 投递记录在 `tbl_webhook_delivery`,界面「设置 › Webhook › 查看投递日志」或
  `GET /api/v1/settings/webhook/deliveries?limit=50`。

> 本地开发用同一种存储:先 `createdb vela_gateway`,再 `cd backend && APP_ENV=dev go run ./cmd/server`
> (或 `backend/run-dev.sh` / `backend\run-dev.bat`),前端 `cd frontend && npm run dev`。
> 跑后端测试另需 `createdb vela_test`(可用 `VELA_TEST_PG_DSN` 指到别处)。
