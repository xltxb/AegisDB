# AegisDB 数据库管理网关

AegisDB 是面向 DBA、研发与运维团队的统一数据库访问网关。终端、脚本、后台任务、数据导出、
发布流水线和开放接口发起的数据库操作都先经过网关**逐语句判定**：放行、拦截，或转审批。

- **先判定，再下发**：能力矩阵、高危命令字典、无 WHERE 拦截都按环境分层配置。
- **审批与执行分开**：命令审批通过不会碰数据库，由有权限的人回来点「执行」，执行前再判一次。
- **全程留痕**：每次操作写进哈希链审计表，可当场重算校验，也可经 Webhook / 飞书外推。
- **真实执行**：支持 MySQL 系、PostgreSQL / DWS、Oracle、SQLite；单二进制交付，无 CDN 依赖。

## 目录

[快速开始](#快速开始) · [架构](#架构) · [功能](#功能) · [安全模型](#安全模型) ·
[配置](#配置) · [生产部署](#生产部署) · [API](#api) · [测试](#测试) · [文档](#文档)

---

## 快速开始

### 环境要求

| 依赖 | 版本 | 说明 |
|---|---|---|
| Go | ≥ 1.23 | 纯 Go，不需要 cgo |
| Node.js | ≥ 22.22 | 前端构建与测试 |
| Docker | 可选 | 只有用 PostgreSQL / MySQL 做本地存储库时才需要 |

### macOS / Linux（零依赖，SQLite）

```bash
# 终端 1：后端。默认 dev 环境，写 backend/aegisdb.db，自动建表并灌入引用数据
cd backend
go run ./cmd/server          # 127.0.0.1:8080，健康检查 GET /healthz

# 终端 2：前端
cd frontend
npm ci
npm run dev                  # http://localhost:5173，/api 代理到 :8080
```

### Windows

| 脚本 | 作用 |
|---|---|
| `start-dev.bat` | 分两个窗口启动后端（PostgreSQL 存储库）与前端 |
| `backend\run-sqlite.bat` | 只启动后端，SQLite，零依赖 |
| `backend\run-pg.bat` | 只启动后端，PostgreSQL 存储库 |
| `backend\run-mysql.bat` | 以 prod 配置启动，需先 `docker compose up -d` |
| `frontend\run-dev.bat` | 只启动前端，首次运行自动装依赖 |

`run-pg.bat` 与 `start-dev.bat` 要求本机已有容器 `aegisdb-store-pg`，首次使用先创建：

```bash
docker run -d --name aegisdb-store-pg -p 127.0.0.1:5437:5432 \
  -e POSTGRES_PASSWORD=aegisdb_test postgres:16
```

`run-mysql.bat` 走 prod 配置，必须先设置 `AEGISDB_JWT_SECRET`（≥ 32 位），否则拒绝启动。

### 登录

| 账号 | 密码 | 角色 |
|---|---|---|
| `linwei@aegisdb.io` | `aegisdb123` | 平台管理员 |

全新的本地库只有这一个账号，其他人在【管理后台 · 权限】里创建。接口文档在
http://localhost:8080/docs （仅本地开发环境挂载）。

> **两点注意**
> - Vite 监听所有网卡并代理 `/api`，同网段的人能用上面的公开密码登录。在不可信网络里请改用
>   `npm run dev -- --host 127.0.0.1`。
> - 前端端口钉死在 5173（`strictPort`），被占用时直接报错退出，不会自动换端口。

没有填连接凭据的实例在本地开发环境走**仿真执行**（合成结果，审计记为「模拟(未执行)」）；
其他环境一律拒绝。清单见 `docs/simulated-paths.md`。

---

## 架构

```
浏览器 (React SPA)      外部系统 / CI
   │ Cookie + CSRF         │ key.secret
   ▼                       ▼
┌──────────────────── AegisDB 网关 ────────────────────┐
│ 鉴权 → 菜单权限 → 能力矩阵 → 高危字典 → 无 WHERE 拦截 │
│        │ 放行            │ 转审批           │ 拒绝    │
│        ▼                 ▼                  ▼         │
│   执行 / 脱敏       审批链 → 人工执行    返回原因      │
│        └────────── 哈希链审计 → Webhook / 飞书 ──────┤
└──────────┬──────────────────────────────┬────────────┘
           ▼                              ▼
   目标库（MySQL 系 / PG / Oracle …）   自身存储库
```

```
AegisDB/
├─ backend/
│  ├─ cmd/server/        入口与子命令
│  ├─ configs/           config.yaml（开发）/ config.prod.yaml（打包进生产）
│  ├─ docs/openapi.yaml  接口契约
│  ├─ internal/
│  │  ├─ bootstrap/      配置、路由、迁移、种子数据、黑盒回归测试
│  │  ├─ gateway/        风险判定、方言、真实执行、脱敏
│  │  ├─ handler/ service/ repository/ model/ dto/ middleware/
│  │  ├─ sqlparse/       SQL 解析器（只用来让判定更严）
│  │  ├─ review/         数据库规范审查规则
│  │  ├─ osc/            MySQL 在线表结构变更
│  │  ├─ chat/           聊天审批卡片的渠道（Lark、Telegram）
│  │  ├─ schemadiff/     表结构对比
│  │  └─ storemigrate/   存储库 MySQL → PostgreSQL 搬迁
│  ├─ migrations/        MySQL 迁移；pg/ 下是 PostgreSQL 基线
│  └─ third_party/omni/  vendored SQL 解析器（MIT）
├─ frontend/
│  ├─ src/               views / components / stores / lib / locales（zh、en）
│  ├─ e2e/  tests/unit/  Playwright 测试
├─ docs/                 ADR、部署 SOP、接口文档、数据库规范、设计稿
├─ deploy/               systemd unit 与环境变量模板
├─ build.sh              一键打包
└─ docker-compose.yml    本地 MySQL 8（Redis 目前未被后端使用）
```

| 层 | 选型 |
|---|---|
| 前端 | React 19 · TypeScript · Vite · Zustand · React Router · @intlify/core-base（词条构建期校验）· xterm.js |
| 后端 | Go · Gin · GORM · gorilla/websocket · JWT · bcrypt · AES-GCM · TOTP |
| 目标库 | MySQL · PolarDB · Aurora MySQL · TiDB · MariaDB · PostgreSQL · GaussDB (DWS) · Oracle · SQLite |
| 自身存储 | MySQL 8 / PostgreSQL（生产）· SQLite（本地开发） |

MongoDB、Redis、ClickHouse 不支持执行：判定引擎解析的是 SQL，读不懂的命令会被误判成只读。

---

## 功能

控制台分**用户后台**（日常使用）和**管理后台**（`/admin`，限平台管理员；审计页审计员也能进）。
页面按角色的菜单开关收敛。

| 用户后台 | 管理后台 |
|---|---|
| 总览、终端、导出、上传、后台执行 | 管理总览、实例配置 |
| 发布、规范审查、审批、执行窗口（班车） | 风险规则、环境分层、规范规则库、发布流程 |
| 数据查询、结构对比、巡检、项目 | 权限与用户、执行窗口全局管理 |
| 我的操作记录 | 全量审计、系统设置 |

### 风险判定与环境分层

- 判定链：**菜单权限 → 能力矩阵（角色 × 能力 × 分层）→ 高危命令字典 → 无 WHERE 拦截**。
- **规则挂在分层（tier）上，实例归属环境（environment）**，一个分层可承载多个环境。内置五层：
  `prod`、`gli`（法务）、`staging`、`uat`、`dev`，可自建。新分层从模板层克隆规则。
- 每个分层另有独立开关：无 WHERE 拦截、MFA、敏感字段脱敏、数据查询导出限制、自动执行等。
  新装时所有分层（含 dev）都开启无 WHERE 拦截。
- 能力维度：`select`、`write`、`ddl`、`grant`、`conn`、`approve`、`explain`、`release`，
  各取放行 / 审批 / 拒绝。`release` 管的是能不能发起发布，与语句本身能不能跑分开判。
- 多语句逐条判级、取最严；会真正执行的 `EXPLAIN ANALYZE` 一类按真实执行判。
- SQL 解析器的结论**只能让判定更严**，不能放宽手写判定（ADR 0019）。

### 终端

- xterm.js 真终端，WebSocket 流式执行，不可用时回退 REST。
- **每个标签页独占一条数据库连接**，`SET`、事务、临时表在标签内连续有效。关闭标签、登出或空闲
  超时即断开，未提交事务由数据库回滚。每人最多 20 条、每实例 30 条；实例满额后改走共享连接池，
  并提示本条语句不保留会话状态。
- 支持 psql / mysql 风格元命令、MySQL 客户端命令（`pager`、`tee`、`status` 等，在浏览器内实现），
  以及 Oracle SQL*Plus 习惯（`SET SERVEROUTPUT`、绑定变量、PL/SQL 块逐行输入）。
- 对象树支持搜索库表、查看对象源码、Oracle 无效对象重编译、程序包下载（`.pck` / zip）。
- 结果网格、Tab 补全、快捷脚本（`Alt+1…9`）、会话日志导出。一次最多读 5 万行，更多请用导出。

### 审批

- 多步审批链，超时可选自动拒绝 / 自动升级 / 继续等待；默认两人控制，自审需显式打开。
- **通过不等于执行**（ADR 0010）：够得到该实例、能力矩阵也放行的人都可以执行，不限发起人；
  一张单只能执行一次，执行前重新判定。单据最终状态以数据库返回的结果为准。
- 例外：发布单由流水线推进，敏感导出单批准后由后台任务执行，执行窗口单批准后窗口生效。
- 网关在执行途中重启的单子判失败并写明「结果未知，请到库上核实」，不会重跑。
- 审批可在站内进行，也可在聊天里的审批卡片上直接通过 / 拒绝（Lark / 飞书、Telegram，默认关闭，见 `docs/chat-approval-setup.md`）：
  网关主动连出去，不需要公网入口；审批人先用一次性绑定码绑定聊天账号，规则与站内完全一致。
- 发布单另可由持 `release:approve` 的开放接口凭据代批（须点名链上审批人，规则同控制台）。新单与超时升级会推送飞书群机器人（Webhook）通知。

### 执行窗口（班车）

为维护车次申请一段免审批时间窗，经审批后生效、到点自动关闭（ADR 0015）。

- 只把「需审批」降为「放行」，能力矩阵判拒绝的仍然拒绝，风险等级不变。
- 按库开放，同一实例可勾选多个库；修改窗口会回到待审批。
- 窗口内放行的每条命令，审计里指回是哪个窗口放的；可回看窗口期间跑了什么。

### 发布流水线

- 流程由阶段组成：规范审查、人工审批、备份点、执行 / 自动执行、执行后校验、人工确认、通知。
  内置「标准发布流程」和仅限开发分层的「开发自助发布」。
- 执行阶段默认等人确认；**自动执行**只在分层允许时生效（默认 dev、uat），否则退回等人点确认。
- 下发前按当时的规则再判一次。网关重启时，执行中的发布判失败且不重跑。
- 收单预检会列明每项检查的来源（实时 / 缓存），没做的检查也照样列出（ADR 0017）。

### MySQL 在线表结构变更

- 适用于 **MySQL 与 PolarDB（MySQL 版）**、且分层开了「生产横幅」的实例。表达到阈值
  （默认 100 万行或 1 GB）时，原生会阻塞写入或重建表的 DDL 自动改走：影子表 + 分块拷贝 +
  binlog 重放 + 原子切换（ADR 0011）。
- 原生秒级完成的照常直接执行；在线变更做不了的（外键、分区等）照原路径，并提示可能长时间锁表。
- 先做前置检查，再经审批启动；可暂停、继续、中止，并按从库延迟与线程数限流。
- 网关重启后，来自终端 / 审批且有检查点的任务标为「已中断」，由人决定续跑或放弃；其余判失败。
- 切换后原表保留为 `_表名_del`，由 DBA 择时删除。

### 脚本、后台执行与导出

- **脚本**：上传 `.sql` 后逐语句扫描；检出高危则整份转审批，执行时校验文件哈希并逐条重判。
- **后台执行**：长语句异步跑，默认上限 90 分钟，PostgreSQL 系可看实时日志。
- **导出**：仅限单条只读查询，格式为 CSV 或 SQL（INSERT），约 100 MB 一卷、AES-256 加密 zip。
  默认上限 500 万行 / 2 GB（未压缩）/ 30 分钟，归档保留 3 天。

### 业务数据查询

给业务方的只读查询页，能查哪些库**只由查询授权决定**（ADR 0018），授权由外部审批系统经开放接口下发。

- 单条只读语句、只读事务、限行、限时、限并发，默认脱敏；不能跨库或查系统库。
- 实例可配从库：查询优先走从库，仅在从库**连不上**时回落主库，结果与审计里注明由谁提供。
- 导出受全局开关与授权「允许导出」双重控制（可按分层放开），每次写审计。

### 规范审查

88 条内置规则，来自 `docs/` 下的 MySQL / TiDB / Oracle / DWS 规范，标注分级与出处。级别、参数、
文案可在线改，可自建正则规则。结论只用于发布流水线和自查，不影响终端执行。

### 其他

- **结构对比**：对比两个库的表结构（同一引擎族），实时读系统目录，可导出 Markdown / CSV。
- **巡检报告**：接收 AI 巡检系统推送的报告，按实例归档；限管理员、DBA 负责人、DBA L2 查看。
- **元数据同步与连通性巡检**：默认关闭，因为打开后网关会定期登录你的生产实例。
- **品牌**：系统名称与 LOGO 可在线配置；界面支持明暗主题与中英双语。

---

## 安全模型

### 账号与会话

- 控制台会话放在 HttpOnly + SameSite=Strict Cookie 里，写操作校验 CSRF；脚本等客户端可用 Bearer。
- 会话有效期、空闲锁定、TOTP、生产操作步进验证（宽限期绑定到单个会话）均可配置。
- 登录与管理后台各有独立 IP 白名单；管理后台另有空闲超时。只信任配置里列出的反向代理。
- 内置角色：平台管理员、DBA 负责人、DBA L2、研发只读、审计员、业务查询。一人可挂多个角色，
  权限取并集。
- 聊天审批的绑定是比会话活得更久的凭据：停用账号、改或重置口令、管理员重置或代绑 MFA 会自动解绑；用户 IP 白名单管不到卡片上的点击（ADR 0021）。
- 可接 SRD 授权登录（公司 SSO）：只做认证，按邮箱对应已有账号。**未开通的邮箱会被拒绝**，
  需管理员先建号或邀请。

### 数据保护

- 连接口令与 TOTP 种子用 AES-GCM 静态加密；审计中的口令语法先打码再入库。
- **敏感字段脱敏在服务端完成**（ADR 0009），分层可开关。
- 页面带当前用户水印，用于追溯截图来源。

> **边界**：脱敏按选择列的文本比对，子查询改名这类写法可能绕过终端脱敏。真正的边界是不给那张表的
> 访问权限。导出与数据查询更严：前者转敏感导出审批，后者直接拒绝。

### 审计

- 每行记录时间、操作人与来源 IP、入口、实例与库、命令、风险、结果、授权单号。
- 管理员、DBA 负责人、审计员看得到全员记录，其他人只看自己的。
- 哈希链可用 `./aegisdb verify-audit`、`GET /api/v1/audit/verify` 或审计页按钮校验。只报第一处断点：

| 结论 | 含义 |
|---|---|
| `content` | 这一行内容被改过 |
| `link` | 中间有行被删除或插入 |
| `gen` | 像是抹掉较新字段后重算的 |

> **边界**：哈希是不带密钥的 SHA-256，能发现顺手改动，挡不住拿到库写权限后整条重算。要防后者，
> 请把审计经 Webhook 推到网关改不了的地方再比对。

`GET /healthz` 里的 `auditWriteFailures` **非零就应告警**：动作已发生，但审计行没写成。

---

## 配置

### 选择环境与存储库

| `APP_ENV` / `AEGISDB_ENV` | 默认存储库 | 说明 |
|---|---|---|
| 未设置 / `dev` / `development` / `local` | SQLite | 本地开发，放宽部分限制 |
| `prod` | MySQL | 强制强 JWT 密钥 |
| 其他值（如 `staging`、`uat`） | SQLite | 强制强 JWT 密钥；**请显式设 `AEGISDB_DB_DRIVER`** |

`AEGISDB_DB_DRIVER`（`mysql` / `postgres` / `sqlite`）优先级最高。用 PostgreSQL 做存储库时，把它和
`AEGISDB_POSTGRES_DSN` 写在 `aegisdb.env` 里，不要改 `config.yaml`：升级解包会覆盖那个文件。

### 关键环境变量

| 变量 | 说明 |
|---|---|
| `AEGISDB_JWT_SECRET` | 会话签名密钥，非本地环境必填，≥ 32 位 |
| `AEGISDB_SECRET_KEY` | 静态加密密钥。强烈建议单独设置；**一经使用不可更改**，否则存量口令与 TOTP 种子解不开 |
| `AEGISDB_MYSQL_DSN` / `AEGISDB_POSTGRES_DSN` | 存储库连接串 |
| `AEGISDB_TLS_CERT` / `AEGISDB_TLS_KEY` | 由网关直接终止 TLS |
| `AEGISDB_SSO_DISABLED` | 强制关闭 SSO（应急） |
| `AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED` | 停用管理后台 IP 白名单（应急） |

两个密钥都可用 `openssl rand -hex 32` 生成。其余变量见 `deploy/aegisdb.env.example` 与 `DEPLOY.md`。

超时、导出上限、审批链、MFA、Webhook、飞书、开放接口凭据等在【管理后台 · 系统设置】里改，
保存后生效，无需重启。

---

## 生产部署

```bash
./build.sh                   # 默认 linux/amd64，可用 GOOS / GOARCH / VERSION 覆盖
```

产物是单二进制（API + 前端 + 内嵌迁移）、配置、systemd unit 和部署说明，打成
`aegisdb-<版本>-<os>-<arch>.tar.gz`。打包前会验证生产环境不返回模拟数据，不通过就不出包。

> **macOS 上打包**：归档步骤用了 GNU tar 的 `--mode`，系统自带的 tar 不支持，会在最后一步失败。
> 请在 Linux 上打包，或安装 GNU tar 并让它在 `PATH` 里优先。

### 首次部署

约定目录为 `/opt/aegisdb`。建用户、建库、systemd、Nginx、TLS 的完整步骤见
[`DEPLOY.md`](DEPLOY.md) 与 [`docs/上线部署SOP.md`](docs/上线部署SOP.md)。核心命令：

```bash
cd /opt/aegisdb
set -a; . ./aegisdb.env; set +a        # 手工跑子命令必须自己加载环境变量
./aegisdb init --admin-email you@corp.io --admin-password 'A-Strong-Passw0rd!'
sudo systemctl enable --now aegisdb
```

`init` 会先跑迁移。没加载环境变量时，子命令会回落到配置文件里的本地默认 DSN。

| 子命令 | 作用 |
|---|---|
| （无） | 启动服务 |
| `init` | 迁移 + 引用数据 + 平台管理员 |
| `migrate` | 只升级表结构，幂等 |
| `verify-audit [-limit N]` | 校验审计哈希链，链断了退出码 2 |
| `migrate-store` | 停机把存储库从 MySQL 搬到 PostgreSQL，见 `docs/上线部署SOP-存储库搬迁到PG.md` |
| `version` | 打印版本 |

### 升级

**备份数据库 → 停旧进程 → `migrate` → 起新进程 → `version` 确认。**
迁移之后不要单独回滚二进制；要回滚，数据库须一起回到备份。

### 上线后检查

1. **审批人自检**：看启动日志里的 `WARN 审批人自检`。角色没有真人成员，或链上只有一个人又没开自审批时，
   工单会建不出来或没人能批。给角色加一名真人，或显式打开「允许自审批」。自检只看启动那一刻，不是监控。
2. **审计基线**：跑一次 `verify-audit` 记下行数，之后定期巡检。
3. **健康检查**：监控 `GET /healthz`，`auditWriteFailures` 非零即告警。

---

## API

| 面 | 前缀 | 鉴权 | 调用方 |
|---|---|---|---|
| 控制台 | `/api/v1` | 会话 Cookie + CSRF，或 `Authorization: Bearer <JWT>` | 登录的人 |
| 开放接口 | `/api/v1/open` | `Authorization: Bearer <key>.<secret>`，或 `X-AegisDB-Key` + `X-AegisDB-Secret` | 外部系统 |

业务接口一律返回 HTTP 200，结果在包体 `{ code, msg, data }` 里。常见码：`0` 成功、`40100` 未登录、
`40300` 无权、`42200` 已转审批、`42800` 需二次验证、`42900` 过于频繁，完整码表见契约文件。

开放接口的凭据绑定一个服务账号并按权限范围收窄：`release:create`、`release:read`、
`release:approve`、`review:check`、`grant:read`、`grant:write`、`inspection:write`。提单按
`externalRef` 幂等，走同一条流水线与判定链，不是第二条执行通道。

| 文档 | 用途 |
|---|---|
| `GET /docs` | Swagger UI，仅本地开发环境；Try it out 打的是真接口 |
| `backend/docs/openapi.yaml` | 完整契约（160 条路径、194 个操作） |
| `docs/控制台接口说明.md` | 控制台接口按业务分组说明 |
| `docs/开放接口对接文档.md` | 外部系统对接 |

新增路由没写进契约、契约里有不存在的路由、没标注守卫，都会让测试失败。

---

## 测试

```bash
cd backend  && go vet ./... && go test ./... -timeout 30m
cd frontend && npm run type-check
cd frontend && npm run test:unit
cd frontend && npx playwright install chromium   # 首次
cd frontend && npm run test:e2e
```

| 套件 | 规模 | 说明 |
|---|---|---|
| 后端 | 486 个文件、1866 个测试函数 | 以黑盒回归为主：启动完整应用实跑 HTTP 接口 |
| 前端单测 | 55 个文件、408 个用例 | `src/lib` 纯逻辑、词条一致性 |
| 前端 e2e | 100 个文件、430 个用例 | 真实浏览器，接口打桩，使用独立端口 5174 |

- `-timeout 30m` 不能省：`internal/bootstrap` 整包耗时较长，慢机器会超过 Go 默认的 10 分钟。
- 需要真实数据库的用例默认跳过，设置 `AEGISDB_TEST_MYSQL`、`AEGISDB_TEST_PG`、
  `AEGISDB_TEST_ORACLE`、`AEGISDB_TEST_STORE_PG`、`AEGISDB_OSC_TEST_DSN` 等变量后启用。

### 开发约定

- 改表结构时，MySQL（`backend/migrations/`）与 PostgreSQL（`backend/migrations/pg/`）各写一份、编号相同。
- 新增路由要同步 `openapi.yaml`，并在 `summary` 标出守卫，如 `[perms · admin]`。
- 新增模拟路径要带 `SIMULATED-PATH` 标记并登记到 `docs/simulated-paths.md`。
- 文案改动要同时更新 `frontend/src/locales/zh.json5` 与 `en.json5`。

---

## 文档

| 主题 | 位置 |
|---|---|
| 部署 | [`DEPLOY.md`](DEPLOY.md)、`docs/上线部署SOP.md`、`docs/上线部署SOP-存储库搬迁到PG.md` |
| 架构决策 | `docs/adr/`（0003–0021，共 19 篇） |
| 对接 | `docs/开放接口对接文档.md`、`docs/巡检报告接入说明.md`、`docs/事件中心平台接入文档.md`、`docs/chat-approval-setup.md` |
| 数据库规范 | `docs/MySQL規範.md`、`docs/TIDB規範.md`、`docs/Oracle規範.md`、`docs/DWS对象设计和业务开发规范-ABLE.md` |
| 安全审计 | `docs/白盒安全审计报告.md` |
| 设计稿 | `docs/*.dc.html`、`docs/_ds/` |
| Agent 协作 | [`CLAUDE.md`](CLAUDE.md)、`docs/agents/` |
