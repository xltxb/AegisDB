@echo off
REM 零依赖本地启动 Vela Gateway 后端（dev 环境 = 纯 Go SQLite，无需 MySQL/Docker）
REM 双击运行，或在 backend 目录执行：run-sqlite.bat
setlocal
set APP_ENV=dev

set GO=go
where go >nul 2>nul || set GO="C:\Program Files\Go\bin\go.exe"

echo [Vela Gateway] env=dev (sqlite)  ->  http://localhost:8080
echo 演示登录: linwei@vela.io / vela123
%GO% run ./cmd/server
endlocal
