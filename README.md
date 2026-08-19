# Vela 数据库管理网关 (DP DB GATEWAY)

面向 DBA 与运维团队的统一数据库访问网关：所有数据库操作经 Web 终端 / 脚本 / 异步任务 /
数据导出四条通道下发，由网关按**三层策略**（菜单权限 → 能力矩阵 → 高危命令字典）逐语句
实时判定 —— 放行、拦截或转审批；审批通过后由网关代执行。全程写入防篡改审计哈希链，
可经 Webhook / 飞书外推。支持真实连接 MySQL、PostgreSQL、Oracle、SQLite 等多引擎实例。

```
db-gateway/
├─ backend/     Go + Gin + GORM 后端（REST + WebSocket；风险引擎/RBAC/审批/审计哈希链/多引擎真实执行）
├─ frontend/    Vue 3 + TS + Vite 前端（深色 Vela 设计系统，中英双语，xterm.js 真终端）
├─ docs/        PRD / 前后端开发文档 / 交互原型 / ADR / 外部审批对接指南
├─ deploy/      systemd unit + 环境变量模板
├─ build.sh     一键打包前后端（默认交叉编译 linux/amd64，产出版本化 tar.gz）
└─ docker-compose.yml  本地 MySQL 8 + Redis 7（可选）
```

## 技术栈

| 层 | 选型 |
|----|------|
| 前端 | Vue 3 (`<script setup>`) · TypeScript · Vite · Pinia · Vue Router · vue-i18n（构建期预编译） · xterm.js · lucide |
| 后端 | Go 1.23 · Gin · GORM · JWT · gorilla/websocket · bcrypt / HMAC / AES-GCM · TOTP |
| 网关目标库 | MySQL / MariaDB / TiDB / PolarDB · PostgreSQL / DWS / GaussDB · Oracle · SQLite（纯 Go 驱动，免 cgo） |
| 自身存储 | MySQL 8（生产）/ SQLite（零依赖本地开发） |

---

## 快速开始

### 一键启动（Windows，最省事）

双击项目根目录的 **`start-dev.bat`** —— 自动在两个窗口分别拉起后端（SQLite，零依赖）与前端（Vite），
然后浏览器打开 http://localhost:5173 ，用 `linwei@vela.io` / `vela123` 登录。
（也可单独双击 `backend\run-sqlite.bat` 或 `frontend\run-dev.bat`。）

### 手动启动

```bash
# 后端（默认 APP_ENV=dev → SQLite，写 ./vela-gateway.db，自动迁移 + 演示数据）
cd backend && go run ./cmd/server            # 监听 :8080，健康检查 GET /healthz

# 前端
cd frontend && npm install && npm run dev    # http://localhost:5173（代理 /api 到 :8080）
```

生产路径（MySQL 8）：`APP_ENV=prod go run ./cmd/server`，DSN 见 `backend/configs/config.yaml`
或环境变量 `VELA_MYSQL_DSN`。环境变量一览与生产部署完整步骤见 **`DEPLOY.md`**。

### 演示账号（本地 seed，密码均为 `vela123`）

| 账号 | 角色 |
|------|------|
| `linwei@vela.io` | 平台管理员（全菜单） |
| `chenhao@vela.io` | DBA L2 |
| `zhangwei@vela.io` | DBA 负责人 |
| `zhaolei@vela.io` | 研发只读 |
| `hujun@vela.io` | 审计员 |

登录不同角色可验证**菜单权限收敛**；一个用户可挂多个角色，权限取**所有角色的并集**。

---

## 功能总览

### 三层风险判定 · 环境/分层模型

- 判定链：`菜单权限 → 能力矩阵(角色×能力×分层) → 高危命令字典(命令×分层)`，外加严格模式
  （拦截无 WHERE 的 DELETE/UPDATE）。EXPLAIN 独立成档（`EXPLAIN` 只读、`EXPLAIN ANALYZE` 按真实执行判）。
- **分层标签（tier）承载规则，环境（environment）归属实例**：内置五个分层 —— `prod` 生产、
  `staging` 预发布、`uat` 演练、`gli` 法务、`dev` 开发；可自建环境并挂到任一分层，新分层自动
  克隆规则。同一 `DROP` 在 PROD 分层拦截转审批、在 DEV 直接放行。
- **多语句整批判定**：批量粘贴 / 堆叠语句（`SELECT 1; DELETE …`）逐条判级取最严格结论，
  预检、同步执行、异步执行三条路径一致；工单聚合展示全部命中规则。
- 按引擎方言判定（MySQL / PostgreSQL / Oracle / MongoDB 语法差异），注释剥离防绕过。

### Web 终端（xterm.js 真终端）

- WebSocket 流式执行（不可用自动回退 REST），psql/mysql 风格元命令（`\dt` `\d` `\l` `\x`
  `\G` `\conns` `\c <db>` 等），实例树按 环境 → 类型 → 实例 组织，支持搜索与点击切换。
- 结果网格 / 垂直显示 / 列宽截断提示，SQL 实测耗时，会话日志（含审计标注）一键导出。
- 快捷脚本：常用 SQL 存为片段，`Alt+1…9` 一键键入 —— 与手工输入走同一判定通道。
- 命中审批规则时弹出提交卡（必填原因），生成审批单 `AP-xxxx` 与审计 ID。

### 审批

- 多步审批链 + 超时策略（自动拒绝 / 自动升级 / 继续等待，后台定时扫描），自审开关，
  列表分页 + 单号直查；通过后由网关**代执行**并回填结果。
- **外部飞书审批（审批魔方）**：高危命令推送飞书交互卡片，回调驱动执行；回调带签名校验 +
  IP 白名单，外发内容全量脱敏。见 `docs/external-approval-setup.md` 与 ADR 0003。

### SQL 脚本

- 上传 `.sql` 管理 / 在线查看，逐语句扫描判级（按可配置的扫描基准分层）；检出高危则整脚本
  转审批 —— 审批单引用已上传文件（校验哈希），通过后逐语句代执行；全安全脚本直接逐条执行并逐条审计。

### 异步执行 · 数据导出

- **异步执行**：30–60min+ 的长语句/存储过程后台跑，流式收集 `RAISE NOTICE` 进度日志，
  执行前过同一判定链；超时可配（默认 90min）。
- **异步导出**：仅允许单条只读查询（写操作/写文件子句一律拒绝），结果流式写出为 ~100MB
  分卷、AES-256 加密 zip（带 UTF-8 BOM，Excel 中文不乱码），按任务发放解压口令；
  行数 / 字节 / 执行超时上限均为运行时设置（系统设置 · 网关），失败任务不遗留孤儿文件。

### 审计

- 每条审计 `hash = SHA256(prev_hash + payload)` 构成防篡改哈希链（唯一约束防分叉），
  记录环境 + 分层**双快照**；命令中的口令语法全引擎脱敏。
- 按风险 / 时间范围（24h / 7 天 / 30 天）服务端过滤，分页，CSV 导出；涉及表列表化展示，完整命令入详情。

### 权限与账号

- 角色 × 菜单开关 × 能力矩阵三态（放行 / 审批 / 拒绝）在线编辑；高危命令字典逐分层指定生效等级。
- 管理员建号（设初始密码即激活）、邀请用户、成员增删与搜索；实例按标签限定可访问角色。

### 会话与安全

- 会话 TTL、空闲自动锁定、TOTP MFA（自助绑定 / 强制开关 / 生产操作步进验证按连接记忆宽限期）、
  登录 IP 白名单（默认关，放行 loopback）；JWT 密钥与连接口令加密密钥解耦，支持 JWT 轮换。
- 连接口令 AES-GCM 静态加密；实例维护态限制下发并审计。

### 系统设置（运行时生效，免重启）

网关策略 / 执行与异步超时 / 导出上限与超时 / 审批链与超时策略 / 会话与 MFA / IP 白名单 /
Webhook（HMAC-SHA256 签名 + 指数退避重试）/ 飞书通知 / 外部审批对接，均落库持久化。

---

## 生产部署

```bash
./build.sh          # 前端构建 + 后端交叉编译 linux/amd64 + 版本化 tar.gz（含部署速查）
```

单二进制同时提供 API 与前端 SPA，内嵌 SQL 迁移；子命令 `version` / `migrate` / `init`。
完整步骤（MySQL 准备、迁移、初始化管理员、systemd 托管、TLS、环境变量一览、升级注意事项）
见 **`DEPLOY.md`** 与打包产物内的 `README-DEPLOY.md`。

## API

统一前缀 `/api/v1`，Bearer Token 鉴权，响应包 `{ code, msg, data }`（`code=0` 成功，
`42200` 命令被拦截需审批）。契约见 `backend/docs/openapi.yaml`（运行时 `GET /openapi.yaml`）。

## 测试与质量

- 后端：`go test ./...` —— 以 **httptest 黑盒回归网**为主（`backend/internal/bootstrap/` 下 80+
  测试文件，启动完整应用实跑 HTTP 接口，覆盖三层判定、多语句、审批链、审计链、脱敏、
  导出上限、会话安全等），另有引擎方言 / 口令脱敏等单元测试。
- 前端：`npm run build` = `vue-tsc` 类型检查 + Vite 打包 + vue-i18n 严格模式构建期预编译
  （非法文案直接构建失败）。

## 文档

- `docs/`：PRD、前后端开发文档、交互原型、Vela 设计系统。
- `docs/adr/`：架构决策记录（外部飞书审批、按引擎判定方言等）。
- `docs/agents/`、`CLAUDE.md`：AI Agent 协作约定（工单在 `.scratch/`，远端为华为云 CodeHub）。

> 说明：未填写连接凭据的实例走**仿真执行**（合成行数/耗时，便于演示）；填入真实凭据后
> 即按引擎家族走 `database/sql` 真实执行（MySQL/PG/Oracle/SQLite 家族见
> `backend/internal/gateway/realdb.go`）。
