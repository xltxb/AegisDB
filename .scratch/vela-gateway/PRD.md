# PRD · AegisDB 数据库管理网关

Status: done（`issues/01..11` 全部落地；增量与缺陷单见 `issues/12..24`，分层模型与飞书审批另见
`.scratch/env-tier-model/`、`.scratch/lark-approval-integration/`）

> 原稿来源：`docs/数据库网关 PRD.dc.html`、`docs/前端开发文档 Vue3.dc.html`、`docs/后端开发文档 Go-Gin.dc.html`、
> `docs/数据库网关-终端布局变体.dc.html`。
>
> **2026-09-11 以代码为准校准**：下文的 Problem Statement 与 User Stories 保留立项时的原始意图（那是需求的历史记录，
> 不该被事后改写）；**Solution / Implementation Decisions / Testing Decisions / Out of Scope 已按实际交付重写**，
> 与原稿不一致之处集中列在「验收校准」与「超出原 PRD 的交付」两节。规范性描述以 `README.md` 与 `docs/adr/` 为准。

## Problem Statement

DBA 与运维团队对生产数据库的操作目前是"直连 + 口头审批 + 事后补记录"：每个人各自拿着库账号直连，高危命令（`DROP` / `TRUNCATE` / 无 `WHERE` 的 `DELETE`）能否执行全凭自觉与运气；审批走 IM 私聊、无单可查；出事后想追"谁、在哪个实例、跑了什么"只能翻零散日志，且日志本身可被改。不同环境（生产/预发/测试）用同一套松散规则，一条 `DROP` 在测试库无伤大雅、在生产却是事故，但系统不区分。结果是：高危操作没有强制闸门、审批没有留痕、审计可被篡改、权限边界靠人记。

## Solution（as-built）

一个统一的**数据库访问网关**：所有数据库操作不再直连，而是经 Web 命令行 / 脚本 / 异步任务 / 数据导出 /
发布流水线 / 开放接口下发，由网关在执行前**逐语句**实时校验。

- **四层风险判定**：每条语句依次过 `菜单权限 → 能力矩阵（角色×能力×分层）→ 高危命令字典（命令×分层）→ 无 WHERE 拦截（分层开关）`，
  取最严档位得出"放行 / 转审批 / 拦截"裁决（Verdict）。原稿写的是"三层 + 全局严格模式开关"，
  第四层现已同样按分层存（迁移 0030、ADR 0013），因为"只对 dev 关掉"是一个真实诉求。
- **分层承载规则，环境归属实例**：内置五个分层 `prod / gli / staging / uat / dev`；环境可自建并挂到任一分层。
  同一 `DROP` 在 PROD 分层拦截转审批、在 DEV 分层直接放行。详见 `.scratch/env-tier-model/PRD.md` 与 ADR 0013/0014。
- **拦截 → 审批 → 由有权者执行**：命中高危弹出提交卡（必填原因），生成审批单（`AP-xxxx`）与审计 ID。
  **审批通过本身不执行任何命令**（ADR 0010）——通过后通知发起人，由**任何本就有权在该实例跑这条命令的人**
  回到审批页点执行（标签授权 / MFA / 能力矩阵全按实际执行者复判），一张单只能执行一次，执行前再判一次规则。
  原稿的"网关代执行"被推翻：命令在审批人点下去那一刻执行，意味着无人在场看着它，且审批人替发起人选择了执行时机。
- **审计哈希链**：每条命令（含被拦截、被拒绝、被模拟）全量留痕，`hash = SHA256(prev_hash + payload)`、
  append-only 防篡改，记录环境 + 分层双快照，可按风险 / 时间筛选并导出 CSV。
- **Webhook 外推**：关键事件（`exec` 高危执行 / `login` 登录会话）以**事件中心格式**推送外部系统，
  鉴权用 `Authorization: Bearer <secret>`，失败按 2ⁿ 秒退避重试。原稿计划的 `X-Vela-Signature`（HMAC-SHA256）
  **未实现**，见「验收校准」。
- 配套**数据库连接管理**、**高危命令规则字典**（按分层三态编辑、即时生效）、**用户与权限管理**、
  **系统设置**、**SQL 脚本上传与扫描**、**中英双语 + 深浅主题**，以及一批立项时未列的能力（见「超出原 PRD 的交付」）。

## User Stories

> 以下 57 条是立项时的原始意图，逐条保留。实际交付与其不同的条目，在下一节「验收校准」里逐条说明。

### 登录与应用壳（菜单权限收敛）
1. 作为平台成员，我想用账号密码登录拿到会话 Token，以便进入网关工作台。
2. 作为登录用户，我想刷新页面后仍保持登录态，以便不被频繁打断。
3. 作为不同角色的用户，我想侧栏只显示我有菜单权限的入口，以便看不到也进不去无权的功能。
4. 作为研发只读角色，我登录后看不到"用户与权限""系统设置"入口，以便权限边界在 UI 层即收敛。
5. 作为任何用户，会话失效（401）时我想被自动登出回登录页，以便不在失效态下误操作。

### 数据库连接管理
6. 作为 DBA，我想按 PROD/STAGING/DEV 分组查看所有被接入网关的实例，以便快速定位目标库。
7. 作为管理员，我想新建连接（实例名/地址端口/引擎/分层环境/网关策略），保存即测试接入，以便纳管新实例。
8. 作为管理员，我想为连接选择引擎（MySQL/PostgreSQL/ClickHouse/Redis/TiDB），以便覆盖多种数据库。
9. 作为管理员，我想把实例在"在线/维护"间切换，以便维护期间限制并审计对它的操作。
10. 作为 DBA，我想对连接做连通性测试，以便确认网关能接入。

### Web 命令行终端（放行通路）
11. 作为 DBA，我想在终端左侧看到按环境分组、标注风险等级的实例/库/表分层树，以便选择目标。
12. 作为 DBA，我想在分层树中搜索定位实例并点击切换目标，使右侧上下文与底部状态栏联动，以便明确"我在哪台、什么角色、什么策略"。
13. 作为 DBA，我想输入安全命令（如 `SELECT`）由网关代执行并回显行数与耗时，以便正常查询。
14. 作为 DBA，我想终端历史区分信息/命令/输出/成功/拦截，以便回看操作流。
15. 作为 DBA，我想右侧上下文面板展示目标实例、库、连接角色、网关策略、受限命令清单与审批链，以便执行前心里有数。

### 高危拦截 → 审批 → 执行
16. 作为 DBA，当我在 PROD 输入高危命令时，我想被实时拦截并在终端内嵌"已拦截"提示，以便不会误伤生产。
17. 作为 DBA，被拦截时我想弹出提交卡展示命令/环境/实例/风险标签并强制填写原因，以便发起规范审批。
18. 作为 DBA，提交后我想拿到审批单号（`AP-xxxx`）与审计 ID，以便跟踪。
19. 作为审批人，我想在审批待办按"待我审批/全部"查看卡片（命令/发起人/目标/原因/审批链），以便决策。
20. 作为审批人，我想一键通过或拒绝并即时看到状态更新，以便高效处理。
21. 作为系统，审批通过后我想由网关代执行原命令并回显结果，以便用户无需再直连。
22. 作为安全负责人，我想同一命令在 DEV 直接放行、PROD 拦截转审批，以便按环境分层管控。
23. 作为安全负责人，我想开启严格模式后无 `WHERE` 的 `DELETE`/`UPDATE` 被拦截，以便防止全表误改。

### 高危命令规则字典
24. 作为管理员，我想维护内置高危命令（DROP/TRUNCATE/DELETE/ALTER/RENAME/GRANT/REVOKE）并新增自定义命令，以便贴合本组风险面。
25. 作为管理员，我想对每个命令按环境配置三态（拦截/审批/放行）并点击徽标循环切换，以便细粒度控制。
26. 作为管理员，我想字典改动后终端实时判定与脚本扫描立刻同步（无需重启会话），以便规则即时生效。
27. 作为管理员，我想新建规则时自定义触发命令 × 适用环境 × 命中动作 × 审批链，以便一次配齐策略。
28. 作为管理员，我想用全局开关启停严格模式，以便按需收紧。

### SQL 脚本上传与预扫描
29. 作为 DBA，我想上传 `.sql` 脚本，系统按 `;` 拆句并剔除行/块注释，以便逐条评估。
30. 作为 DBA，我想脚本被逐条判级（高危/需审批/安全），无 `WHERE` 的 `DELETE`/`UPDATE` 单独标记，以便识别风险点。
31. 作为 DBA，我想看到扫描统计概览（总语句/高危/需审批/安全）与逐条明细，以便整体把握。
32. 作为 DBA，当脚本含高危语句时我想整脚本转审批后才执行、全安全时可直接执行，以便安全交付变更。
33. 作为 DBA，我想脚本扫描规则与终端实时拦截、字典完全一致，以便结果可信。

### 用户与权限管理
34. 作为管理员，我想在角色视图左切角色、右侧实时看到该角色的菜单权限/能力矩阵/成员，以便集中管理。
35. 作为管理员，我想逐功能开关菜单权限，关闭后该角色看不到对应入口，以便控制可见性。
36. 作为管理员，我想点击能力矩阵单元格循环 `allow→approve→deny` 并按角色保存，以便定义角色在各环境对各能力的权限。
37. 作为管理员，我想能力矩阵改动直接影响终端风险判定，以便授权变更立即闭环生效。
38. 作为管理员，我想从组织目录为角色增删成员且即时生效，以便维护团队。
39. 作为管理员，我想在用户视图查看账号/角色/状态，并启用/停用、邀请新用户（预分配角色），以便管理人员生命周期。

### 审计日志
40. 作为审计员，我想检索每条命令的时间/操作人/实例/命令全文/风险等级/结果/审批单号，以便追溯。
41. 作为审计员，我想按风险（全部/高危/中/低）与时间范围（24h/7天/30天）服务端筛选，以便聚焦。
42. 作为审计员，我想被拦截、被拒绝的命令同样可检索，以便审计无盲区。
43. 作为审计员，我想导出含完整命令与审批链的 CSV（与当前筛选一致），以便归档或上报。
44. 作为安全负责人，我想审计为 append-only 哈希链不可篡改，以便留痕可信。

### Webhook 外推
45. 作为管理员，我想配置 Webhook 推送地址/签名密钥/订阅事件（拦截/审批/高危执行/登录）/重试/启停并落库，以便对接 SIEM/IM。
46. 作为接收方系统，我想每次投递带 `X-Vela-Signature`（HMAC-SHA256），以便校验来源真实性。
47. 作为管理员，我想投递失败按指数退避重试，以便容忍下游抖动。
48. 作为管理员，我想发送测试事件验证连通性并查看投递结果，以便配置即验证。

### 系统设置
49. 作为管理员，我想配置网关策略（默认策略/严格模式/执行超时）并对引擎生效，以便统一管控。
50. 作为管理员，我想配置审批超时处理（自动驳回/自动升级/保持等待），由后台定时扫描逾期审批单执行，以便审批不无限挂起。
51. 作为安全负责人，我想配置会话有效期/空闲锁定/强制 MFA/IP 允许列表，以便加固访问。
52. 作为管理员，我想配置通知（Lark 频道/邮件/拦截即时通知），以便事件可达人。
53. 作为任一用户，我想切换界面语言（中文/English）与主题（深/浅），且顶栏与系统设置联动、数据类内容（实例名/SQL/人名/邮箱/IP）保持原文不翻译。

### 终端流式执行与布局变体
54. 作为 DBA，我想命令经 WebSocket 流式下发由网关代执行、输出流式回显，以便长输出体验顺滑。
55. 作为 DBA，当 WebSocket 不可用时我想自动回退 REST 执行，以便功能不中断。
56. 作为 DBA，我想在三种终端布局间切换（A 三栏 IDE / B 终端优先+底部抽屉审批卡 / C 拦截焦点居中模态），以便按场景选择信息密度与打断强度。
57. 作为受 `prefers-reduced-motion` 影响的用户，我想动画收敛为 0，以便无障碍使用。

## 验收校准（原稿 vs 实际交付）

| # | 原稿说法 | 实际交付 | 依据 |
|---|---|---|---|
| 21 | 审批通过后由**网关代执行** | **通过不执行**。通知发起人，由任何本就有权在该实例跑这条命令的人点「执行」；一单一次；执行前复判 | ADR 0010、`service/approval_execute.go` |
| 19/20 | 多步审批链逐级推进 | 链 = `owner` 角色全部真人成员（无则 `admin`），**任一成员一次决定即终结整单**（approve-1），不逐级推进 | `repository.DecideActiveStep` |
| 8 | 引擎含 **ClickHouse / Redis** | 引擎下拉 7 项：MySQL / PolarDB / TiDB / MariaDB / PostgreSQL / GaussDB(DWS) / Oracle。SQLite 只有后端驱动（自身存储与既有连接识别），界面建不出 | `frontend/src/lib/engines.ts` |
| 22/25/27 | 规则按**环境**配置 | 规则按**分层（tier）**配置，环境只决定实例归属；新增命令时逐分层指定等级，「放行」也写一条显式 `off` 行 | ADR 0013、env-tier PRD |
| 23/28 | 严格模式是**全局开关** | 改为**按分层**的 `strict_nowhere`（迁移 0030 起），开关在「分层」页，设置页只留一个跳转链接 | ADR 0013 |
| 27 | 新建规则可配**审批链** | 未实现。新建规则只有 `command + 各分层等级`，审批链是全局的（或由发布流程阶段的 `approverRole` 指定） | `dto.go` UpsertRiskCommandReq |
| 43 | CSV 含**审批链** | CSV 列为 `time,actor,instance,command,risk,result,approval_no,hash`，只带单号不带链 | `service/admin.go` |
| 44 | append-only 哈希链 | 写侧成立（唯一约束防分叉）；**没有读侧的链完整性校验端点**，校验目前只在测试里做 | issue 08 验收项未闭环 |
| 45/46 | Webhook 带 `X-Vela-Signature`（HMAC-SHA256） | **无 HMAC**。只发 `Authorization: Bearer <secret>` + `X-Vela-Event`；HMAC-SHA256 只用于飞书自定义机器人的签名 | `service/webhook.go` |
| 45 | 订阅事件四种（拦截/审批/高危执行/登录） | 后端接受 `exec / login / intercept / approve`，**界面只提供 `exec` 与 `login`**；seed 默认只订阅 `exec` | `handler/admin.go`、`WebhookPanel.vue` |
| 50 | 超时可**自动升级** | `auto-escalate` 只把单标为「已升级」并写一条告警审计，**审批人不变、单仍待审**；真正的升级未实现 | `service/gateway.go` |
| 7 | 保存**即测试接入** | 后端建连接不探测；前端保存后自动发起一次 `POST /connections/:id/test`，效果等价 | `views/ConnectionsView.vue` |
| 33 | 脚本扫描与终端判定**完全一致** | 基本一致，但 `ScanStatement` 缺 `SessionScoped` 短路：`ALTER SESSION SET …` 扫描报高危、执行放行 | 已建 GitHub issue |
| 56 | 终端布局变体 **A / B / C** | **未实现**。交付的是固定三栏 + 可拖拽/可折叠 + 沉浸模式（左右两栏同时收起） | `views/TerminalView.vue` |
| — | `/risk/check` 命中高危返回 `42200` + `ap_no` | `/risk/check` 是**纯预检**，一律返回 `code=0` + Verdict，不建单；`42200` + `ap_no` 出现在 `/terminal/exec` | `service/gateway.go` |
| — | WS Token 经 **query** 鉴权 | 改走 `Sec-WebSocket-Protocol: vela-token, <jwt>` —— query 会把 Token 落进 gin 访问日志与 Referer | `handler/terminal.go` |
| — | 演示种子含多个角色账号 | 空库首次启动只建 **一个**平台管理员 `linwei@vela.io`，且**不建任何实例**；验证菜单收敛需自建角色与用户 | `bootstrap/seed.go` |

## 超出原 PRD 的交付

立项后陆续落地、本 PRD 未曾要求的能力（各自有 ADR 或独立工单，此处仅登记归属）：

| 能力 | 归属 |
|---|---|
| 变更发布流水线（流程模板 / 阶段 / 执行前复判 / 重启恢复） | ADR 0005、`issues/*` 之外的独立开发 |
| 开放接口提单（凭据 / 服务账号 / 幂等 / 预检） | ADR 0006 |
| 数据库规范审查规则库（87 条，按方言，可在线调级） | ADR 0005、0007、0008 |
| 敏感字段脱敏（服务端，终端与导出两处同一层） | ADR 0009 |
| 执行窗口「班车」（申请走审批、到点自动关） | ADR 0015 |
| 环境 / 分层解耦 | `.scratch/env-tier-model/` |
| 外部飞书审批（审批魔方）+ 飞书群通知卡片 | ADR 0003、`.scratch/lark-approval-integration/` |
| 异步执行、异步导出（分卷加密 zip、上限与保留期） | `issues/13,14,18,20` |
| SQL 脚本上传管理、快捷脚本片段、会话日志导出 | `issues/06,19` |
| 项目归属、标签授权（角色级 + 用户级） | 独立开发 |
| 元数据缓存与定时同步 | `issues/24` |
| 对象树 / 表 DDL / Oracle 单对象编译与无效对象重编译 | `issues/15,16,21,22,23` |
| MFA/TOTP 真实校验、登录频控与锁定、IP 白名单 | 原稿列为 Out of Scope，现已交付 |
| 站内通知（8 类）、总览页、`/gateway/stats` | 独立开发 |
| 真实多引擎执行（`database/sql`，仿真降级为 dev-only） | 原稿列为 Out of Scope，现已交付；ADR 见 `docs/simulated-paths.md` |

## Implementation Decisions（as-built）

> 不含具体代码片段；记录模块、接口、契约与架构决策。

### 架构与模块
- 后端分层：`bootstrap`（依赖装配 + 迁移 + seed + 后台 worker）→ `middleware`（JWTAuth / MenuGuard / APIClient / IP 白名单 / 访问日志 / Recovery / CORS）→
  `handler` → `service` → `repository`（GORM）→ `model`；`gateway`（方言 / 风险引擎 / 真实执行器 / 脱敏）；`review`（规范审查规则）；
  `dto`；`metrics`；`pkg`（jwt / crypto / totp / resp / logger / sqlutil）。
  配置经环境变量 + yaml，`VELA_DB_DRIVER` / `VELA_MYSQL_DSN` / `VELA_JWT_SECRET` / `VELA_SECRET_KEY` 等可覆盖；
  DB 驱动在 MySQL 8 与纯 Go SQLite 间切换。
- 前端 Vue3 `<script setup>` + Vite + Pinia + Vue Router + vue-i18n（构建期预编译，严格模式）+ lucide；
  路由懒加载；样式用 Vela 设计系统语义 token，深浅主题经 `<html data-theme>` 切换。

### 风险引擎（核心契约）
- 四层判定顺序递进，取最严档位，输出 `Verdict(Action, Risk, Command, Rule)`：
  ① 菜单权限（无则拒绝）② 能力矩阵 `角色×能力×分层 → allow|approve|deny`（多角色取并集中最宽的档，
  再与其它层取最严）③ 高危命令字典 `命令×分层 → high|mid|off` ④ 无 WHERE 拦截（该分层的 `strict_nowhere` 开关）。
- 能力维度共 **8** 个：`select / write / ddl / grant / conn / approve / explain / release`。
  `explain` 与 `select` 取更严的那个（拆权限不能发出原本被拒绝的东西）；`release` 管的是能否**发起发布**。
- `Action` 三分支：`Allow` → 执行 + 审计(executed)；`Approve` → 建审批单 + 通知 + 审计(pending)；`Deny` → 拒绝 + 审计(rejected)。
- **多语句整批判定**：批量粘贴逐条判级取最严结论，预检 / 同步执行 / 异步执行三条路径一致。
- **会话级语句短路**：`SET search_path`、Oracle `ALTER SESSION SET` 只查 `select` 能力，跳过字典与无 WHERE 检查；
  `SET GLOBAL / PERSIST / PASSWORD / ROLE`、`ALTER SYSTEM` 不放宽。
- **EXPLAIN 分档**：纯计划的 `EXPLAIN` 按只读判；`EXPLAIN ANALYZE`（含 `ANALYSE`、`(ANALYZE …)`、DWS `EXPLAIN PERFORMANCE`）按真实执行判。
- **执行窗口**放宽只发生在判定函数内部，且只把 `approve` 降为 `allow`，`deny` 仍 `deny`。
- 字典 / 矩阵 / 设置变更**热生效**，无需重启会话。

### API 契约
- 三套鉴权面互不混用：控制台 `/api/v1`（Bearer JWT）、开放接口 `/api/v1/open`（`Bearer <key>.<secret>` 或 `X-Vela-Key`/`X-Vela-Secret`）、
  审批回调 `/api/v1/approvals/lark/callback`（共享密钥 + 可选 IP 白名单）。
- 响应信封 `{ code, msg, data }`，**一律 HTTP 200**。常用码：`0` 成功 / `40001` 参数错误 / `40100` 未登录 /
  `40300` 无权 / `40301` IP 不在白名单 / `42200` 命令拦截需审批（带 `ap_no`）/ `42800` 需二次验证 / `42900` 登录频控 / `50000` 内部错误。
  无鉴权的只有 `GET /healthz`、`/openapi.yaml`、`/docs`。
- **全部 136 个操作（107 条路径）都在 `backend/docs/openapi.yaml`**，由 `TestOpenAPICoversEveryRoute` 与
  `TestOpenAPIDocumentsNoGhostRoutes` 双向钉住；每个操作的 summary 带守卫标注如 `[perms · admin]`、`[scope release:create]`。
- 终端 WS：`WS /terminal/ws`，Token 走 `Sec-WebSocket-Protocol: vela-token, <jwt>`；支持 `exec` / `cancel` 帧，
  心跳 20s，断线退避 1→15s 重连；不可用时前端自动回退 REST（REST 路径不可取消）。

### Schema（GORM 模型，36 张表）
- 身份与权限：`tbl_user`（多角色，`tbl_role_member` 关联）、`tbl_role`、`tbl_role_menu`（10 个菜单键）、
  `tbl_role_capability(role_id, capability, tier_code, level)`、`tbl_role_tag` / `tbl_user_tag`（标签授权）。
- 分层与实例：`tbl_env_tier`（属性位：`require_mfa / danger_banner / counts_in_pending / scan_baseline / strict_nowhere / conn_layer / default_role`）、
  `tbl_environment`、`tbl_connection`、`tbl_project` / `tbl_database_project`。
- 判定与审批：`tbl_risk_command(command, tier_code, level)`、`tbl_approval`（含 `exec_status`、`external_task_id`、
  环境+分层双快照）、`tbl_approval_step`、`tbl_exec_window`。
- 执行与产物：`tbl_audit_log`（`prev_hash` / `hash` 链）、`tbl_async_job`、`tbl_export_job`、`tbl_script_upload`、
  `tbl_terminal_snippet`、`tbl_sensitive_column`。
- 发布与集成：`tbl_pipeline` / `tbl_pipeline_stage`、`tbl_release` / `tbl_release_stage`、`tbl_sql_review_rule`、
  `tbl_api_client`、`tbl_webhook_config` / `tbl_webhook_delivery`、`tbl_notification`、`tbl_setting`。
- 元数据缓存：`tbl_meta_table` / `tbl_meta_column` / `tbl_meta_sync` / `tbl_schema_object`。
- Schema 的唯一权威是 `backend/migrations/*.sql`（内嵌进二进制）；MySQL 上 `auto_migrate` 被忽略并告警。

### 前端状态与交互
- Pinia store 只有四个：`auth`（user / menus / capabilities / token）、`envtier`（分层与环境）、`snippets`（快捷脚本，存服务端）、
  `ui`（主题 / 语言 / toast）。**页面数据由各视图自行 fetch，没有按领域拆 store** —— 原稿设想的
  connections/roles/riskRules/terminal/approvals/audit/settings 七个 store 并不存在。
- 三层权限在前端的体现：路由守卫 + 按 `menus` 渲染侧栏（部分路由复用别的菜单键，如导出/上传/后台执行/规范审查复用 `terminal`）；
  能力驱动按钮禁用；提交前由服务端判定，前端不自算可执行性。
- axios 拦截器：注入 `Authorization: Bearer`、统一解包 `{code,msg,data}`、`code≠0` toast、`401` 自动登出。
- 本地持久化仅限偏好项：`vela_token / vela_lang / vela_theme / vela_tree_w / vela_insp_w / vela_insp_collapsed /
  vela_termgrid / vela_termgrid_h / vela_conn_dense`。
- 执行说明：填了凭据的实例按引擎家族走 `database/sql` 真实执行；**未填凭据的实例只在非生产环境**做仿真返回
  （审计记 `simulated` 而非 executed），生产一律拒绝并说明（`docs/simulated-paths.md`、打包前有测试闸）。

### Webhook 与审批超时
- Webhook 投递：`Authorization: Bearer <secret>` + `X-Vela-Event`，载荷为事件中心格式（`source_system=AegisDB`、
  `occurred_at`、`action`（按命令首动词映射）、`resource`、`operator`、`summary`、`raw_payload`）；
  单次 5s 超时、禁重定向、拨号时校验目标 IP（默认拦内网，`VELA_WEBHOOK_ALLOW_PRIVATE` 放行）；
  失败 `backoff(i)=2^i` 秒退避（封顶 30s）重试至 `retry_max`（默认 5），整条投递 2 分钟总时限；
  每次投递写 `tbl_webhook_delivery`。
- 审批超时：后台每 60s 扫描，按 `approval.onTimeout`（`keep-waiting` / `auto-reject` / `auto-escalate`，
  阈值默认 720 分钟）处理；`auto-reject` 会置 expired、审计、通知发起人并向审批魔方回写 cancel。

## Testing Decisions（as-built）

**好的测试只断言外部可观测行为，不耦合实现细节。** 主缝与原稿一致，前端缝的技术选型换了。

- **主缝 · 后端 HTTP API 边界**：经 `bootstrap` 装配完整 app（SQLite 文件库 + seed），用 `httptest` 驱动
  `/api/v1/*` 与 `/terminal/ws`，**不 mock service/repository 内部**。目前 **148 个测试文件**，覆盖判定链、
  多语句、审批链、审计链、脱敏、导出上限、执行窗口、会话安全、开放接口、发布流水线等。
  整包约 12 分钟，超过 `go test` 默认的 10 分钟包超时，所以命令必须带 `-timeout`：`go test ./... -timeout 20m`。
- **前端单元 · Playwright runner**（`npm run test:unit`，**210 个用例 / 23 个文件**）：跑 `src/lib/*` 的纯逻辑
  （行编辑器、结果渲染的控制字符、会话日志脱敏、导入表解析、规则文案等），以及对 `.vue` 与词条文件的结构性检查
  （autofocus、modal 根节点、中英词条对齐、引擎表、内置名）。用 `tsconfig.unit.json` 把 `@/locales` 指向测试替身
  —— 裸 Node runner 读不了 `.json5` 也没有 `localStorage`，而那次失败炸的是收集阶段：整套一个用例都不执行。
  **原稿计划的 Vitest + zod 未采用**，store 契约测试也不存在（因为按领域拆的 store 本身不存在）。
- **前端 e2e · Playwright**（`npm run test:e2e`，5 个用例）：分层树、导出实例选择器、元数据同步、权限矩阵、
  减少动效；自动起 Vite :5174，接口用 mock，不依赖 Go 后端。原稿设想的"登录→拦截→审批→执行→审计"整链 e2e
  由后端 httptest 覆盖，未在浏览器侧重复搭建。
- **前置回归门**：后端 `go build ./...` / `go vet ./...`；前端 `npm run build`（= `vue-tsc` + Vite + i18n 严格模式预编译）。
- **打包闸**：`TestProductionServesNoSimulatedData` / `TestSimulationDefaultsToOff` / `TestSimulatedPathsAreDocumented`
  在编译前跑，跑不过不出包（见 `DEPLOY.md` 第 1 节）。

## Out of Scope（原稿的边界与其后续去向）

| 原稿列为 Out of Scope | 现状 |
|---|---|
| 真实目标库的实际 SQL 执行 | **已交付**。按引擎家族挂 `database/sql` 驱动，仿真降级为 dev-only 且审计不记 executed |
| Lark 审批卡 / 邮件 / SIEM 真实联调 | 飞书**已交付两条**（群通知卡片 + 审批魔方回调，见 ADR 0003）；邮件通知仍是设置页的一个开关，后端不读 |
| 生产部署形态（多副本、Redis、主从、配置中心） | 仍不在范围内。服务保持无状态 + 策略热生效；`docker-compose.yml` 里的 Redis 未被后端使用 |
| 真实 MFA 第二因子 | **已交付**。TOTP 自助绑定 / 管理员代绑 / 步进验证 / 宽限期 / 强制绑定开关 |
| 高级 RBAC（动态能力维度、跨角色继承） | 仍不在范围内。能力集合固定为 8 维三态；多角色按并集，不做继承 |

仍然明确不做：终端布局变体 A/B/C（固定三栏已够用）、ClickHouse / Redis 等非关系型引擎、规则级审批链。

## Further Notes

- **拆单**：本 PRD 按纵切（tracer bullet）拆成 11 片，见 `issues/01..11`，全部已落地；关键路径
  `01→02→03→04→{06,07,09,10,11}`，05/08 在 03 后并行。`issues/12..24` 是上线后的缺陷与增量单。

  | # | 工单 | Blocked by |
  |---|------|------------|
  | 01 | 行走骨架：登录 + 菜单驱动应用壳 | — |
  | 02 | 数据库连接管理 | 01 |
  | 03 | 终端放行通路 + 风险引擎 + 审计哈希链 | 02 |
  | 04 | 高危拦截 → 审批 → 执行闭环 | 03 |
  | 05 | 高危命令规则字典（分层三态 + 无 WHERE 拦截） | 03 |
  | 06 | SQL 脚本上传扫描 + 整脚本审批/执行 | 04, 05 |
  | 07 | 用户与权限管理 | 04 |
  | 08 | 审计日志查询（筛选 + 时间范围 + CSV 导出） | 03 |
  | 09 | Webhook 外推 | 04 |
  | 10 | 系统设置（网关/审批超时/安全/通知/外观） | 04 |
  | 11 | 终端 WebSocket 流式执行 + REST 回退（布局变体未做） | 03, 04 |

- **演示账号**：`linwei@vela.io / vela123`（平台管理员，全菜单）。空库 seed 只建这一个账号、不建实例。
- **设计系统**：UI 沿用 Vela 设计 token（深色 futuristic/neon、浅色 young & energetic），中英混排、数字为视觉主角；图标用 lucide。
- **文档分工**：面向使用者的完整能力说明在 `README.md`；部署与运维在 `DEPLOY.md`；每条架构决策的取舍在 `docs/adr/`。
  本 PRD 只承担"当初要解决什么问题、最后交付成了什么样"。
