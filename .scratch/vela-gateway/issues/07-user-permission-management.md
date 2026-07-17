# 07 · 用户与权限管理（菜单 + 能力矩阵 + 成员 + 用户）

Status: ready-for-agent

## What to build

管理员在 `/permissions` 管理角色与用户：左侧切换角色，右侧实时展示并编辑该角色的菜单权限（逐功能开关）、能力矩阵（能力×环境单元格循环 allow→approve→deny）、成员列表（从组织目录增删）；用户视图支持列表查看、启用/停用、邀请新用户（预分配角色）。改动形成回环——改菜单即影响 01 侧栏可见性，改能力即影响 03/04 风险判定。

- 后端：`GET /roles`、`GET /roles/:id`（含矩阵+菜单+成员）、`PUT /roles/:id/menus`、`PUT /roles/:id/capabilities`、`POST /roles/:id/members` + `DELETE /roles/:id/members/:userId`；`GET /users`、`POST /users/invite`、`PATCH /users/:id`（启用/停用/改角色）。写 `tbl_role_menu` / `tbl_role_capability` / `tbl_user`。
- 前端：`/permissions` 页 + `roles` store（select/cycleCell/toggleMenu/addMember）。MenuAccess 逐菜单开关；CapabilityMatrix 单元格三态循环 + 保存；成员管理增删搜索；用户视图列表 + 启停 + 邀请弹窗（预分配角色）。

## Acceptance criteria

- [ ] 角色视图左切角色、右侧实时展示菜单/矩阵/成员（FR-USER-01）
- [ ] 菜单权限逐功能开关；关闭后该角色重新登录看不到对应入口（FR-USER-02，与 01 收敛一致）
- [ ] 能力矩阵单元格循环 allow→approve→deny 并按角色保存；保存后影响终端风险判定（FR-USER-03）
- [ ] 成员可从组织目录增删，变更即时生效（FR-USER-04）
- [ ] 用户列表支持启用/停用与邀请新用户（预分配角色，status=invited，FR-USER-05）

## Blocked by

- 04（能力矩阵改动需通过风险引擎闭环验证收敛）
