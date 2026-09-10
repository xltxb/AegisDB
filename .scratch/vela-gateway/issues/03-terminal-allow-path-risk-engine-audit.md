# 03 · 终端放行通路 + 风险引擎 + 审计哈希链

Status: ready-for-human（已交付；判定已由三层扩为四层并改按分层，见 ADR 0013）

## What to build

核心 tracer bullet。DBA 在 `/terminal` 选中实例、输入一条安全命令（如 `SELECT`），命令经三层风险引擎判定为**放行**，由网关代执行并在终端回显行数/耗时，同时写入一条不可篡改的审计记录。本片只打通放行路径，拦截/审批在 04 片实现。

- 后端：风险引擎实现三层判定的完整骨架——① 菜单权限 ② 能力矩阵（角色×能力×环境，`tbl_role_capability`，level ∈ allow/approve/deny）③ 高危命令字典（`tbl_risk_command`，env×command，level ∈ high/mid/off，先 seed 内置命令）——取最严档位返回 `Verdict(Action, Risk, Rule)`；本片只需走通 `ActionAllow`。`POST /risk/check` 返回 `{requiresApproval, level, reason}`。executor 对目标库仿真返回（行数/耗时）。`tbl_audit_log(... prev_hash, hash)`，`hash = SHA256(prev_hash + payload)`，append-only，异步落库不阻塞执行。
- 前端：`/terminal` 三栏布局（左：由 02 连接生成的环境分组分层树，支持搜索定位与点击切换目标实例；中：命令行输入 + 历史回显，区分 信息/命令/输出/成功；右：RiskInspector 基础上下文面板——目标实例/库/连接角色/网关策略）。底部状态栏（连接状态/角色/策略/编码/审计开关）。`terminal` store（entries/input/connectionId/run）。

## Acceptance criteria

- [ ] 左侧树按环境分组、标实例风险等级、支持搜索，点击切换目标实例并联动右侧/状态栏（FR-TERM-01/04/05）
- [ ] 在 DEV 实例执行 `SELECT` 返回行数与耗时并回显（FR-TERM-02）
- [ ] `POST /risk/check` 对安全命令返回放行；判定按 菜单→能力矩阵→字典 三层取最严（FR-RBAC-02/03）
- [ ] 每次执行写入一条审计，`hash = SHA256(prev_hash+payload)` 链式校验通过、append-only
- [ ] 风险判定路径不依赖直连目标库（网关代执行 + 仿真返回）

## Blocked by

- 02（终端左侧树依赖连接数据）
