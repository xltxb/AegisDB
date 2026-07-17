# Vela 数据库管理网关 — Agent 指引

全栈项目：`backend/`（Go · Gin · GORM · MySQL/SQLite）+ `frontend/`（Vue 3 · TS · Vite · Pinia）。
本地零依赖启动见 `README.md`（双击根目录 `start-dev.bat`，演示登录 `linwei@vela.io / vela123`）。

## Agent skills

### Issue tracker

工单与 PRD 以本地 markdown 形式存于 `.scratch/<feature>/`（本仓库无 GitHub/GitLab 远端）。
See `docs/agents/issue-tracker.md`.

### Triage labels

沿用默认五态标签词汇：`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`。
See `docs/agents/triage-labels.md`.

### Domain docs

单上下文布局（根 `CONTEXT.md` + `docs/adr/`）。
See `docs/agents/domain.md`.
