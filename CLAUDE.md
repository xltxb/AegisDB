# AegisDB 数据库管理网关 — Agent 指引

全栈项目：`backend/`（Go · Gin · GORM · PostgreSQL）+ `frontend/`（React 19 · TS · Vite · Zustand · TanStack Query）。
本地启动见 `README.md`：先 `createdb vela_gateway`（网关自身的存储是 PostgreSQL，不再有零依赖模式），
再跑 `backend/run-dev.sh`（macOS/Linux）或双击 `backend\run-dev.bat`（Windows）与 `frontend\run-dev.bat`，
演示登录 `linwei@vela.io / vela123`。跑后端测试另需 `createdb vela_test`。

## Agent skills

### Issue tracker

工单与 PRD 以本地 markdown 形式存于 `.scratch/<feature>/`；远端是 GitHub（`origin`，https://github.com/xltxb/AegisDB ），代码审查发现的问题记在 GitHub Issues。
See `docs/agents/issue-tracker.md`.

### Triage labels

沿用默认五态标签词汇：`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`。
See `docs/agents/triage-labels.md`.

### Domain docs

单上下文布局（根 `CONTEXT.md` + `docs/adr/`）。
See `docs/agents/domain.md`.
