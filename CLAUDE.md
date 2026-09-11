# AegisDB 数据库管理网关 — Agent 指引

全栈项目：`backend/`（Go · Gin · GORM · MySQL/SQLite）+ `frontend/`（React 19 · TS · Vite · Zustand · TanStack Query）。
`frontend-vue/` 是被它取代的 Vue 3 实现，留作对照与回退，不要往那边加新功能。
本地零依赖启动见 `README.md`（分别双击 `backend\run-sqlite.bat` 与 `frontend\run-dev.bat`，演示登录 `linwei@vela.io / vela123`）。

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
