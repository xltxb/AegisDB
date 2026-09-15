#!/bin/sh
# 本地启动 AegisDB 后端（PostgreSQL）。
# 前置：装好 PostgreSQL 并建库  createdb vela_gateway
# DSN 见 configs/config.yaml 的 postgres_dsn（可用 VELA_PG_DSN 覆盖）。
set -e
cd "$(dirname "$0")"
export APP_ENV=dev
echo "[AegisDB] env=dev (postgres)  ->  http://localhost:8080"
echo "演示登录: linwei@vela.io / vela123"
exec go run ./cmd/server
