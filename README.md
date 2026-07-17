# Vela 数据库管理网关 (Vela Gateway)

面向 DBA 与运维团队的统一数据库访问网关：所有数据库操作经 Web 命令行下发，由网关按
**三层策略**（菜单权限 → 能力矩阵 → 高危命令字典）实时校验、拦截高危命令、触发审批，
并全量审计、可经 Webhook 外推。按《PRD》《前端开发文档 (Vue3)》《后端开发文档 (Go·Gin·MySQL)》
与交互原型实现。

```
db-gateway/
├─ backend/     Go + Gin + GORM 后端（REST + WebSocket，风险引擎/RBAC/审批/审计哈希链/Webhook）
├─ frontend/    Vue 3 + TS + Vite 前端（深色 Vela 设计系统，7 大模块 + 弹窗 + 中英双语）
├─ docs/        PRD / 前后端开发文档 / 交互原型 / Vela 设计系统
└─ docker-compose.yml  本地 MySQL 8 + Redis 7
```

## 技术栈

| 层 | 选型 |
|----|------|
| 前端 | Vue 3 (`<script setup>`) · TypeScript · Vite · Pinia · Vue Router · vue-i18n · lucide |
| 后端 | Go 1.23 · Gin · GORM · JWT · gorilla/websocket · bcrypt/HMAC |
| 存储 | MySQL 8（文档默认）/ SQLite（零依赖本地验证，纯 Go 驱动）· Redis（可选） |

---

## 快速开始

### 0. 一键启动（Windows，最省事）

双击项目根目录的 **`start-dev.bat`** —— 自动在两个窗口分别拉起后端（SQLite，零依赖）与前端（Vite），
然后浏览器打开 http://localhost:5173 ，用 `linwei@vela.io` / `vela123` 登录。
（也可单独双击 `backend\run-sqlite.bat` 或 `frontend\run-dev.bat`。）

### 1. 后端

```bash
cd backend

# 数据库由「部署环境」决定：dev→SQLite（零依赖）、prod→MySQL。
# 用 APP_ENV 在启动时切换，默认 dev。

# 方式 A —— 零依赖本地验证（默认，无需 MySQL/Docker，纯 Go SQLite）
go run ./cmd/server                   # = APP_ENV=dev，写 ./vela-gateway.db

# 方式 B —— 生产路径 MySQL 8
#   先起 MySQL（项目根目录已提供 docker-compose）：
#     docker compose up -d            # 启动 MySQL 8 + Redis 7
#   migrations/0001_init.sql 会在 MySQL 首次启动时自动建表；
#   GORM 也会 AutoMigrate，首次启动自动写入演示数据（seed）。
APP_ENV=prod go run ./cmd/server      # 切到 MySQL（DSN 见 config.yaml）
```

后端监听 `:8080`。健康检查：`GET /healthz`。

**配置**：`backend/configs/config.yaml`（`env`、数据库 DSN、JWT、网关策略、Webhook）。
数据库驱动由 `env`（dev→sqlite / prod→mysql）决定，可用环境变量覆盖：
`APP_ENV`（dev|prod）> `VELA_DB_DRIVER`（强制 mysql|sqlite）；另有 `VELA_MYSQL_DSN` / `VELA_JWT_SECRET`。

### 2. 前端

```bash
cd frontend
npm install
npm run dev          # http://localhost:5173 （Vite 代理 /api 与 /healthz 到 :8080）
```

生产构建：`npm run build`（先 `vue-tsc` 类型检查，再 Vite 打包到 `dist/`）。

### 3. 登录

| 演示账号 | 密码 | 角色 |
|----------|------|------|
| `linwei@vela.io` | `vela123` | 平台管理员（全菜单） |

其它种子账号（同密码 `vela123`）：`chenhao@vela.io`(DBA L2)、`zhangwei@vela.io`(DBA 负责人)、
`zhaolei@vela.io`(研发只读)、`hujun@vela.io`(审计员) —— 登录不同角色可验证**菜单权限收敛**
（无权限的角色看不到对应入口）。

---

## 核心能力与验收点

- **三层风险判定**：`菜单权限 → 能力矩阵(角色×能力×环境) → 高危命令字典(环境×命令)` + 严格模式（无 WHERE 的 DELETE/UPDATE）。
- **环境分层差异**：同一 `DROP` 在 PROD 拦截转审批、在 DEV 直接放行。
- **拦截 → 审批 → 代执行**：命中高危弹出提交卡（必填原因），生成审批单（`AP-xxxx`）、审计 ID，
  审批通过后由网关代执行；全程写入审计。
- **SQL 脚本扫描**：上传 `.sql`，拆分语句/剔注释/逐条判级，检出高危则整脚本转审批。
- **审计哈希链**：每条审计 `hash = SHA256(prev_hash + payload)`，append-only 防篡改；支持按风险筛选、CSV 导出。
- **Webhook 外推**：HMAC-SHA256 签名 + 指数退避重试，可发送测试事件。
- **中英双语 + 深/浅主题**：顶栏与系统设置联动，沿用 Vela 设计 token。
- **终端 WebSocket 流式执行**：`/terminal/ws`（Token 经 query 鉴权），不可用时自动回退 REST。
- **数据驱动分层树**：左侧树由连接数据生成，按环境分组、标风险等级，支持搜索定位与点击切换目标实例（右侧上下文/状态栏联动）。
- **审批超时处理**：后台定时扫描，按设置 `auto-reject / auto-escalate / keep-waiting` 处理逾期审批单（FR-APPR-05）。
- **审计时间范围筛选**：按 24h / 7天 / 30天 服务端过滤（FR-AUD-02），CSV 导出同步。
- **Webhook 可配置持久化**：推送地址、事件订阅、启停均可编辑并落库（FR-AUD-03）。
- **维护态限制**：实例切换为维护态后，对其下发命令被限制并审计（FR-CONN-04）。
- **完整交互**：新建/编辑角色（含后端 `PATCH /roles/:id`）、能力矩阵三态、菜单开关、成员增删与搜索、
  邀请用户、新建高危规则（触发命令 × 环境 × 动作直接写入字典）、连接在线/维护切换、系统设置往返持久化。
- **OpenAPI 契约**：`backend/docs/openapi.yaml`（后端文档 §11），运行时 `GET /openapi.yaml` 可取。

## API

统一前缀 `/api/v1`，Bearer Token 鉴权，响应包 `{ code, msg, data }`（`code=0` 成功，
`42200` 表示命令被拦截需审批，返回 `ap_no`）。完整端点见
`docs/后端开发文档 Go-Gin.dc.html` 第 06 章及各 handler。

## 验证状态

- 后端 `go build ./...` / `go vet ./...` 通过；以 SQLite 实跑校验了登录、三层风险判定
  （PROD 拦截 / DEV 放行 / 严格模式）、执行、脚本扫描、审批、审计哈希链、连接/字典/角色等接口。
- 前端 `vue-tsc --noEmit` 类型检查 + `vite build` 通过（1691 模块）。
- 前后端联调：Vite 代理 → 后端，登录与鉴权数据流贯通。

> 说明：目标实例（prod-order-cluster 等）在开发环境不可达，网关的「代执行」对目标库做
> 仿真返回（行数/耗时），与原型一致；接入真实库时在 `internal/gateway/executor.go` 按引擎
> 挂载 `database/sql` 驱动即可。
