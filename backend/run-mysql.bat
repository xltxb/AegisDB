@echo off
REM 以生产环境（prod = MySQL）启动 Vela Gateway 后端。
REM 需先启动 MySQL：在仓库根目录执行  docker compose up -d
REM DSN 见 configs/config.yaml 的 mysql_dsn（可用 VELA_MYSQL_DSN 覆盖）。
setlocal
set APP_ENV=prod

set GO=go
where go >nul 2>nul || set GO="C:\Program Files\Go\bin\go.exe"

echo [Vela Gateway] env=prod (mysql)  ->  http://localhost:8080
echo 若连接失败：请确认 MySQL 已启动（docker compose up -d）
%GO% run ./cmd/server
endlocal
