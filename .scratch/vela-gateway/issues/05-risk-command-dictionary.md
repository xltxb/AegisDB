# 05 · 高危命令规则字典（环境三态 + 严格模式 + 即时生效）

Status: ready-for-human（已交付；严格模式由全局开关改为按分层，见 ADR 0013）

## What to build

管理员在 `/risk-rules` 维护高危命令字典：按环境（PROD/STAGING/DEV）对每个命令配置三态（拦截/审批/放行），点击徽标循环切换；可新增自定义触发命令并指定适用环境与命中动作；全局严格模式开关。字典调整后，03/04 的终端实时判定与 06 的脚本扫描结果**即时同步**变化（同一份字典驱动）。

- 后端：`tbl_risk_command(command, env, level)` 的完整 CRUD——`GET/POST/PATCH/DELETE /risk-commands[/:name]`，按环境三态。内置 DROP/TRUNCATE/DELETE/ALTER/RENAME/GRANT/REVOKE，支持自定义命令。字典变更后引擎判定无需重启会话即生效（如经缓存失效热生效）。严格模式开关持久化（与 10 系统设置共用 setting，或独立字段）。
- 前端：`/risk-rules` 页 + `riskRules` store（commands env→level、strictMode、cycleLevel、addCommand、removeCommand）。按环境 Tab 切换；命令徽标点击循环"拦截→审批→放行"；新建规则（勾选已有命令 + 输入任意命令、适用环境、命中动作、审批链）；严格模式开关。

## Acceptance criteria

- [ ] 字典含内置高危命令，可新增/删除自定义命令（FR-RULE-01）
- [ ] 每命令按环境三态可点击循环切换并持久化（FR-RULE-02）
- [ ] 在 PROD 把某命令由"拦截"改为"放行"后，终端对该命令的实时判定同步变化（FR-RULE-03，无需重启会话）
- [ ] 新建规则支持自定义触发命令 × 环境 × 动作 × 审批链（FR-RULE-04）
- [ ] 严格模式开关生效（开启拦截无 WHERE 的 DELETE/UPDATE，FR-RULE-05）

## Blocked by

- 03（共用风险引擎与字典数据模型）
