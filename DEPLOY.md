# 部署 AegisDB 数据库网关(生产 · MySQL)

## 1. 打包(前后端一体)

在仓库根目录执行(纯 Go,交叉编译到 Linux 无需 C 工具链;`VERSION` 不设则取 `git describe`):

```bash
VERSION="${VERSION:-$(git describe --tags --always --dirty)}"
rm -rf dist && mkdir -p dist/web dist/configs dist/migrations dist/deploy

# 打包前的闸:生产环境不得含模拟数据
(cd backend && go test ./internal/bootstrap/ \
  -run 'TestProductionServesNoSimulatedData|TestSimulationDefaultsToOff|TestSimulatedPathsAreDocumented' \
  -count=1 -timeout 10m)

# 前端
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
  configs/config.yaml   生产配置(env=prod → MySQL,auto_migrate=false,seed=false)
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
sudo cp /opt/vela-gateway/vela.env.example /opt/vela-gateway/vela.env   # 填入 VELA_JWT_SECRET / VELA_MYSQL_DSN
sudo chown -R vela:vela /opt/vela-gateway && sudo chmod 600 /opt/vela-gateway/vela.env
```

托管见 `deploy/vela-gateway.service`(其头部注释含完整安装步骤)。下面第 3–6 步在 `/opt/vela-gateway` 下执行。

## 3. 准备 MySQL

创建库(字符集 utf8mb4)与账号,例如:

```sql
CREATE DATABASE vela_gateway CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'vela'@'%' IDENTIFIED BY '你的密码';
GRANT ALL ON vela_gateway.* TO 'vela'@'%';
```

DSN 形如:`vela:密码@tcp(mysql主机:3306)/vela_gateway?charset=utf8mb4&parseTime=true&loc=Local`

建议把密钥放进 `vela.env`(由 `vela.env.example` 复制,`chmod 600`),后续命令统一 `set -a; . ./vela.env; set +a` 加载。

## 4. 迁移表结构(幂等)

按 `migrations/*.sql` 版本化建表,已应用的版本记录在 `schema_migrations` 表并自动跳过,可在每次发版时重复执行:

```bash
cd dist
set -a; . ./vela.env; set +a          # 载入 VELA_MYSQL_DSN 等
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
set -a; . ./vela.env; set +a          # 至少包含 VELA_JWT_SECRET(≥32 位)与 VELA_MYSQL_DSN
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
| `VELA_MYSQL_DSN` | 生产 MySQL DSN(覆盖配置文件) |
| `VELA_JWT_SECRET` | JWT 签名密钥,**必填 ≥32 位、≥8 种不同字符、不在弱值黑名单**;未设 `VELA_SECRET_KEY` 时同时派生连接口令静态加密密钥 |
| `VELA_SECRET_KEY` | 连接口令与运行时密钥(审批魔方令牌等)的 AES-GCM 静态加密密钥(**强烈建议 ≥32 位**)。设置后与 JWT 密钥解耦,可安全轮换 JWT 密钥而不影响存量口令解密;**一经设定不可更改**(轮换会导致存量口令无法解密)。未设时启动打 WARN |
| `VELA_WEB_DIR` | 前端静态目录(默认取配置 `server.web_dir: web`);缺 `index.html`/`assets` 时打 WARN |
| `VELA_TLS_CERT` / `VELA_TLS_KEY` | 直接由网关终止 TLS 的证书/私钥(可选) |
| `VELA_WEBHOOK_SECRET` | 出站审计 Webhook 的 Bearer Token(随 `Authorization: Bearer` 发送;**不是 HMAC 签名**)。只在空库首次 seed/init 时写入 Webhook 配置行,之后以「设置 › Webhook」为准 |
| `VELA_WEBHOOK_ALLOW_PRIVATE` | `1/true/yes/on` 时允许 Webhook / 飞书 / 审批魔方的出站目标是内网、loopback、链路本地、CGNAT 地址。默认 dev 放行、prod 拦截(SSRF 防护);生产打开时启动打 WARN |
| `APP_ENV` / `VELA_ENV` | `prod`(别名 `production` / `release` / `live`)→ MySQL;其它(含 `dev` / `development` / `local`)→ SQLite;未识别的值按 dev 并打 WARN |
| `VELA_DB_DRIVER` | 强制 `mysql` \| `sqlite`(覆盖上面的 env 推断;yaml 里的 `database.driver` 无效,总被它覆盖) |
| `VELA_ADMIN_EMAIL` / `VELA_ADMIN_PASSWORD` / `VELA_ADMIN_NAME` | `init` 时的管理员凭据;口令 ≥12 位且含大小写 / 数字 / 符号中 ≥3 类 |

## 配置文件(`configs/config.yaml`)其余键

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `server.addr` | `:8080` | 监听地址 |
| `server.mode` | dev `debug` / prod `release` | gin 模式;`debug` 时业务日志(slog JSON)降到 Debug 级 |
| `server.cors_origins` | dev 两个本地源 / prod 空 | 空 = 不发 CORS 头(同源部署不需要) |
| `server.trusted_proxies` | 空 | 见第 6 步 |
| `database.sqlite_path` | `vela-gateway.db` | 仅 dev;自动追加 WAL、`busy_timeout=5000`、外键开、连接池 1 |
| `database.auto_migrate` | dev true / prod false | 只对 SQLite 生效;MySQL 上忽略并打 WARN(schema 只由 SQL 迁移拥有) |
| `database.seed` | dev true / prod false | 空库写演示引用数据 + 1 个管理员;prod + 空库 → 拒绝启动 |
| `jwt.ttl_hours` | 8 | 仅回落值;实际登录 TTL 由运行时设置 `security.sessionTTL`(4h / 8h / 24h)决定 |
| `gateway.exec_timeout_seconds` | 30 | 只在首次 seed 时写进运行时设置 `gateway.execTimeout` |
| `gateway.default_policy` / `gateway.strict_mode` | `strict` / true | 前者只被存储、判定不读;后者只在升级到 0030 时一次性折进分层 |
| `webhook.endpoint / secret / retry_max / enabled` | 空 / — / 5 / false | 只在空库首次 seed/init 时写入 Webhook 行;`webhook.events` **无效**(seed 固定订阅 `exec`) |
| `webhook.allow_private` | dev true / prod false | 同 `VELA_WEBHOOK_ALLOW_PRIVATE` |
| `secret_key` | 回落 `jwt.secret` | 同 `VELA_SECRET_KEY` |

运行时设置(`tbl_setting`,界面「设置」或 `PUT /api/v1/settings` 即改即生效)的完整清单见 README「系统设置」。

## 迁移与 `init` 的行为细节

- `migrate` / `init` 用 MySQL `GET_LOCK('vela_schema_migrate', 60)` 串行,多台同时执行只有一台跑。
- 迁移文件里的 `CREATE DATABASE` / `USE` 会被跳过;已应用版本记录在 `schema_migrations`。
- `migrate` 同时回填引用数据:`gli` / `uat` 环境、五个内置分层、流程模板与规则库、`executed_at`、`strict_nowhere`。
- `init` 重复执行会**重置**该管理员的密码,并强制其 active + admin 角色。
- 迁移文件 `0013 / 0019 / 0020 / 0025 / 0026 / 0029 / 0033` 各含多条非幂等 `ALTER`;若在其中一条之后失败,
  重跑会在已成功的那条上报 `1060`,需要手工把已应用的语句注释掉再跑(见 ADR 0016)。

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

> 本地开发仍用零依赖 SQLite:`cd backend && APP_ENV=dev go run ./cmd/server`,前端 `cd frontend && npm run dev`。
