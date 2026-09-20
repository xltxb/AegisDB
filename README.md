# AegisDB 数据库管理网关

面向 DBA 与运维团队的统一数据库访问网关：所有数据库操作经 Web 终端 / 脚本 / 异步任务 /
数据导出 / 发布流水线 / 开放接口下发，由网关**逐语句实时判定** —— 放行、拦截或转审批；
审批通过后由**有权者自己**执行（见 ADR 0010）。全程写入防篡改审计哈希链，可经 Webhook /
飞书外推。支持真实连接 MySQL 家族（MySQL / PolarDB / TiDB / MariaDB）、PostgreSQL 家族
（PostgreSQL / GaussDB(DWS)）与 Oracle。

```
db-gateway/
├─ backend/     Go + Gin + GORM 后端（REST + WebSocket；风险引擎/RBAC/审批/审计哈希链/多引擎真实执行）
├─ frontend/    React 19 + TS + Vite 前端（Vela 设计系统 + HUD 视觉层，明暗双主题，中英双语，xterm.js 真终端）
├─ docs/        功能总览 / PRD / 开发文档 / 交互原型 / ADR / 四份数据库规范 / 外部对接指南
├─ deploy/      systemd unit + 环境变量模板
└─ docker-compose.yml  本地 PostgreSQL 16 + Redis 7（可选，本机已装 PostgreSQL 就不需要；后端目前不使用 Redis）
```

---

## 快速开始

### 前置：一个本机 PostgreSQL

网关自身的元数据存储是 PostgreSQL —— 开发和生产同一种，**没有零依赖模式了**（为什么见
ADR 0018）。开跑之前先把库建出来：

```bash
# macOS：brew install postgresql@16 && brew services start postgresql@16
# Debian/Ubuntu：apt install postgresql-16
createdb vela_gateway      # 服务用
createdb vela_test         # 跑后端测试用（测试各自建独占 schema，互不干扰）
```

默认 DSN 是 `host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable`（见
`backend/configs/config.yaml`）—— 不写 `user=`，libpq 就回落到当前 OS 用户，Homebrew
装出来的 PostgreSQL 正是这个形态。别的用户名/口令用 `VELA_PG_DSN` 覆盖。
本机不想装的话，`docker compose up -d` 起仓库根的那份（用户 `vela` / 口令 `velapass`，
此时要显式给 `VELA_PG_DSN`）。

### 起服务

```bash
cd backend && go run ./cmd/server            # 监听 :8080，健康检查 GET /healthz
cd frontend && npm install && npm run dev    # http://localhost:5173（代理 /api 到 :8080）
```

也可以用脚本：macOS / Linux 跑 `backend/run-dev.sh`，Windows 双击 `backend\run-dev.bat`
与 `frontend\run-dev.bat`。

然后浏览器打开 http://localhost:5173 ，用 `linwei@vela.io` / `vela123` 登录。

**空库首次启动只建一个平台管理员**（全菜单），不创建任何实例 —— 登录后到「配置」页新增。
dev 构建的登录页会预填这组账号，生产构建不预填也不提示。未填凭据的实例在 dev 环境走
仿真执行（见文末）。

`APP_ENV` 只决定演示数据、JWT 强度校验与 CORS/SSRF 的松紧，**不再挑存储引擎**。
生产路径：`APP_ENV=prod ./vela-gateway`，DSN 由环境变量 `VELA_PG_DSN` 给
（`configs/config.prod.yaml` 里故意留空，凭据不进仓库）。

---

## 技术栈

| 层 | 选型 |
|----|------|
| 前端 | React 19（函数组件 + Hooks） · TypeScript · Vite 6 · Zustand（客户端状态） · TanStack Query（服务端状态） · React Router 7 data router · react-i18next · xterm.js · lucide |
| 后端 | Go 1.23 · Gin · GORM · JWT · gorilla/websocket · bcrypt / HMAC / AES-GCM · TOTP |
| 网关目标库 | MySQL / MariaDB / TiDB / PolarDB · PostgreSQL / DWS / GaussDB · Oracle（新建实例的引擎下拉即这 7 项；SQLite 只有后端驱动，界面不提供） |
| 自身存储 | PostgreSQL 16（开发与生产同一种；见 ADR 0018） |

---

## 它能做什么

控制台分**运维前台**与**管理后台**两个门户，去重后共 19 页：

| 门户 | 页面 |
|---|---|
| 运维前台 | 总览 · 终端 · 申请 · 待办 · 变更 · 脚本 · 导出 · 调度 · 班车 · 资产 |
| 管理后台 | 总览 · 配置 · 规则 · 审查 · 治理 · 待办 · 权限 · 流程 · 用户 · 审计 · 设置 |

核心机制：

- **逐语句判定** —— `菜单权限 → 能力矩阵(角色×能力×分层) → 高危命令字典(命令×分层) → 无 WHERE 拦截(分层)`
- **规则挂在分层上，环境只决定实例归属** —— 第二个生产集群照样按生产判，把 prod 环境改挂到 dev 分层之后就不按生产判
- **审批通过 ≠ 执行**（ADR 0010）—— 放行之后仍由有权的人自己跑，网关不代执行
- **审计哈希链** —— 防篡改，可在界面上「校验链」
- **规范审查** —— 87 条规则，覆盖 MySQL / TiDB / Oracle / DWS 四份规范

**逐项说明见 [`docs/features.md`](docs/features.md)** —— 每个页面、每条判定、每个开关做什么。

---

## 测试

```bash
cd backend  && go test ./... -timeout 20m    # 需要本机 PostgreSQL 与 vela_test 库
cd frontend && npm run type-check && npm run test:unit && npm run test:e2e && npm run build
```

**后端**以 httptest 黑盒回归网为主：`backend/internal/bootstrap/` 下 183 个测试文件，每个用例
启动一次完整应用、实跑 HTTP 接口，覆盖判定链、多语句、审批链、审计链、脱敏、导出上限、
执行窗口、会话安全。整包约 12 分钟，**超过 `go test` 默认的 10 分钟包超时** —— 所以上面
那条命令带了 `-timeout`，不带会在跑完前被判超时失败。连不上 PostgreSQL 是**失败**不是 skip，
静默跳过等于假绿。

**前端有两个测试口，跑的是两种东西，不要互相替代：**

- `npm run test:unit`（291 例，`playwright.unit.config.ts`）—— **Node 里跑纯逻辑**，没有浏览器
  也没有 dev server。结果渲染里的控制字符、行编辑器的忙/排队状态机、导入表的校验、
  窄屏该收哪些列，都是这一口盯的。
- `npm run test:e2e`（116 例 / 10 个 spec，`playwright.config.ts`）—— **真浏览器里跑真应用**，自动拉起 Vite，
  API 由各 spec 自己打桩，所以不需要起 Go 后端。这一口盯的是排版和层叠算完之后才成立的事：
  能力矩阵塌成一列、媒体查询没能收掉动画、两个数据源里坏了一个就把实例树整棵清空、
  表格的 grid 轨道数和实际渲染的单元格数对不上 —— 类型检查、构建和单测都看不见这些。
  其中 `hud-visual.spec.ts` 一个文件就占 76 例，盯的是视觉层：装饰有没有真的画出来、
  hover 时行内文字有没有被推动、主按钮的对比度有没有退化、滚动容器里的装饰会不会
  跟着内容滚走。**断言要问"它看得见吗"，不是"这个节点在吗"** —— 后者在这个项目里
  放过同一个 bug 两次：四角取景框的节点存在、绝对定位、不吃点击，四条断言全绿，
  而它被三栏的不透明底色整个盖住，一个像素都没显示。

### 前端约定

- 不写 `forwardRef`（React 19 里 ref 是普通 prop）
- `useEffect` 必须返回清理函数 —— StrictMode 下会双调用，xterm / WebSocket / 定时器的泄漏在那里暴露
- **服务端状态一律归 TanStack Query**，只有真正属于客户端的才进 Zustand。把列表塞进客户端
  store 会得到两份真相，而它们分叉时的表现是「刷新一下就变了」
- 自适应只有两个断点：`768` / `1080`（`src/lib/breakpoints.ts` 与 CSS 各一份，**改一处要改两处**）；
  表格在窄屏按列优先级收列，而不是把八列压进 768px
- **样式分两层，按关注点不按机制**：`theme.css`（3001 行）是既有骨架，就地改；
  `hud.css`（325 行）是 HUD 视觉层，不论用伪元素还是 `background-image` 都归它。
  这样整轮视觉改造可以作为一个单位回滚。`hud.css` 由 `main.tsx` 在 `theme.css`
  **之后** import —— CSS 的 `@import` 必须位于所有规则之前，塞进去会被后面三千行压过
- **滚动容器不能用绝对定位的伪元素承载装饰** —— 它会跟着内容滚出可视区。这个坑在本项目
  踩过四次（`.c-table`、`.rail`、`.ib-side`、`.tv-insp`），前两个各犯一次才被发现。
  会滚的容器改用元素自身的 `background-image`（`background-attachment` 默认 `scroll`，
  锚在边框盒上）。`hud-visual.spec.ts` 里有一条普查断言守着这一**类**错误，不是守某一个容器
- **三处 `.on::before` 是选中态的 3px 左色条**（`.perm-rcard` / `.chg-item` / `.pl-item`），
  装饰不得占用它们的 `::before`，也不得盖住它们 —— 那是这三个容器上唯一回答
  「现在选的是哪一个」的东西。列表行档因此整档走 `::after`

---

## API

三套鉴权面，别混用：

| 面 | 前缀 | 鉴权 | 谁在调 |
|---|---|---|---|
| 控制台 | `/api/v1` | `Authorization: Bearer <JWT>` | 登录的人 |
| 开放接口 | `/api/v1/open` | `Authorization: Bearer <key>.<secret>`（或 `X-Vela-Key` / `X-Vela-Secret`） | 外部系统 |
| 回调 | `/api/v1/approvals/lark/callback` | 共享密钥 + 可选 IP 白名单 | 审批服务 |

一律 HTTP 200，业务结果在包体里：`{ code, msg, data }`（`0` 成功、`42200` 命令被拦截转审批、
`42800` 需二次验证、`42900` 登录频控、`40300` 无权、`40301` IP 不在白名单……完整码表见契约开头）。
无鉴权的只有 `GET /healthz`、`/openapi.yaml`、`/docs`。

- **在线文档**：`GET /docs`（本地 http://localhost:8080/docs ）—— Swagger UI，资源
  **vendored 进二进制**，不走 CDN：断网机器上照常打开，那正是网关最常见的部署环境。
- **完整契约**：`backend/docs/openapi.yaml`。**全部 138 个操作都在里面**（109 条路径），
  由 `TestOpenAPICoversEveryRoute` 静态守着 —— 新增路由没写进文档，测试直接失败。
- **外部系统对接**：`docs/开放接口对接文档.md`。

---

## 生产部署

```bash
# 1. 打包前的闸：生产环境不得含模拟数据
(cd backend && go test ./internal/bootstrap/ -run 'TestProductionServesNoSimulatedData|TestSimulationDefaultsToOff|TestSimulatedPathsAreDocumented' -count=1 -timeout 10m)
# 2. 前端
(cd frontend && npm ci && npm run build)                 # 产物 frontend/dist/，放到后端的 web_dir
# 3. 后端（交叉编译 linux/amd64，纯 Go 无需 C 工具链）
(cd backend && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" -o ../dist/vela-gateway ./cmd/server)
```

单二进制同时提供 API 与前端 SPA，内嵌 SQL 迁移；子命令 `version` / `migrate` / `init`。

**升级顺序**：备份数据库 → 停旧进程 → `./vela-gateway migrate` → 起新进程 → `version` 确认。
注意 `serve` 启动时**自己也会跑一遍迁移**，所以"先起新进程"会直接把库改掉；多副本共享一个库时，
第一台换上新二进制的副本就会改库。迁移之后也不要单独回滚二进制（旧代码按旧列名查询，列已经不在了）。

**上线后看一眼审批人自检日志** —— 每次启动会检查"这些审批环节到底还有没有人能批"，
命中打 WARN（不阻止启动）。最容易漏的一条是：链上恰好一个人且未开自审批，于是工单
建得出来、每层校验都"通过"，但**由他本人发起的永远没人能批** —— 单子静静堆在待办里，
不报错也不推进。

完整步骤（PostgreSQL 准备、迁移、初始化管理员、systemd 托管、TLS、反向代理信任列表、
环境变量一览、运维观测、升级取舍）见 **[`DEPLOY.md`](DEPLOY.md)**。

---

## 文档

| 去处 | 内容 |
|---|---|
| [`docs/features.md`](docs/features.md) | **功能总览** —— 每个页面、每条判定、每个开关做什么 |
| [`DEPLOY.md`](DEPLOY.md) | 生产部署完整步骤与环境变量一览 |
| `docs/adr/` | 架构决策记录（17 条） |
| `docs/superpowers/specs/` | 各轮改造的设计文档（7 份）—— 每份都写明**非目标**和被推翻的判断，不只写做了什么 |
| `docs/superpowers/plans/` | 对应的实现计划（6 份），任务拆解到可独立审查的粒度 |
| `docs/` | PRD、前后端开发文档、交互原型、Vela 设计系统、四份数据库规范（MySQL / TiDB / Oracle / DWS）—— 规范审查的 87 条规则就是从它们来的 |
| `docs/开放接口对接文档.md` | 外部系统怎么拿凭据、幂等怎么算、状态怎么轮询 |
| `docs/agents/`、`CLAUDE.md` | AI Agent 协作约定 |

较关键的几条 ADR：0003 外部飞书审批 · 0004 按引擎判定方言 · 0005 发布流水线与规范审查 ·
0006 开放接口提单 · 0009 敏感字段脱敏在服务端做 · **0010 审批通过不执行** · 0013 无 WHERE
拦截按分层 · 0014 脚本扫描按目标分层判 · 0015 执行窗口 · 0017 JWT 存 localStorage（记在账上的
取舍，附偿还条件） · **0018 自身存储收敛到 PostgreSQL 单一来源** · 0019 审计链哈希的 payload 版本。

---

> **关于仿真执行**：未填写连接凭据的实例**只在非生产环境**（`APP_ENV` 非 prod）走仿真执行
> （合成行数/耗时，便于演示，审计记 `simulated` 而非 executed）；生产环境一律返回
> 「该实例未配置真实执行凭据」，不返回模拟数据（`docs/simulated-paths.md`）。填入真实凭据后
> 即按引擎家族走 `database/sql` 真实执行（见 `backend/internal/gateway/realdb.go`）。
