# 02 · 数据库连接管理

Status: ready-for-human（已交付；引擎为 7 项关系型，无 ClickHouse/Redis，见 PRD「验收校准」）

## What to build

DBA 能在 `/connections` 管理被网关接入的数据库实例：按环境（PROD/STAGING/DEV）分组查看，新建连接，切换实例在线/维护状态，测试连接。连接数据是后续终端左侧分层树与风险判定的数据源。

- 后端：模型 `tbl_connection(id, name, engine, host, port, env, policy, default_role, status, created_at)`，engine ∈ mysql/postgres/clickhouse/redis/tidb，policy ∈ strict/approve-1/audit-only，status ∈ online/maint。端点 `GET /connections`（按 env 分组）、`POST /connections`（保存即测试接入）、`PATCH /connections/:id`（状态切换）、`POST /connections/:id/test`（连通性测试，目标库不可达时仿真返回）。受 `MenuGuard("db")` 保护。
- 前端：`/connections` 页 + `connections` store（list / byEnv / create / toggleStatus / test）。环境分组列表展示实例名、分层、引擎、地址、默认角色、网关策略、状态；新建连接表单（实例名/地址端口/引擎/分层环境/网关策略）；在线↔维护切换；测试连接结果提示。

## Acceptance criteria

- [ ] 连接列表按 PROD/STAGING/DEV 分组，字段完整（FR-CONN-01）
- [ ] 新建连接保存后出现在对应环境分组，并执行"测试连接并接入"（FR-CONN-02）
- [ ] 至少支持 MySQL/PostgreSQL/ClickHouse/Redis/TiDB 选择（FR-CONN-03）
- [ ] 实例可在"在线/维护"间切换，维护态有视觉标识（FR-CONN-04）
- [ ] 无 `db` 菜单权限的角色无法访问该路由与接口（403）

## Blocked by

- 01（行走骨架：模型/中间件/壳/store 约定）
