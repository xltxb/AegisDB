# AegisDB 数据库管理网关 — Agent 指引

全栈项目：`backend/`（Go · Gin · GORM · MySQL/PostgreSQL）+ `frontend/`（React 19 · TS · Vite · Zustand）。
本地启动见 `README.md`「本地开发」：需要 PostgreSQL（Windows 双击根目录 `start-dev.bat`；macOS/Linux 装 PG 后 `go run ./cmd/server migrate` 再启动），演示登录 `linwei@aegisdb.io / aegisdb123`。SQLite 存储库已移除；后端测试自动起临时 PG（需 PG 程序，见 README「测试」）。

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
