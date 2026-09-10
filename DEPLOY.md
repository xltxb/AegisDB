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

> 生产环境若 `VELA_JWT_SECRET` 缺失/为占位值/短于 32 位,服务会**拒绝启动**。
> TLS:前置反向代理终止,或设 `VELA_TLS_CERT`/`VELA_TLS_KEY` 让网关直接跑 HTTPS。
> 托管:用 `deploy/vela-gateway.service`(systemd)。

浏览器打开 `https://部署主机`(或反代域名),用上一步创建的管理员登录。

## 环境变量一览

| 变量 | 说明 |
| --- | --- |
| `VELA_MYSQL_DSN` | 生产 MySQL DSN(覆盖配置文件) |
| `VELA_JWT_SECRET` | JWT 签名密钥,**必填 ≥32 位**;未设 `VELA_SECRET_KEY` 时同时派生连接口令静态加密密钥 |
| `VELA_SECRET_KEY` | 连接口令静态加密密钥(**强烈建议 ≥32 位**)。设置后与 JWT 密钥解耦,可安全轮换 JWT 密钥而不影响存量口令解密;**一经设定不可更改**(轮换会导致存量口令无法解密) |
| `VELA_WEB_DIR` | 前端静态目录(默认取配置 `server.web_dir: web`) |
| `VELA_TLS_CERT` / `VELA_TLS_KEY` | 直接由网关终止 TLS 的证书/私钥(可选) |
| `VELA_WEBHOOK_SECRET` | 出站审计 Webhook / 飞书签名密钥(可选) |
| `APP_ENV` / `VELA_ENV` | `prod`→MySQL,`dev`→SQLite |
| `VELA_DB_DRIVER` | 强制 `mysql` \| `sqlite`(覆盖上面的 env 推断) |
| `VELA_ADMIN_EMAIL` / `VELA_ADMIN_PASSWORD` / `VELA_ADMIN_NAME` | `init` 时的管理员凭据 |

> 本地开发仍用零依赖 SQLite:`cd backend && APP_ENV=dev go run ./cmd/server`,前端 `cd frontend && npm run dev`。
