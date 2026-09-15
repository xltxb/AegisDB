@echo off
REM 本地启动 AegisDB 后端（PostgreSQL）。
REM 前置：装好 PostgreSQL 并建库  createdb vela_gateway
REM DSN 见 configs/config.yaml 的 postgres_dsn（可用 VELA_PG_DSN 覆盖）。
setlocal
set APP_ENV=dev

set GO=go
where go >nul 2>nul || set GO="C:\Program Files\Go\bin\go.exe"

echo [AegisDB] env=dev (postgres)  ->  http://localhost:8080
echo 演示登录: linwei@vela.io / vela123
%GO% run ./cmd/server
endlocal
