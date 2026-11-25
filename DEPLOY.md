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

## 7. 接入 SRD 授权登录(公司 SSO,可选)

SSO **只做认证**:SRD Portal 回答"你是谁"(邮箱),网关照旧用自己的角色 / 菜单 / 能力矩阵回答
"你能干什么"。判定链不因接入 SSO 而改变。对接依据是《SRD Portal 授权登录对接文档》。

**原理**:

1. 登录页点「使用 SRD 授权登录」。浏览器里还没有 SRD 登录态时,跳到
   `{SRD 地址}/auth/login?redirect=<网关登录页>`,在 SRD 选租户、完成 Lark 授权。
2. SRD 在公司父域(`bssrvc66.com`)写 `CS-Token` Cookie,跳回网关登录页。
3. 登录页自动调网关的 `POST /api/v1/auth/sso`,浏览器随请求带上 `CS-Token`。
4. 网关服务端从 `CS-Token` 解出 JWT,带 `Authorization: Bearer <jwt>` 调 SRD 的
   `GET /api/v1/user/oauth/userinfo`,按返回的**邮箱**找到网关用户,签发网关自己的会话。
   之后每个请求不再回 SRD。

**前提**:

- 网关域名必须在 `CS-Token` 所在父域之下,例如 `dbgw.bssrvc66.com`。不同主域读不到这个 Cookie,
  SRD 文档也写明了这一点。
- 网关的回跳地址要被 SRD 接受:同主域的子域默认允许;否则请 SRD 把网关的 origin 加进
  `VUE_APP_SSO_ALLOWED_REDIRECT_ORIGINS`。回跳地址必须是 https。
- 网关所在机器要能访问 SRD 的 `https://srd.bssrvc66.com`。

### 配置:在【系统设置 · 会话与安全 · SRD 授权登录】里开

管理员在界面上打开开关,填 **SRD 授权登录地址**(如 `https://srd.bssrvc66.com`,登录入口与用户信息
接口都挂在它下面)和**登录态 Cookie 名**(默认 `CS-Token`)。保存即生效,**不用重启、不改任何文件**。

- 保存时整体校验:地址必须是 https(本地开发除外)。任何一项不对,这次一项都不存。
- 开关 SSO 写审计(`settings.sso ...`)。配置存在数据库的设置表里,**升级解包碰不到它**。
- 开关开着但地址被清空时,按"未启用"处理。

### 账号怎么对上

- 登录时按**邮箱**匹配,不分大小写。网关里已有这个邮箱的账号 → 就是这个人,权限原样保留。
- 网关里**没有**这个邮箱 → 自动建号,**不分配任何角色**,并直接记为已绑定;能登录但看不到任何菜单,
  登录页提示"等待管理员分配角色"。建号写审计 `sso.provision`。
- 网关里**停用**的账号、**服务账号**:SRD 认也进不来。**受邀**账号:经 SRD 首登即激活。
- 部门同步自 SRD userinfo 的 `internal_department`(为空时退回 `department`);姓名不覆盖(管理员可能在网关里改过)。
- 登录这一步不再要网关的 TOTP;**生产操作的步进验证照常**。

### 存量账号:登录时自动绑定

**绑定**决定谁**只能**走 SSO。SRD 没有提供用户名单接口,所以匹配发生在登录那一刻:

1. 存量账号的主人**第一次经 SRD 授权登录**、邮箱对上网关账号时,当场绑定,写审计 `sso.bind`。
2. 绑定后,非管理员**只能经 SSO 登录**,用口令登录会被拒并提示改走 SSO。
   **管理员始终保留口令登录**作应急门。
3. 还没经 SRD 登录过的账号不受影响,照常用口令;邮箱在两边写法不一致的人,经 SRD 进来得到的是
   一个新的零权限账号,原账号不受影响,需要管理员核对处理。
4. 用户列表每一行有 `SSO` 标签显示绑定状态,管理员点已绑定的标签可**解绑**(恢复口令登录,写审计
   `sso.unbind`)。解绑后这个人再经 SRD 登录,会重新绑定。

### CS-Token 的编码

`CS-Token` 是"SRD 自定义 base64"编码后的 `{"t_time": 秒级时间戳, "token": "<jwt>"}`,只有这一层。
SRD 文档没给实现,2026-09-26 从 SRD 线上前端包(`app.8db67ec2.js`,源文件 `src/assets/js/encryption.js`)
里核实:算法就是标准 base64,只是**换了字母表、填充符是 `*`**:

```
GedC6kERMS.0H3r5tXUD9o_LyVNaIf2JwWOlYZzvgjs8bpi7~TPAch14QnqKx-Fu   (填充 *)
```

网关按这个字母表解;标准 / URL 安全 base64、URL 编码过的形态、直接是 JWT 的 Cookie 值也都认。

**SRD 升级换了字母表时**,登录会得到"无法解析 SRD 登录态 Cookie"的提示,登录失败审计里有记录,
网关日志里有一行 `sso: CS-Token 解不开`,带 Cookie 的长度与字符集(不含内容)。处理办法:下载 SRD
当前的 `app.*.js`,用 node 跑其中的 `u()` 打出新字母表,改后端 `srdBase64` 一处,并用 SRD 原函数
重新生成 `sso_settings_test.go` 里的测试向量。不要照源码肉眼抄:其中一段含不可见字符。
`t_time` 超过 24 小时的登录态按"没有登录态"处理,与 SRD 前端一致。

### 关闭 SSO、退回账号口令登录

测试或上线后发现 SSO 有问题,按下面任一方式关掉,所有人回到账号口令登录:

1. **设置页开关(首选,即改即生效)**:管理员用口令登录(SSO 开着时点登录页的「使用网关账号口令登录」),
   在【系统设置 · 会话与安全】关掉「SRD 授权登录（公司 SSO）」并保存。写审计 `settings.sso`。
2. **部署层总闸(进不去设置页时用,需重启)**:在 `config.yaml` 里写

   ```yaml
   sso:
     disabled: true
   ```

   或设环境变量 `AEGISDB_SSO_DISABLED=1`,重启服务。无论设置页存的是什么,SSO 一律按关闭处理,
   启动日志打一条 WARN,设置页的 SSO 开关旁提示"已被部署配置强制关闭"。去掉配置、重启即恢复。

关闭后:

- 登录页只显示账号口令表单,不再自动跳 SRD;`POST /auth/sso` 一律拒绝。
- **已绑定 SSO 的存量账号**立即恢复口令登录(口令没动过,还是原来那个)。
- **首登时由 SSO 自动开通的账号没有口令**,关掉后登不进来:管理员在【权限 · 用户】点该账号的「管理」,
  给他设一个口令即可(写审计 `admin.password.reset`)。
- 绑定关系保留。重新打开 SSO 后,已绑定的人又只能走 SSO,不用重新绑定。

### 已知边界

- **SRD 那边下线不会传过来**。网关会话按自己的 TTL(【设置 · 会话与安全】)到期。离职要同时在网关里停用账号。
- 主动点「退出」后,本标签页回到登录页**不会**自动再用 SSO 登进来(否则一眨眼又登录了);
  再点「使用 SRD 授权登录」即可。
- 刚从 SRD 授权回来,网关却仍然收不到登录态时,登录页会直接说明是 Cookie 没带过来,
  多半是网关域名不在 SRD Cookie 的父域下。
- SRD 不可用时,管理员仍可用本地口令登录(应急门)。

## 环境变量一览

| 变量 | 说明 |
| --- | --- |
| `AEGISDB_MYSQL_DSN` | 生产 MySQL DSN(覆盖配置文件) |
| `AEGISDB_JWT_SECRET` | JWT 签名密钥,**必填 ≥32 位**;未设 `AEGISDB_SECRET_KEY` 时同时派生连接口令静态加密密钥 |
| `AEGISDB_SECRET_KEY` | 至静加密密钥,保护**连接口令与 TOTP 种子**(**强烈建议 ≥32 位**)。设置后与 JWT 密钥解耦,可安全轮换 JWT 密钥而不影响存量解密;**一经设定不可更改**(轮换会导致存量口令与 MFA 种子全部无法解密)。长度不足 32 位或字符种类过少时启动会打 WARN —— 只告警不拒绝启动,因为对一套已在跑的部署,拒启动等于锁死;用 `openssl rand -hex 32` 生成 |
| `AEGISDB_WEB_DIR` | 前端静态目录(默认取配置 `server.web_dir: web`) |
| `AEGISDB_TLS_CERT` / `AEGISDB_TLS_KEY` | 直接由网关终止 TLS 的证书/私钥(可选) |
| `AEGISDB_WEBHOOK_SECRET` | 出站审计 Webhook / 飞书签名密钥(可选) |
| `AEGISDB_SSO_DISABLED` | `1`/`true` 时强制关闭 SSO,所有人回到账号口令登录(覆盖设置页开关,需重启;见第 7 节) |
| `AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED` | `1`/`true` 时管理后台 IP 白名单一律不生效(覆盖设置页,需重启)。白名单配错、管理员都进不了管理后台时用;本机回环本来就始终放行 |
| `APP_ENV` / `AEGISDB_ENV` | `prod`→MySQL,`dev`→SQLite |
| `AEGISDB_DB_DRIVER` | 强制 `mysql` \| `sqlite`(覆盖上面的 env 推断) |
| `AEGISDB_ADMIN_EMAIL` / `AEGISDB_ADMIN_PASSWORD` / `AEGISDB_ADMIN_NAME` | `init` 时的管理员凭据 |

> 本地开发仍用零依赖 SQLite:`cd backend && APP_ENV=dev go run ./cmd/server`,前端 `cd frontend && npm run dev`。
