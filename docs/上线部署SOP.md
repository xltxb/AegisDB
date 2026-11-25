# Vela 数据库网关 · 上线部署 SOP

> **这份文档给谁**：执行部署与升级的人（DBA / 运维）。
> **它和 `DEPLOY.md` 的分工**：`DEPLOY.md` 回答"这套东西怎么装起来"，是一次性的安装说明；
> 本文回答"**每次发版照着做什么**、做完怎么验、出了问题怎么定位"，是可以逐行打勾的作业指导。
>
> 生产目录：`/opt/db-gateway` ｜ 反向代理：Nginx（同机，`proxy_pass` 到 `127.0.0.1:8080`）
> ｜ 服务托管：systemd

---

## 零、一页速查

例行升级（**没有新迁移**时）最短路径：

```bash
cd /opt/db-gateway
cp configs/config.yaml /tmp/config.yaml.bak                     # ① 护住本地改过的配置
sudo systemctl stop vela-gateway                                # ② 停
sudo tar -xzf ~/vela-gateway-<版本>-linux-amd64.tar.gz -C /opt/db-gateway   # ③ 解包
cp /tmp/config.yaml.bak configs/config.yaml                     # ④ 解包覆盖了配置,还原成线上那份
set -a; . ./vela.env; set +a                                    # ⑤ 载入环境变量（否则连不上库）
./vela-gateway migrate                                          # ⑥ 迁移（幂等）
sudo systemctl start vela-gateway                               # ⑦ 起
./vela-gateway version && curl -sf localhost:8080/healthz       # ⑧ 验
```

**三个最容易踩的坑**，每一个都在下文有完整说明：

| 坑 | 表现 | 出处 |
|---|---|---|
| 手工跑子命令没载入 `vela.env` | `dial tcp 127.0.0.1:3306: connection refused` | [§5.2](#52-连不上数据库) |
| 解包覆盖了 `configs/config.yaml` | 线上改过的配置（数据库、多层代理的 `trusted_proxies` 等）被模板冲掉 | [§5.3](#53-来源-ip-全是-127001) |
| 先起进程后跑迁移 | 新二进制对着旧表结构跑，报列不存在 | [§3 步骤 6](#步骤-6迁移表结构) |

---

## 一、适用范围与前置条件

### 1.1 适用范围

- **例行升级**：同一台机器上把网关从旧版本换到新版本 → [§3](#三例行升级最常用)
- **首次部署**：一台全新的机器 → [§2](#二首次部署全新环境)
- **纯前端热更**：这一版只动了前端，想免停机 → [§4](#四纯前端热更免停机)

### 1.2 前置条件

| 项 | 要求 | 怎么确认 |
|---|---|---|
| 操作账号 | 能 `sudo`，能读写 `/opt/db-gateway` | `sudo -v` |
| 数据库 | MySQL 8，库已建、账号可连 | `mysql -h<host> -u<user> -p -e 'select 1'` |
| 停机窗口 | 例行升级需要 1–3 分钟（含迁移） | 与业务约定 |
| 备份 | **当天的数据库备份已存在且可用** | 见 [§1.3](#13-备份这一条不能跳) |
| 安装包 | `vela-gateway-<版本>-linux-amd64.tar.gz` 已传到服务器 | `ls -lh ~/vela-gateway-*.tar.gz` |

### 1.3 备份，这一条不能跳

迁移会改表结构，而**迁移之后不能单独回滚二进制**（旧代码按旧列名查询，列已经不在了）。
所以回滚路径只有一条：恢复数据库 + 回旧二进制。没有备份就没有回滚。

```bash
mysqldump --single-transaction --routines --triggers \
  -h<host> -u<user> -p vela_gateway > /backup/vela_gateway_$(date +%F_%H%M).sql
ls -lh /backup/vela_gateway_*.sql | tail -1      # 确认文件非空
```

---

## 二、首次部署（全新环境）

> 已经在跑的环境跳过本节，直接看 [§3](#三例行升级最常用)。

### 步骤 1：建目录与账号

```bash
sudo useradd --system --home /opt/db-gateway --shell /usr/sbin/nologin vela
sudo mkdir -p /opt/db-gateway
sudo tar -xzf ~/vela-gateway-<版本>-linux-amd64.tar.gz -C /opt/db-gateway
```

### 步骤 2：准备 MySQL

```sql
CREATE DATABASE vela_gateway CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'vela'@'%' IDENTIFIED BY '<强密码>';
GRANT ALL ON vela_gateway.* TO 'vela'@'%';
```

### 步骤 3：写 `vela.env`

```bash
cd /opt/db-gateway
sudo cp deploy/vela.env.example vela.env
sudo vi vela.env
sudo chown vela:vela vela.env && sudo chmod 600 vela.env
```

必填三项：

```bash
APP_ENV=prod
VELA_MYSQL_DSN=vela:<密码>@tcp(<mysql主机>:3306)/vela_gateway?charset=utf8mb4&parseTime=true&loc=Local
VELA_JWT_SECRET=<openssl rand -hex 32 的结果>
```

> `VELA_JWT_SECRET` **少于 32 位、还是模板里的占位值、或字符种类太少（一个字符重复凑长度那种），
> 服务会拒绝启动** —— 这是有意的，用 `openssl rand -hex 32` 生成就三条都满足。
>
> 强烈建议同时设 `VELA_SECRET_KEY`（另一个 32 位随机串）。它是连接口令的静态加密密钥，
> 设了之后与 JWT 密钥解耦，日后可以安全轮换 JWT 密钥而不影响存量口令解密。
> **它一经设定不可更改** —— 改了存量口令就解不开了。所以要么第一天就设，要么永远别设。

### 步骤 4：改 `configs/config.yaml`

```bash
sudo vi /opt/db-gateway/configs/config.yaml
```

`server:` 段必须加上这一行（**包里的模板没有它**）：

```yaml
server:
  addr: ":8080"
  mode: "release"
  cors_origins: []
  web_dir: "web"
  trusted_proxies: ["127.0.0.1", "::1"]    # ← 必加，理由见 §5.3
```

### 步骤 5：初始化

```bash
cd /opt/db-gateway
set -a; . ./vela.env; set +a
./vela-gateway init \
  --admin-email <你的邮箱> \
  --admin-password '<强口令>' \
  --admin-name '平台管理员'
```

口令要求 **≥12 位，且含大小写字母 / 数字 / 符号中的 ≥3 类**。
也可以改用环境变量 `VELA_ADMIN_PASSWORD` 传，口令就不会留在 shell history 里。
`init` 会先自动跑迁移，然后写入引用数据（角色、菜单、能力矩阵、风险字典、默认设置）
和**一个**管理员账号 —— 不含任何演示数据。

### 步骤 6：装 systemd unit

```bash
sudo cp /opt/db-gateway/deploy/vela-gateway.service /etc/systemd/system/
sudo vi /etc/systemd/system/vela-gateway.service     # 把路径全部改成 /opt/db-gateway
sudo systemctl daemon-reload
sudo systemctl enable --now vela-gateway
```

> **模板里的路径写的是 `/opt/vela-gateway`，必须改。** 而且
> `EnvironmentFile=-/opt/.../vela.env` 前面那个 `-` 表示**文件不存在就静默跳过** ——
> 路径写错时服务照样起得来、一个环境变量都没有，然后回落到配置里的本地默认 DSN：
> 起得来、连不上，而且不报配置错误。装完务必做 [§6](#六上线后验证清单) 的验证。

### 步骤 7：配 Nginx

参考现网配置（同机反代 + WebSocket 透传 + 长连接超时）：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;                          # WS 必须 HTTP/1.1
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;   # ← 与 trusted_proxies 配套
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 3600s;    # 终端是长连接，默认 120s 会把它掐断
    proxy_send_timeout 3600s;
}
```

三点缺一不可：`proxy_http_version 1.1` + `Upgrade`/`Connection` 两个头（否则 Web 终端连不上，
会静默降级成 REST）、`X-Forwarded-For`（否则审计记不到真实 IP）、`proxy_read_timeout`
（否则跑得久的语句会在 2 分钟时断线）。

### 步骤 8：走一遍 [§6 上线后验证清单](#六上线后验证清单)

---

## 三、例行升级（最常用）

### 步骤 0：确认这一版要不要跑迁移

```bash
# 包里最高的迁移编号
tar -tzf ~/vela-gateway-<新版本>-linux-amd64.tar.gz | grep migrations | tail -1
# 当前库里已应用到哪一条
mysql -h<host> -u<user> -p vela_gateway -e "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 3;"
```

两边一致 → 这一版**没有新迁移**，`migrate` 那一步只是走个过场（仍然要跑，它是幂等的）。
包里更高 → 有新迁移，[§1.3 的备份](#13-备份这一条不能跳)**必须**先做。

### 步骤 1：备份数据库

见 [§1.3](#13-备份这一条不能跳)。

### 步骤 2：备份当前配置

```bash
cd /opt/db-gateway
cp configs/config.yaml /tmp/config.yaml.bak
cp vela.env /tmp/vela.env.bak
```

**解包会覆盖 `configs/config.yaml`** —— 你在里面改过的东西会被模板冲掉。模板自 2026-10 起已自带
`trusted_proxies: ["127.0.0.1", "::1"]`（同机 Nginx 够用），但多层代理等改动仍要靠这份备份还原。
`vela.env` 不在包里，不会被覆盖，但一起备份不花钱。

### 步骤 3：停服务

```bash
sudo systemctl stop vela-gateway
sudo systemctl status vela-gateway --no-pager | head -5    # 确认真的停了
```

### 步骤 4：留一份旧二进制（回滚用）

```bash
cp /opt/db-gateway/vela-gateway /tmp/vela-gateway.$(date +%F).bak
```

### 步骤 5：解包

```bash
sudo tar -xzf ~/vela-gateway-<新版本>-linux-amd64.tar.gz -C /opt/db-gateway
sudo chown -R vela:vela /opt/db-gateway
```

立刻把线上那份配置还原回去（模板现在本身就带 `trusted_proxies`，不能再靠 grep 它来判断有没有被冲掉）：

```bash
sudo cp /tmp/config.yaml.bak /opt/db-gateway/configs/config.yaml
grep -n trusted_proxies /opt/db-gateway/configs/config.yaml   # 还原之后必须还在
```

> ⚠️ **发版注意（登录 IP 白名单）**：还原的是**旧备份**时，它可能比模板还老、根本没有 `trusted_proxies`。
> 同机 Nginx 带 `X-Forwarded-For` / `X-Real-IP`、又开着登录 IP 白名单时，缺这一项会让**全员登录不了**
> （2026-10 起回环地址只在不带转发头时放行，见 [§5.3](#53-来源-ip-全是-127001)）。上面的 grep 没输出就补上
> `trusted_proxies: ["127.0.0.1", "::1"]` 再起服务。网关检测到这种情况会在日志里打 WARN
> `login ip allowlist: request from loopback carries X-Forwarded-For/X-Real-IP but the local proxy is not trusted`
> （每 10 分钟至多一条）。

### 步骤 6：迁移表结构

```bash
cd /opt/db-gateway
set -a; . ./vela.env; set +a      # ← 这一行不能省，理由见 §5.2
./vela-gateway migrate
```

期望输出形如：

```
{"level":"INFO","msg":"migration complete","driver":"mysql","env":"prod"}
```

> **顺序不能反。** 必须「停旧进程 → migrate → 起新进程」。
> 反过来做，新二进制会对着旧表结构跑，表现是查询报"列不存在"。
> 这类失败朝"关"的方向（规则查不到就拒绝），不会变成放行，但会是一场故障。

### 步骤 7：起服务

```bash
sudo systemctl start vela-gateway
sudo systemctl status vela-gateway --no-pager | head -10
```

### 步骤 8：走 [§6 上线后验证清单](#六上线后验证清单)

---

## 四、纯前端热更（免停机）

**仅当这一版只动了前端**（没有后端改动、没有新迁移）时可用。判断依据：发版说明里写明
"纯前端"，或者 `git log` 里这几笔只碰了 `frontend/`。

```bash
cd /opt/db-gateway
cp -r web /tmp/web.bak.$(date +%F)          # 留退路
sudo rm -rf web/assets                       # 删旧资源：文件名带哈希，不删会越堆越多
sudo unzip -o ~/vela-web-<版本>.zip -d web/
sudo chown -R vela:vela web
```

**不用重启，不用迁移。** `index.html` 是 no-cache 的，用户刷新一次就拿到新版本。

> 打这个 zip 的人注意：**必须用 Go 的 `archive/zip` 打**。Windows 上 `tar -a -c -f x.zip`
> 产出的是"tar 假 zip"，服务器上 `unzip` 会报
> `End-of-central-directory signature not found`。

---

## 五、常见故障与定位

### 5.1 服务起不来

```bash
sudo journalctl -u vela-gateway -n 50 --no-pager
```

| 日志关键字 | 原因 | 处理 |
|---|---|---|
| `invalid configuration` / JWT 相关 | `VELA_JWT_SECRET` 缺失、是占位值、或不足 32 位 | 补 `vela.env`，`openssl rand -hex 32` |
| `trusted_proxies 配置非法` | 写了不是 IP 也不是 CIDR 的值 | 改成 `["127.0.0.1", "::1"]` |
| `database open failed` | 见 [§5.2](#52-连不上数据库) | |
| `bind: address already in use` | 旧进程没停干净 | `sudo systemctl stop vela-gateway`，再 `ss -lntp \| grep 8080` |

### 5.2 连不上数据库

```
[error] failed to initialize database, got error dial tcp 127.0.0.1:3306: connect: connection refused
{"level":"ERROR","msg":"database open failed","err":"connect mysql ..."}
```

**看到 `127.0.0.1:3306` 基本可以断定：环境变量没载入。**

真正的 DSN 来自 `VELA_MYSQL_DSN`，而那个变量是 **systemd 通过 `EnvironmentFile=` 注入给
服务进程**的 —— 你在 shell 里手工跑 `./vela-gateway migrate` 时它并不存在，配置就回落到
`configs/config.yaml` 里那条本地默认 DSN。

```bash
cd /opt/db-gateway
set -a; . ./vela.env; set +a
./vela-gateway migrate
```

确认环境变量确实在：

```bash
ls -l /opt/db-gateway/vela.env                  # 存在？权限应为 600
grep -c VELA_MYSQL_DSN /opt/db-gateway/vela.env # 有这一行？
grep EnvironmentFile /etc/systemd/system/vela-gateway.service   # systemd 指向哪个文件？
```

> `verify-audit`、`init` 同理 —— 凡是要连库的子命令，都要先载入。

### 5.3 来源 IP 全是 `127.0.0.1`

审计日志里每一行的「来源 IP」都是 `127.0.0.1`（或空）。

**原因**：网关前面有 Nginx，它看到的 TCP 对端就是本机的代理。`ClientIP()` 只在**配置过的
可信代理**后面才认 `X-Forwarded-For`，而默认是"一个代理都不信"（这是防伪造的安全默认值）。

**处理**：`configs/config.yaml` 的 `server:` 段加

```yaml
  trusted_proxies: ["127.0.0.1", "::1"]
```

然后 `sudo systemctl restart vela-gateway`。

三件要一起确认：

1. **Nginx 真的在传** —— `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`
2. **多层代理要全列**（SLB → Nginx → 网关）。gin 从 XFF 最右往左走，遇到第一个不在可信
   列表里的地址就停下当作客户端 —— 漏配中间那层，拿到的会是 Nginx 的地址。
3. **填错格式会拒绝启动**并指出是哪一项。这是有意的：gin 拿到非法值时会悄悄退回
   "信任所有代理"，那比不配更危险。

> ⚠️ **这一条同时关系到登录 IP 白名单和管理后台 IP 白名单。** 两者读的是同一个 `ClientIP()`。
> 回环地址只在**本机直接访问**（不带 `X-Forwarded-For` / `X-Real-IP`）时放行（2026-10 起；
> 之前是对 loopback 一律放行，没配 `trusted_proxies` 时白名单形同虚设）。现在没配 `trusted_proxies`
> 时，经 Nginx 转来的请求来源都是 `127.0.0.1` 又带着转发头，**一律不放行** —— 白名单一打开就把
> 所有人挡在外面。所以先确认 `trusted_proxies` 配好、审计里看得到真实 IP，**再把自己的出口网段
> 填进白名单**，最后才打开开关。
>
> 真锁死了的退路：在服务器上绕过 Nginx 直接打网关，那才是真正的 loopback：
> `curl -X POST http://127.0.0.1:8080/api/v1/auth/login ...`
>
> 怎么从日志认出这种情况：登录 IP 白名单开着、收到带转发头的本机请求而本机代理没被信任时，网关打一条
> WARN `login ip allowlist: request from loopback carries X-Forwarded-For/X-Real-IP but the local proxy is not trusted (server.trusted_proxies not configured?)`
> （每 10 分钟至多一条）。看到它就按上面补 `trusted_proxies` 并重启。

> **已有的审计行不会变**，`127.0.0.1` 就留在那儿，不回填。那一行当时记下的确实就是这个；
> 事后改它会被 `verify-audit` 当成篡改抓出来。

### 5.4 Web 终端连不上 / 跑久了断线

| 现象 | 原因 | 处理 |
|---|---|---|
| 终端一直"连接中"，功能降级成 REST | Nginx 少了 `proxy_http_version 1.1` 或 `Upgrade`/`Connection` 头 | 见 [§2 步骤 7](#步骤-7配-nginx) |
| 语句跑到两分钟就断 | `proxy_read_timeout` 是默认的 60/120s | 调到 `3600s` |

### 5.5 审计链校验报某一行对不上

```
审计链在第 N 行断开(... id=xxx):这一行的内容被改过(重算的哈希与存着的不一致)
```

**先看断点落在哪儿**：

- 断在**很老的行**上 → 多半是分代键集没覆盖到（不是有人动过）。载荷的键集随迁移长过五代
  （0039 的 `event`、0040 的 `action`、0041 的 `ip`），校验器逐代重试，但更老的部署可能有
  代码里没登记的变体。**把 `verify-audit` 的输出发给维护者扩表**，不要当成事故。
- 断在**近期的行**上 → 值得紧张。结论有三种，要人做的事不同：

  | 结论 | 含义 | 下一步 |
  |---|---|---|
  | `content` | 这一行的内容被改过 | 去查它原来是什么（对比备份 / SIEM 里那份） |
  | `link` | 前驱哈希接不上上一行 | 中间有行被删除或插入，去找那一行 |
  | `gen` | 按更老的键集才算得过 | 像是有人抹掉了较新字段再重算 |

> **这道防线挡得住什么**：它挡的是"顺手改一行"。载荷是裸 SHA-256、算法公开，拿得到数据库
> 写权限的人可以把整条尾巴重算一遍而链照样自洽。真正的核对发生在**网关之外** —— 网关自己
> 的库是可写的，Webhook 推到 SIEM / 数据湖的那份是只进不出的（`raw_payload` 里带着 `hash`
> 与 `prevHash`），两边对一下才叫核对。

---

## 六、上线后验证清单

每一项都要实际做，不要"看起来没问题"。

### 6.1 进程与版本

```bash
cd /opt/db-gateway && set -a; . ./vela.env; set +a
./vela-gateway version                      # 版本号是不是这次要发的那个
curl -s localhost:8080/healthz             # 健康检查;auditWriteFailures 应为 0
sudo systemctl status vela-gateway --no-pager | head -5
```

### 6.2 看一眼启动日志里的「审批人自检」

```bash
sudo journalctl -u vela-gateway -n 100 --no-pager | grep -i "审批人自检"
```

每次启动会检查"这些审批环节到底还有没有人能批"，命中打 WARN（不阻止启动）：

| 情形 | 后果 |
|---|---|
| 角色没有成员，或成员全是服务账号 | 工单根本建不出来 |
| 链上**恰好一个人**且未开自审批 | 工单建得出来，但由他本人发起的**永远没人能批** |
| 启动那一刻已经卡住的存量待办 | 现在就已经没人能处理 |

第二条最容易漏：每一层校验都"通过"，然后两人控制让它们一张都批不掉，单子静静堆在待办里，
不报错也不推进。两条出路：给该角色**再加一名真人**（推荐），或在【设置】里打开
**允许自审批**（单人运维的逃生门，它只解除"自己不能审自己"，不会把人放上审批链）。

### 6.3 登录与基本功能

- [ ] 浏览器打开 `https://<域名>`，用管理员登录
- [ ] 顶栏「网关在线」有延迟数字
- [ ] 进【终端】，在**开发环境**实例上跑一条 `SELECT 1` —— 有结果表
- [ ] 在**生产**实例上跑一条高危语句（如 `DROP TABLE不存在的表`）—— 应当被拦截并生成 `AP-xxxx`
- [ ] 该工单出现在【审批】列表里

### 6.4 来源 IP（本次发版新增，务必验）

- [ ] 进【审计】页，看最新那条登录记录的「来源 IP」列
- [ ] 应当是**你的真实出口地址**，不是 `127.0.0.1`
- [ ] 再去【终端】跑一条 SQL（走 WebSocket，与登录不是同一条认证路径），回到审计页确认那一行也有真实 IP

若仍是 `127.0.0.1` → [§5.3](#53-来源-ip-全是-127001)

### 6.5 审计链基线

```bash
cd /opt/db-gateway && set -a; . ./vela.env; set +a
./vela-gateway verify-audit
```

期望：

```
审计链完好:验过 N / N 行(id 1..N),当前键集第 5 代
```

**记下这个行数**。以后每次巡检对比。报错见 [§5.5](#55-审计链校验报某一行对不上)。

### 6.6 待办铃声（本次发版新增）

- [ ] 右上角「待办」点开，面板头上有个**铃铛按钮**
- [ ] 点成"打开"状态会**立刻响一声** —— 用它验证这台机器的声音通不通
- [ ] 若你名下有"已通过待执行"的单，重新登录一次应当**响三声**并弹提示

> 铃声只对**发起人**响，只对"批过了等我执行"的单响。别人提的单你能执行、但不会响 ——
> 这是刻意的：值班的人一登录就被自己没提过的单子叫一通，几天之后这声音就只剩噪音。
> 刷新页面不响（浏览器的自动播放策略要求有用户手势，登录那一下点击才是）。

### 6.7 下游兼容（发版说明提到时才需要）

- [ ] **审计 CSV 导出的列变了**：`actor` 之后多一列 `client_ip`。按**列号**取数的下游会错位；
      按列名取的不受影响。通知相关方。

---

## 七、回滚

### 7.1 能不能回滚，先看有没有跑过迁移

| 情况 | 能否只回二进制 |
|---|---|
| 这一版**没有新迁移** | ✅ 可以，见 [§7.2](#72-无迁移回滚) |
| 这一版**跑过新迁移** | ❌ 不行，见 [§7.3](#73-有迁移回滚) |

### 7.2 无迁移回滚

```bash
sudo systemctl stop vela-gateway
sudo cp /tmp/vela-gateway.<日期>.bak /opt/db-gateway/vela-gateway
sudo cp /tmp/config.yaml.bak /opt/db-gateway/configs/config.yaml
sudo chown vela:vela /opt/db-gateway/vela-gateway
sudo systemctl start vela-gateway
./vela-gateway version      # 确认是旧版本
```

前端也要一起退（`web/` 在包里）：`sudo rm -rf web && sudo cp -r /tmp/web.bak.<日期> web`

### 7.3 有迁移回滚

**必须连数据库一起退**，否则旧代码按旧列名查询，而列已经不在了：

```bash
sudo systemctl stop vela-gateway
mysql -h<host> -u<user> -p vela_gateway < /backup/vela_gateway_<时间戳>.sql   # 恢复备份
sudo cp /tmp/vela-gateway.<日期>.bak /opt/db-gateway/vela-gateway
sudo systemctl start vela-gateway
```

> **代价是备份时刻之后的所有数据都没了** —— 包括那段时间的审计行。
> 所以有迁移的版本，宁可向前修，不要向后退。

---

## 八、日常运维

| 事项 | 频率 | 命令 / 位置 |
|---|---|---|
| 审计链校验 | 每天（建议挂 cron） | `./vela-gateway verify-audit`，**退出码 2 = 链断了**，直接接告警 |
| 审计写入失败数 | 随健康检查（建议 1 分钟） | `curl -s localhost:8080/healthz` 里的 `auditWriteFailures`，**非零即告警**（见下） |
| 在备份上校验 | 每月 | 把备份恢复到一台临时库，对它跑一次 `verify-audit` —— 拿今天的库和上周的备份各验一次，是这条链最有用的用法 |
| 数据库备份 | 每天 | `mysqldump`，见 [§1.3](#13-备份这一条不能跳) |
| 看审批人自检 | 每次重启后 | `journalctl -u vela-gateway \| grep 审批人自检` |
| 导出归档清理 | 自动 | 由网关按【设置 · 网关】的保留天数每小时清理 |

> **`auditWriteFailures` 是什么**：它计的是"动作已经发生、审计行没写成"的次数，平时恒为 0。
> 一旦不是 0，说明审计链上缺了行 —— 那是**当天就该知道**的事，而不是等下一次 `verify-audit`
> 发现链上断了一段再回头查。网关在这种情况下不会阻断动作（动作已经落在目标库上，回滚不了），
> 所以这个计数就是唯一的出口。看到它涨，先去 `journalctl` 里搜 `AUDIT WRITE FAILED`，那行
> 日志带着操作人、结果与命令片段。

cron 示例（每天 3 点校验审计链，断了就发邮件）：

```cron
0 3 * * * cd /opt/db-gateway && set -a && . ./vela.env && set +a && ./vela-gateway verify-audit || echo "审计链校验失败" | mail -s "[告警] Vela 审计链" ops@corp.io
```

---

## 附录 A：子命令

| 命令 | 作用 | 退出码 |
|---|---|---|
| `./vela-gateway version` | 打印版本 | 0 |
| `./vela-gateway migrate` | 只应用待执行的迁移（幂等） | 0 / 1 |
| `./vela-gateway init --admin-email … --admin-password …` | 迁移 + 引用数据 + 建管理员（仅首次） | 0 / 1 |
| `./vela-gateway verify-audit [-limit N]` | 重算审计哈希链 | 0 完好 / **2 链断了** / 1 出错 |
| `./vela-gateway migrate-store --from-mysql … --to-postgres … --tz …` | 存储库从 MySQL 一次性搬到 PostgreSQL（停机，见 `上线部署SOP-存储库搬迁到PG.md`） | 0 搬完且核对通过 / 1 未完成 |
| `./vela-gateway -config configs/config.yaml` | 直接运行服务（systemd 用这个） | — |

**所有要连库的子命令，跑之前都要 `set -a; . ./vela.env; set +a`。**

## 附录 B：关键环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `APP_ENV` | ✅ | `prod` → MySQL |
| `VELA_MYSQL_DSN` | ✅ | 生产 DSN，覆盖配置文件 |
| `VELA_DB_DRIVER` | 搬到 PG 后 ✅ | 写 `postgres` 即使用 PostgreSQL 存储库（**只认环境变量**，配置文件里的 driver 在 prod 下不生效） |
| `VELA_POSTGRES_DSN` | 搬到 PG 后 ✅ | PG 存储库 DSN（libpq 键值串或 URL） |
| `VELA_JWT_SECRET` | ✅ | **≥32 位**，否则拒绝启动 |
| `VELA_SECRET_KEY` | 建议 | 连接口令静态加密密钥。**一经设定不可更改** |
| `VELA_TLS_CERT` / `VELA_TLS_KEY` | 可选 | 让网关自己终止 TLS（现网由 Nginx 终止，不需要） |

完整清单见 `DEPLOY.md` 的「环境变量一览」。

## 附录 C：目录结构

```
/opt/db-gateway/
├─ vela-gateway          单二进制（API + 前端 + 内嵌迁移）
├─ vela.env              环境变量（600，**不在包里，解包不会覆盖**）
├─ configs/config.yaml   配置（**在包里，解包会覆盖** —— 每次升级都要检查 trusted_proxies）
├─ web/                  前端静态资源（在包里，纯前端热更只换它）
├─ migrations/           迁移 SQL（同样已内嵌进二进制，这里是留档）
├─ deploy/               systemd unit + env 模板（路径写的是 /opt/vela-gateway，装的时候要改）
└─ README-DEPLOY.md      包内速查
```

## 附录 D：相关文档

| 文档 | 回答什么 |
|---|---|
| `DEPLOY.md` | 安装说明：打包产物、MySQL 准备、环境变量全表 |
| `README.md` | 这套系统是什么、判定链怎么走、每个功能为什么这样设计 |
| `docs/控制台接口说明.md` | 控制台 API 按业务面分组，每条路由的守卫 |
| `docs/开放接口对接文档.md` | 外部系统提单：凭据、幂等、轮询、错误码 |
| `backend/docs/openapi.yaml` | 完整接口契约（运行时 `GET /openapi.yaml`） |
