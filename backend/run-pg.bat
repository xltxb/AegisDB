@echo off
REM 本地开发环境用 PostgreSQL 做存储库启动后端（dev 环境 + VELA_DB_DRIVER=postgres）。
REM 默认连本机 Docker 容器 vela-store-pg（postgres:16，端口 5437）里的 vela_gateway_dev 库；
REM 容器没起会先 docker start，库不存在会先建，然后执行迁移再启动。
REM 换库：先 set VELA_POSTGRES_DSN=... 再运行本脚本。零依赖的 SQLite 方式仍可用 run-sqlite.bat。
setlocal
set APP_ENV=dev
set VELA_DB_DRIVER=postgres
if "%PG_CONTAINER%"=="" set PG_CONTAINER=vela-store-pg
if "%VELA_POSTGRES_DSN%"=="" set "VELA_POSTGRES_DSN=host=127.0.0.1 port=5437 user=postgres password=vela_test dbname=vela_gateway_dev sslmode=disable TimeZone=Asia/Shanghai"

set GO=go
where go >nul 2>nul || set GO="C:\Program Files\Go\bin\go.exe"

docker start %PG_CONTAINER% >nul 2>nul
docker exec %PG_CONTAINER% psql -U postgres -c "create database vela_gateway_dev" >nul 2>nul

echo [DP DB GATEWAY] env=dev (postgres)  ->  http://localhost:8080
echo 演示登录: linwei@vela.io / vela123
%GO% run ./cmd/server migrate || (echo 迁移失败：请确认 PostgreSQL 已启动且 VELA_POSTGRES_DSN 正确 & exit /b 1)
%GO% run ./cmd/server
endlocal
