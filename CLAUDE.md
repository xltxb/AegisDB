# AegisDB 数据库管理网关 — Agent 指引

全栈项目：`backend/`（Go · Gin · GORM · MySQL/SQLite）+ `frontend/`（Vue 3 · TS · Vite · Pinia）。
本地零依赖启动见 `README.md`（双击根目录 `start-dev.bat`，演示登录 `linwei@aegisdb.io / aegisdb123`）。

## Agent skills

### Issue tracker

修复问题一律先开 GitHub issue（`xltxb/AegisDB`），再开分支修，最后提 PR 合并，合并后直接删除本地和远端分支；不直接往 `main` 提交。
功能的 PRD 与拆分工单仍以本地 markdown 形式存于 `.scratch/<feature>/`（不入库）。
See `docs/agents/issue-tracker.md`.

### Triage labels

沿用默认五态标签词汇：`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`。
See `docs/agents/triage-labels.md`.

### Domain docs

单上下文布局（根 `CONTEXT.md` + `docs/adr/`）。
See `docs/agents/domain.md`.
