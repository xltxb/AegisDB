# 部署 AegisDB 数据库网关(生产 · MySQL)

## 1. 打包(前后端一体)

```bash
./build.sh                                    # 为当前平台打包
GOOS=linux GOARCH=amd64 ./build.sh            # 交叉编译到 Linux 服务器(纯 Go,无需 C 工具链)
VERSION=1.0.0 GOOS=linux GOARCH=amd64 ./build.sh   # 显式版本号(否则取 git describe)
```

产物在 `dist/`,并额外生成 `aegisdb-<版本>-<os>-<arch>.tar.gz` 归档:

```
dist/
  aegisdb[.exe]    单一后端二进制(内置 API + 前端 UI + 内嵌 SQL 迁移)
  web/                  构建后的前端(后端按 web_dir 提供)
  configs/config.yaml   生产配置(env=prod → MySQL,auto_migrate=false,seed=false)
  migrations/           参考 SQL 迁移(同样已内嵌进二进制)
  deploy/               systemd unit + 环境变量模板
  aegisdb.env.example      环境变量模板(复制为 aegisdb.env 并填写)
  README-DEPLOY.md      部署速查
```

子命令:`aegisdb version` | `migrate`(仅建/升级表) | `init`(迁移+引用数据+管理员) | `verify-audit`(重算审计哈希链,链断了退出码 2) | 直接运行(服务)。

## 2. 准备主机(Linux systemd 部署)

```bash
sudo useradd --system --home /opt/aegisdb --shell /usr/sbin/nologin aegisdb
sudo mkdir -p /opt/aegisdb
sudo tar -xzf aegisdb-*-linux-amd64.tar.gz -C /opt/aegisdb
sudo cp /opt/aegisdb/aegisdb.env.example /opt/aegisdb/aegisdb.env   # 填入 AEGISDB_JWT_SECRET / AEGISDB_MYSQL_DSN
sudo chown -R aegisdb:aegisdb /opt/aegisdb && sudo chmod 600 /opt/aegisdb/aegisdb.env
```

托管见 `deploy/aegisdb.service`(其头部注释含完整安装步骤)。下面第 3–6 步在 `/opt/aegisdb` 下执行。

## 3. 准备 MySQL

创建库(字符集 utf8mb4)与账号,例如:

```sql
CREATE DATABASE aegisdb CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'aegisdb'@'%' IDENTIFIED BY '你的密码';
GRANT ALL ON aegisdb.* TO 'aegisdb'@'%';
```

DSN 形如:`aegisdb:密码@tcp(mysql主机:3306)/aegisdb?charset=utf8mb4&parseTime=true&loc=Local`

建议把密钥放进 `aegisdb.env`(由 `aegisdb.env.example` 复制,`chmod 600`),后续命令统一 `set -a; . ./aegisdb.env; set +a` 加载。

## 4. 迁移表结构(幂等)

按 `migrations/*.sql` 版本化建表,已应用的版本记录在 `schema_migrations` 表并自动跳过,可在每次发版时重复执行:

```bash
cd dist
set -a; . ./aegisdb.env; set +a          # 载入 AEGISDB_MYSQL_DSN 等
./aegisdb migrate
```

## 5. 初始化引用数据 + 管理员(仅首次)

写入引用数据(角色/菜单/能力矩阵/风险字典/默认设置,**无演示数据**)并创建平台管理员(`init` 会先自动跑迁移):

```bash
./aegisdb init \
  --admin-email you@corp.io \
  --admin-password 'A-Strong-Passw0rd!' \
  --admin-name '平台管理员'
```

管理员凭据也可用环境变量 `AEGISDB_ADMIN_EMAIL` / `AEGISDB_ADMIN_PASSWORD` / `AEGISDB_ADMIN_NAME`。
口令要求 **≥12 位且含大小写字母/数字/符号中的 ≥3 类**。`init` 可重复执行:引用数据只在空库写入;重复执行会重置该管理员密码,不会重复建号。

## 6. 运行

```bash
cd dist
set -a; . ./aegisdb.env; set +a          # 至少包含 AEGISDB_JWT_SECRET(≥32 位)与 AEGISDB_MYSQL_DSN
./aegisdb -config configs/config.yaml     # 同时提供 API 与前端 UI,监听 :8080
```

> 生产环境若 `AEGISDB_JWT_SECRET` 缺失/为占位值/短于 32 位,服务会**拒绝启动**。
> TLS:前置反向代理终止,或设 `AEGISDB_TLS_CERT`/`AEGISDB_TLS_KEY` 让网关直接跑 HTTPS。
> 托管:用 `deploy/aegisdb.service`(systemd)。

浏览器打开 `https://部署主机`(或反代域名),用上一步创建的管理员登录。

## 7. 接入公司 SSO(OIDC,可选)

SSO **只做认证**:身份服务回答"你是谁"(邮箱),网关照旧用自己的角色 / 菜单 / 能力矩阵回答
"你能干什么"。判定链不因接入 SSO 而改变。网关按标准 OIDC(授权码流程 + PKCE)对接,任何符合规范的
身份服务都能接(Keycloak、Azure AD / Entra ID、Okta、Authing、飞书等)。

**原理**:

1. 登录页点「使用 SSO 登录」,浏览器去网关的 `GET /api/v1/auth/oidc/start`。网关生成 state、nonce
   和 PKCE 校验串,加密放进一个 10 分钟有效的 HttpOnly Cookie,把浏览器 302 到身份服务。
2. 用户在身份服务登录后,身份服务带着授权码跳回网关的 `GET /api/v1/auth/oidc/callback`。
3. 网关核对 state,在**服务端**用授权码换 token,按身份服务的 JWKS 验 ID Token(签名、颁发方、受众、
   有效期、nonce),取出邮箱。验过的身份加密放进一个 5 分钟有效的 Cookie,跳回网关登录页。
4. 登录页调 `POST /api/v1/auth/sso`,网关按**邮箱**找到网关用户,签发网关自己的会话。之后每个请求
   不再回身份服务。

token 全程只在服务端;浏览器只见过授权码(一次性,且必须配 PKCE 校验串才换得动)。

**前提**:

- 在身份服务里把网关登记为一个 OIDC 应用(授权码流程),拿到 Client ID(和 Client Secret,
  公开客户端可以没有)。
- 把网关的回调地址 `https://<网关访问地址>/api/v1/auth/oidc/callback` 原样加入应用的允许回调列表。
  网关挂在子路径下时,回调地址也带上子路径。
- 网关所在机器要能访问身份服务的 Issuer 地址(取发现文档、JWKS,换 token)。
- 让身份服务在 ID Token(或 userinfo)里给出邮箱:一般申请 `email` scope 即可。

### 配置:在【系统设置 · 会话与安全 · SSO 登录(OIDC)】里开

| 项 | 说明 |
| --- | --- |
| Issuer 地址 | 身份服务的颁发方,`{Issuer}/.well-known/openid-configuration` 必须能取到。与发现文档里的 `issuer` **逐字一致**(包括结尾有没有 `/`) |
| Client ID | 应用的 Client ID |
| Client Secret | 加密存储,从不回显;留空即不改。公开客户端(只用 PKCE)可不填 |
| 回调地址 | 必须以 `/api/v1/auth/oidc/callback` 结尾,与身份服务里登记的一致。旁边的「填入本站地址」按当前浏览器地址推出 |
| Scopes | 默认 `openid email profile`;`openid` 总会带上 |
| 邮箱字段 | 默认 `email`。此时只认身份服务标记为已验证(`email_verified=true`)的邮箱;改成别的字段(如 Azure AD 的 `upn`)表示由管理员担保它就是本人邮箱 |

保存即生效,**不用重启、不改任何文件**。

- 保存时整体校验:Issuer 与回调地址必须是 https(本地开发除外),必填项要齐。任何一项不对,这次一项都不存。
- 改 SSO 设置写审计(`settings.sso ...`),Client Secret 只记"已修改",不记内容。配置存在数据库的
  设置表里,**升级解包碰不到它**。
- 开关开着但 Issuer / Client ID / 回调地址缺了任何一项时,按"未启用"处理。
- ID Token 里没有邮箱时,网关会去 userinfo 端点取(要求 `sub` 与 ID Token 一致)。

### 账号怎么对上

- 登录时按**邮箱**匹配,不分大小写。网关里已有这个邮箱的账号 → 就是这个人,权限原样保留。
- 网关里**没有**这个邮箱 → **拒绝登录**,不会自动建号(接口码 `40302`),登录页弹窗提示"无权限访问",
  并记登录失败审计。由管理员在【权限 · 用户】新建或邀请这个邮箱后再登录。
- 网关里**停用**的账号、**服务账号**:身份服务认也进不来。**受邀**账号:经 SSO 首登即激活。
- 姓名、部门不从身份服务同步(管理员可能在网关里改过)。身份服务给的姓名(`name`,没有时用
  `preferred_username`)记在绑定信息里。
- 登录这一步一般不再要网关的 TOTP;例外是**已绑定 TOTP 的平台管理员**,经 SSO 登录也要输码。
  **生产操作的步进验证照常**。

### 存量账号:登录时自动绑定

**绑定**决定谁**只能**走 SSO。OIDC 没有通用的用户名单接口,所以匹配发生在登录那一刻:

1. 存量账号的主人**第一次经 SSO 登录**、邮箱对上网关账号时,当场绑定,写审计 `sso.bind`。
2. 绑定后,非管理员**只能经 SSO 登录**,用口令登录会被拒并提示改走 SSO。
   **管理员始终保留口令登录**作应急门。
3. 还没经 SSO 登录过的账号不受影响,照常用口令;邮箱在两边写法不一致的人(大小写不算),
   经 SSO 进来会按"没有这个邮箱"被拒绝,原账号不受影响,需要管理员核对处理。
4. 用户列表每一行有 `SSO` 标签显示绑定状态,管理员点已绑定的标签可**解绑**(恢复口令登录,写审计
   `sso.unbind`)。解绑后这个人再经 SSO 登录,会重新绑定。

### 从 SRD 授权登录升级上来

之前接的是 SRD Portal 的私有协议(父域 Cookie `CS-Token`),这一版已移除。升级后:

- 旧的 `sso.baseUrl` / `sso.cookieName` 不再读取。库里没有 Issuer,SSO 按**关闭**处理:登录页只有
  口令表单,**已绑定的账号临时恢复口令登录**。
- 绑定关系保留。管理员配好 OIDC 后,邮箱一致的人经 SSO 登录照常可用,不用重新绑定。
- 一直走 SRD、没有口令的账号(经邀请开通的、只有「业务查询」角色的)在配好 OIDC 之前登不进来,
  请在升级窗口内尽快完成配置。

### 关闭 SSO、退回账号口令登录

测试或上线后发现 SSO 有问题,按下面任一方式关掉,所有人回到账号口令登录:

1. **设置页开关(首选,即改即生效)**:管理员用口令登录(SSO 开着时点登录页的「使用网关账号口令登录」),
   在【系统设置 · 会话与安全】关掉「SSO 登录（OIDC）」并保存。写审计 `settings.sso`。
2. **部署层总闸(进不去设置页时用,需重启)**:在 `config.yaml` 里写

   ```yaml
   sso:
     disabled: true
   ```

   或设环境变量 `AEGISDB_SSO_DISABLED=1`,重启服务。无论设置页存的是什么,SSO 一律按关闭处理,
   启动日志打一条 WARN,设置页的 SSO 开关旁提示"已被部署配置强制关闭"。去掉配置、重启即恢复。

关闭后:

- 登录页只显示账号口令表单;发起登录与 `POST /auth/sso` 一律拒绝。
- **已绑定 SSO 的存量账号**立即恢复口令登录(口令没动过,还是原来那个)。
- **经邀请开通、一直走 SSO 的账号没有口令**,关掉后登不进来:管理员在【权限 · 用户】点该账号的「管理」,
  给他设一个口令即可(写审计 `admin.password.reset`)。
- **只有「业务查询」角色的账号**没有口令,也不允许设口令,SSO 关闭期间无法登录。
- 绑定关系保留。重新打开 SSO 后,已绑定的人又只能走 SSO,不用重新绑定。

### 已知边界

- **身份服务那边下线不会传过来**。网关会话按自己的 TTL(【设置 · 会话与安全】)到期。离职要同时在网关里停用账号。
- 登录页**不会**自动跳身份服务,也不会在没点按钮时自动登录:只有从回调回来(`#/login?sso=1`)才换会话。
  主动点「退出」后回到登录页因此不会一眨眼又登进去;身份服务那边的登录态还在时,再点「使用 SSO 登录」会直接回来。
- 回调失败时登录页按原因提示(未启用、登录已失效、身份服务未授权、身份服务不可用或结果无法验证、没有已验证的邮箱),
  详细原因在网关日志 `sso: 回调失败` 里。
- 身份服务不可用时,管理员仍可用本地口令登录(应急门)。
- 账号只按**邮箱**认人。身份服务把离职人员的邮箱回收再分配时,新主人会进到旧账号 ——
  离职要同时在网关里停用账号。其他已知边界见 `docs/adr/0023-oidc-sso.md`。

## 环境变量一览

| 变量 | 说明 |
| --- | --- |
| `AEGISDB_MYSQL_DSN` | 生产 MySQL DSN(覆盖配置文件) |
| `AEGISDB_POSTGRES_DSN` | PostgreSQL 存储库 DSN,配合 `AEGISDB_DB_DRIVER=postgres` 使用 |
| `AEGISDB_JWT_SECRET` | JWT 签名密钥,**必填 ≥32 位**;未设 `AEGISDB_SECRET_KEY` 时同时派生连接口令静态加密密钥 |
| `AEGISDB_SECRET_KEY` | 至静加密密钥,保护**连接口令与 TOTP 种子**(**强烈建议 ≥32 位**)。设置后与 JWT 密钥解耦,可安全轮换 JWT 密钥而不影响存量解密;**一经设定不可更改**(轮换会导致存量口令与 MFA 种子全部无法解密)。长度不足 32 位或字符种类过少时启动会打 WARN —— 只告警不拒绝启动,因为对一套已在跑的部署,拒启动等于锁死;用 `openssl rand -hex 32` 生成 |
| `AEGISDB_WEB_DIR` | 前端静态目录(默认取配置 `server.web_dir: web`) |
| `AEGISDB_TLS_CERT` / `AEGISDB_TLS_KEY` | 直接由网关终止 TLS 的证书/私钥(可选) |
| `AEGISDB_WEBHOOK_SECRET` | 出站审计 Webhook / 飞书签名密钥(可选) |
| `AEGISDB_SSO_DISABLED` | `1`/`true` 时强制关闭 SSO,所有人回到账号口令登录(覆盖设置页开关,需重启;见第 7 节) |
| `AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED` | `1`/`true` 时管理后台 IP 白名单一律不生效(覆盖设置页,需重启)。白名单配错、管理员都进不了管理后台时用;本机回环本来就始终放行 |
| `APP_ENV` / `AEGISDB_ENV` | `prod`→MySQL,`dev`→PostgreSQL;其他值(如 `staging`)按 dev 处理(同样默认 PostgreSQL),生产类部署请显式设 `AEGISDB_DB_DRIVER` |
| `AEGISDB_DB_DRIVER` | 强制 `mysql` \| `postgres`(覆盖上面的 env 推断;`sqlite` 已不再支持,会报错) |
| `AEGISDB_ADMIN_EMAIL` / `AEGISDB_ADMIN_PASSWORD` / `AEGISDB_ADMIN_NAME` | `init` 时的管理员凭据 |

> 本地开发需要 PostgreSQL(≥ 13):装好后建库,按 `backend/configs/config.yaml` 的 `postgres_dsn` 对齐或设 `AEGISDB_POSTGRES_DSN`,然后 `cd backend && go run ./cmd/server migrate && go run ./cmd/server`,前端 `cd frontend && npm run dev`。详见 README「本地开发」。
