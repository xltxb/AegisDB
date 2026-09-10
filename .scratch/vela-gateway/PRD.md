# PRD · AegisDB 数据库管理网关

Status: ready-for-agent

> 来源：`docs/数据库网关 PRD.dc.html`、`docs/前端开发文档 Vue3.dc.html`、`docs/后端开发文档 Go-Gin.dc.html`、`docs/数据库网关-终端布局变体.dc.html`。
> 实现拆单见本目录 `issues/01..11`（纵切 tracer bullet）。

## Problem Statement

DBA 与运维团队对生产数据库的操作目前是"直连 + 口头审批 + 事后补记录"：每个人各自拿着库账号直连，高危命令（`DROP` / `TRUNCATE` / 无 `WHERE` 的 `DELETE`）能否执行全凭自觉与运气；审批走 IM 私聊、无单可查；出事后想追"谁、在哪个实例、跑了什么"只能翻零散日志，且日志本身可被改。不同环境（生产/预发/测试）用同一套松散规则，一条 `DROP` 在测试库无伤大雅、在生产却是事故，但系统不区分。结果是：高危操作没有强制闸门、审批没有留痕、审计可被篡改、权限边界靠人记。

## Solution

一个统一的**数据库访问网关**：所有数据库操作不再直连，而是经 Web 命令行下发，由网关在执行前实时校验。

- **三层风险判定**：每条命令依次过 `菜单权限 → 能力矩阵（角色×能力×环境）→ 高危命令字典（环境×命令）`，任一层拒绝即终止；取最严档位得出"放行 / 转审批 / 拦截"裁决（Verdict）。叠加**严格模式**拦截无 `WHERE` 的 `DELETE`/`UPDATE`。
- **环境分层差异**：同一 `DROP`，在 DEV（`audit-only`）直接放行、在 PROD（`strict`）拦截转审批。
- **拦截 → 审批 → 代执行**：命中高危弹出提交卡（必填原因），生成审批单（`AP-xxxx`）与审计 ID，审批通过后由**网关代执行**（用户全程不直连目标库）。
- **审计哈希链**：每条命令（含被拦截、被拒绝）全量留痕，`hash = SHA256(prev_hash + payload)`、append-only 防篡改，可按风险/时间筛选并导出 CSV。
- **Webhook 外推**：关键事件以 HMAC-SHA256 签名推送外部系统，失败指数退避重试。
- 配套**数据库连接管理**、**高危命令规则字典**（环境三态可视化编辑、即时生效）、**用户与权限管理**（菜单权限 + 能力矩阵 + 成员/用户）、**系统设置**（网关策略/审批超时/会话安全/通知/外观）、**SQL 脚本上传预扫描**、**中英双语 + 深浅主题**。

## User Stories

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

### 高危拦截 → 审批 → 代执行
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

## Implementation Decisions

> 不含具体文件路径/代码片段；仅记录模块、接口、契约与架构决策。

### 架构与模块
- 后端沿用既有分层：`bootstrap`（依赖装配 + 路由）→ `middleware`（JWTAuth / MenuGuard(key) / Audit / Recovery / CORS）→ `handler`（参数绑定/调 service/返回）→ `service`（risk/approval/terminal/audit/webhook 业务）→ `repository`（GORM 仓储）→ `model`；`gateway`（引擎驱动 + 代执行器 executor）；`dto`（请求/响应）；`pkg`（jwt / crypto-HMAC / resp / logger）。配置经 viper，支持 `VELA_DB_DRIVER` / `VELA_MYSQL_DSN` / `VELA_JWT_SECRET` 覆盖；DB 驱动可在 MySQL 8 与纯 Go SQLite（零依赖本地）间切换。
- 前端沿用 Vue3 `<script setup>` + Vite + Pinia + Vue Router + vue-i18n + lucide；按路由懒加载分包；样式仅用 Vela 设计系统语义 token（`--surface-card` / `--text-body` / `--accent`），深浅主题经 `<html data-theme>` 切换。

### 风险引擎（核心契约）
- 三层判定顺序递进，取最严档位，输出 `Verdict(Action, Risk, Rule)`：① 菜单权限（无则拒绝）② 能力矩阵 `角色×能力×环境 → allow|approve|deny` ③ 高危命令字典 `环境×命令 → high|mid|off`；严格模式将无 `WHERE` 的 `DELETE`/`UPDATE` 升级为 high。
- `Action` 三分支：`Allow` → 网关代执行 + 审计(executed)；`Approve` → 生成审批单 + 通知 + 审计(pending)；`Deny` → 拒绝 + 审计(rejected)。
- 字典/矩阵变更**热生效**（经缓存失效），无需重启会话。

### API 契约
- 统一前缀 `/api/v1`，Bearer Token 鉴权，响应信封 `{ code, msg, data }`（`code=0` 成功）。错误码：`40001` 参数错误 / `40100` 未登录或 Token 失效 / `40300` 无菜单或能力权限 / `42200` 命令拦截需审批（返回 `ap_no`）/ `50000` 内部错误。
- 关键端点：`POST /auth/login`、`GET /auth/me`（user + menus + capabilities）；`POST /risk/check`；`POST /scripts/scan`、`/scripts/execute`；`GET/POST /connections`、`PATCH /connections/:id`、`POST /connections/:id/test`；`GET /roles[/:id]`、`PUT /roles/:id/menus`、`PUT /roles/:id/capabilities`、`POST|DELETE /roles/:id/members[/:userId]`；`GET/POST /users`、`/users/invite`、`PATCH /users/:id`；`GET/POST/PATCH/DELETE /risk-commands[/:name]`；`GET /approvals?scope=mine|all`、`POST /approvals/:id/approve|reject`；`GET /audit`、`GET /audit/export`；`GET/PUT /settings`、`GET/PUT /settings/webhook`、`POST /settings/webhook/test`；`WS /terminal/ws`（Token 经 query 鉴权）。
- 终端 WS 消息协议（原型确认）：
  ```
  → { type:'exec', connectionId, sql }
  ← { type:'output', rows, ms }
  ← { type:'intercept', rule, approvalNo, auditId }
  ← { type:'error', message }
  ```

### Schema（GORM 模型）
- `tbl_role(code, name, layer, default_conn_role, ...)`、`tbl_user(name, email, role_id, status, mfa_enabled)`（status: active/disabled/invited）。
- `tbl_role_menu(role_id, menu_key, enabled)`、`tbl_role_capability(role_id, capability, env, level)`（level: allow/approve/deny）。
- `tbl_connection(name, engine, host, port, env, policy, default_role, status)`（policy: strict/approve-1/audit-only；status: online/maint）。
- `tbl_risk_command(command, env, level)`（level: high/mid/off）。
- `tbl_approval(ap_no, connection_id, env, instance, command, initiator_id, reason, risk_level, status, audit_id)`（status: pending/approved/rejected/expired）+ `tbl_approval_step(approval_id, step_order, approver_id, status)`。
- `tbl_audit_log(occurred_at, actor_id, connection_id, instance, command, risk, result, approval_no, prev_hash, hash)`（result: executed/pending/rejected/warn；`hash = SHA256(prev_hash + payload)`，append-only）。
- `tbl_webhook_config(endpoint, secret, events, retry_max, enabled)`、`tbl_setting(k, v)`（JSON KV）。

### 前端状态与交互
- Pinia store：`auth`（user/menus/capabilities/token）、`connections`、`roles`（含矩阵编辑 cycleCell/toggleMenu/addMember）、`riskRules`（commands env→level / strictMode）、`terminal`（entries/input/connectionId/run）、`approvals`（mine/all/approve/reject/submit）、`audit`、`settings`、`i18n`。
- 三层权限在前端的体现：路由守卫 + 按 `menus` 动态渲染侧栏（菜单层）；能力指令禁用/隐藏操作（能力层）；提交前 `POST /risk/check` 决定放行/审批/拦截（命令层）。
- axios 拦截器：注入 `Authorization: Bearer`、统一解包 `{code,msg,data}`、`code≠0` toast、`401` 自动登出。
- 代执行说明：开发环境目标实例不可达，executor 做仿真返回（行数/耗时）；接入真实库时按引擎挂载 `database/sql` 驱动，对外契约不变。

### Webhook 与审批超时
- Webhook 投递头 `X-Vela-Signature = HMAC-SHA256(secret, body)`，失败按 `backoff(i)=2^i` 秒重试至 `retry_max`。
- 审批超时由后台定时任务扫描 `tbl_approval_step`，按设置 `auto-reject / auto-escalate / keep-waiting` 处理逾期单。

## Testing Decisions

**好的测试只断言外部可观测行为，不耦合实现细节**（不断言私有函数、内部调用次数、DB 行的内部布局）。优先复用最高、最少的缝——本仓库当前零测试，故新建缝，且把数量压到最小。

- **主缝 · 后端 HTTP API 边界**：经 `bootstrap` 装配完整 app（SQLite 内存库 + seed），用 `httptest` 驱动 `/api/v1/*` 与 `/terminal/ws`，**不 mock service/repository 内部**。在此缝上断言绝大多数 FR：
  - 三层风险判定与环境分层（DEV 放行 / PROD 拦截返回 `42200` + `ap_no`）、严格模式（无 `WHERE` 的 `DELETE`/`UPDATE` 被拦）。
  - 拦截→审批→代执行闭环（审批通过后命令被执行、状态流转 pending→approved）。
  - 脚本扫描的拆句/剔注释/逐条判级与整脚本转审批。
  - 审计哈希链：连续写入后校验 `hash = SHA256(prev_hash+payload)` 链完整、append-only。
  - RBAC 菜单收敛：无 `MenuGuard` 权限角色访问受保护端点得 `40300`/`403`；`GET /auth/me` 的 menus 随角色变化。
  - 字典热生效：改 `/risk-commands` 后同一命令的 `/risk/check` 裁决随之变化。
  - Webhook 签名：断言投递头含正确 HMAC-SHA256 签名、失败重试次数受 `retry_max` 约束（可用桩接收端）。
- **前端缝 · Pinia store + api 契约**（引入 Vitest + zod）：store 经 axios api 层打到被 stub 的 HTTP 契约（zod 校验 DTO 与后端一致），断言外部行为——菜单驱动侧栏渲染、能力矩阵三态循环、审批乐观更新、风险预检后是否打开 InterceptModal、脚本扫描报告渲染。不测组件内部实现细节。
- **一条端到端 · Playwright**：仅覆盖 tracer-bullet 关键路径——登录 → 终端选 PROD 实例 → 输入 `DROP` 被拦截 → 提交原因 → 审批通过 → 代执行回显 → 审计可检索到该条。
- **前置回归门**：后端 `go build ./...` / `go vet ./...`；前端 `vue-tsc --noEmit` + `vite build`。
- **Prior art**：本仓库暂无测试样板；以"装配真实 app + httptest 黑盒驱动"作为后端测试范式起点，后续工单沿用同一缝。

## Out of Scope

- 真实目标库的实际 SQL 执行：开发与验收阶段 executor 对目标库做仿真返回；真实驱动挂载（按引擎 `database/sql`）不在本轮交付内，但需保证对外契约不变以便后续无缝替换。
- 真实第三方集成的端到端联调：Lark 审批卡推送、邮件网关、外部 SIEM 的真实投递只做契约/签名层面验证，不接真实租户。
- 生产部署形态：多副本无状态部署、Redis 会话/缓存、MySQL 主从、配置中心等运维拓扑不在功能交付内（架构上保持无状态、策略热生效即可）。
- 强一致的 MFA 第二因子真实校验（TOTP/短信通道）：本轮做开关与会话/IP 策略，不接真实第二因子通道。
- 高级 RBAC（动态自定义能力维度、跨角色继承）：本轮固定能力集合（select/write/ddl/grant/...）与三态模型。

## Further Notes

- **拆单**：本 PRD 已按纵切（tracer bullet）拆成 11 片，见 `issues/01..11`；关键路径 `01→02→03→04→{06,07,09,10,11}`，05/08 在 03 后可并行。每片贯穿 schema→API→UI→test 且单独可演示。
- **依赖表**：

  | # | 工单 | Blocked by |
  |---|------|------------|
  | 01 | 行走骨架：登录 + 菜单驱动应用壳 | — |
  | 02 | 数据库连接管理 | 01 |
  | 03 | 终端放行通路 + 风险引擎 + 审计哈希链 | 02 |
  | 04 | 高危拦截 → 审批 → 代执行闭环 | 03 |
  | 05 | 高危命令规则字典（环境三态 + 严格模式） | 03 |
  | 06 | SQL 脚本上传扫描 + 整脚本审批/执行 | 04, 05 |
  | 07 | 用户与权限管理 | 04 |
  | 08 | 审计日志查询（筛选 + 时间范围 + CSV 导出） | 03 |
  | 09 | Webhook 外推 | 04 |
  | 10 | 系统设置（网关/审批超时/安全/通知/外观） | 04 |
  | 11 | 终端 WebSocket 流式执行 + REST 回退 + 布局变体 A/B/C | 03, 04 |

- **演示账号**：`linwei@vela.io / vela123`（平台管理员，全菜单）；其它种子账号同密码可验证菜单权限收敛。
- **设计系统**：UI 沿用 Vela 设计 token（深色 futuristic/neon、浅色 young & energetic），中英混排、数字为视觉主角；图标用 lucide。
