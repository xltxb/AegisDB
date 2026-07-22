@echo off
REM 一键启动全栈开发环境（后端 SQLite + 前端 Vite），各开一个窗口。
REM 双击本文件即可。后端 http://localhost:8080 · 前端 http://localhost:5173
start "Vela Backend"  cmd /k "cd /d %~dp0backend && run-sqlite.bat"
start "Vela Frontend" cmd /k "cd /d %~dp0frontend && run-dev.bat"
echo 已在两个新窗口启动后端与前端。浏览器打开 http://localhost:5173 （linwei@vela.io / vela123）
