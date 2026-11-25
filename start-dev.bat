@echo off
REM 一键启动全栈开发环境（后端 PostgreSQL 存储库 + 前端 Vite），各开一个窗口。
REM 后端用 backend\run-pg.bat（Docker 容器 aegisdb-store-pg）；想零依赖跑 SQLite 改成 run-sqlite.bat。
REM 双击本文件即可。后端 http://localhost:8080 · 前端 http://localhost:5173
start "AegisDB Backend"  cmd /k "cd /d %~dp0backend && run-pg.bat"
start "AegisDB Frontend" cmd /k "cd /d %~dp0frontend && run-dev.bat"
echo 已在两个新窗口启动后端与前端。浏览器打开 http://localhost:5173 （linwei@aegisdb.io / aegisdb123）
