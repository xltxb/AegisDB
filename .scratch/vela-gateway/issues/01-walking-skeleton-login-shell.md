# 01 · 行走骨架：登录 + 菜单驱动应用壳

Status: ready-for-human（已交付，回归测试覆盖，待人工验收）

## What to build

最薄的端到端通路，一次性验证前后端所有集成层，并完成后续所有片所需的 prefactor（"先让改动变容易，再做容易的改动"）。

用户能用演示账号登录，进入应用壳，左侧侧栏**仅渲染该角色有菜单权限的入口**；切换到权限更少的角色登录，看到的入口更少（菜单权限收敛的最薄验证）。

- 后端：配置加载（viper，支持 `VELA_DB_DRIVER` 等环境变量覆盖）、DB 驱动可切换（mysql / 纯 Go sqlite 零依赖）、GORM 模型 `tbl_user` / `tbl_role` / `tbl_role_menu` + migration + seed（至少平台管理员 + 一个低权限角色）、JWT 签发与校验、`POST /auth/login`、`GET /auth/me`（返回 user + menus + capabilities）、统一响应信封 `{code,msg,data}`（code=0 成功）、错误码表（40001/40100/40300/42200/50000）、中间件 `JWTAuth` + `MenuGuard(key)`、`/healthz`。
- 前端：Vite + Vue3 `<script setup>` + Pinia + Vue Router 工程；axios 拦截器（注入 `Authorization: Bearer`、解包信封、code≠0 toast、401 自动登出）；路由守卫 + 按 `menus` 动态渲染侧栏；登录页；应用壳（顶栏 + 侧栏 + 内容区）；Vela 设计系统语义 token 接入（深/浅色 `<html data-theme>`）；vue-i18n 脚手架（zh/en 资源骨架，先不要求全量文案）。

## Acceptance criteria

- [ ] `linwei@vela.io / vela123` 可登录，刷新后登录态保持（token 持久化）
- [ ] `GET /auth/me` 返回的 menus 驱动侧栏；无权限角色登录后看不到对应入口
- [ ] 后端以 sqlite 驱动零依赖启动（`VELA_DB_DRIVER=sqlite`），首次启动自动 migrate + seed
- [ ] 所有响应为 `{code,msg,data}`；401 触发前端自动登出回登录页
- [ ] 深/浅主题可切换且仅使用 Vela 语义 token；i18n 可切换 locale（壳级文案）
- [ ] 后端 `go build ./...` / `go vet ./...` 通过；前端 `vue-tsc --noEmit` + `vite build` 通过

## Blocked by

None - can start immediately
