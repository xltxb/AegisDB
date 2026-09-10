#!/usr/bin/env bash
# Build & package DP DB GATEWAY (frontend + backend) into a self-contained,
# production-deployable bundle under ./dist, plus a versioned tarball.
#
# Usage:
#   ./build.sh                                  # build for linux/amd64 (the deploy target)
#   GOOS=... GOARCH=... ./build.sh              # override the target explicitly
#   VERSION=1.2.0 ./build.sh                    # stamp an explicit version
#
# The server runs on Linux. The target therefore defaults to linux/amd64 rather
# than the host OS: building on a Windows workstation used to silently produce a
# Windows server binary that can never be deployed.
#
# The backend is pure-Go (sqlite + mysql drivers need no cgo), so cross-compiling
# needs no C toolchain.
#
# Output (dist/):
#   vela-gateway[.exe]        server binary — also runs `init` / `migrate` / `version`
#   web/                      built frontend (served by the backend, web_dir: web)
#   configs/config.yaml       production config (secrets come from env)
#   migrations/               SQL migrations (also embedded in the binary)
#   deploy/                   systemd unit + env template
#   README-DEPLOY.md          step-by-step deploy guide
#   vela-gateway-<ver>-<os>-<arch>.tar.gz   the shippable archive
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
DIST="$ROOT/dist"
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || GO="/c/Program Files/Go/bin/go.exe"

# Deploy target, NOT the host: see the usage note above.
GOOS_="${GOOS:-linux}"
GOARCH_="${GOARCH:-amd64}"
EXT=""
case "$GOOS_" in windows*) EXT=".exe";; esac
BIN="vela-gateway"

# Version: explicit $VERSION, else git describe, else a date tag.
if [ -z "${VERSION:-}" ]; then
  if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse >/dev/null 2>&1; then
    VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)"
  fi
fi
VERSION="${VERSION:-0.0.0}"

echo "==> DP DB GATEWAY build  (version=$VERSION, target=$GOOS_/$GOARCH_)"

echo "==> Clean dist/"
rm -rf "$DIST"
mkdir -p "$DIST/web"

# 打包前的一道闸:生产环境不得返回任何模拟数据。
#
# 来由是一次线上误判 ——「批量巡检」把 88 台实例全报成「可连」,因为那条探测从仓库
# 第一笔提交起就是个 `sleep(120ms); return true` 的桩,而且没记在任何待办里。
#
# 模拟数据本身留着(本地开发和演示离不开它),但它只能在开发环境生效。这条测试逐条
# 走 docs/simulated-paths.md 里登记的每一条路径,确认关掉模拟之后它们给的是明确的
# 拒绝而不是编出来的数据。跑不过就不出包 —— 这正是"打包部署生产时必须都是真实现"
# 那句话该被强制的位置。
echo "==> Verify no simulated data in prod"
(cd "$ROOT/backend" && "$GO" test ./internal/bootstrap/   -run 'TestProductionServesNoSimulatedData|TestSimulationDefaultsToOff|TestSimulatedPathsAreDocumented'   -count=1 -timeout 10m)

echo "==> Build frontend (Vite)"
cd "$ROOT/frontend"
if [ ! -d node_modules ]; then
  if [ -f package-lock.json ]; then npm ci; else npm install; fi
fi
npm run build
cp -r dist/* "$DIST/web/"

echo "==> Build backend ($GOOS_/$GOARCH_)"
cd "$ROOT/backend"
GOOS="$GOOS_" GOARCH="$GOARCH_" "$GO" build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$DIST/$BIN$EXT" ./cmd/server
# Best-effort locally; the mode that actually ships is set when the archive is
# written (see "Create tarball"). chmod is a no-op on an NTFS working copy under
# Git Bash, which is exactly where this bundle gets built.
chmod 755 "$DIST/$BIN$EXT" 2>/dev/null || true

echo "==> Bundle config, migrations, deploy assets"
mkdir -p "$DIST/configs"
cp configs/config.prod.yaml "$DIST/configs/config.yaml"   # matches the binary's default -config path
mkdir -p "$DIST/migrations"
cp migrations/*.sql "$DIST/migrations/"                   # reference SQL only (also embedded in the binary)
[ -d docs ] && cp -r docs "$DIST/docs" || true
mkdir -p "$DIST/deploy"
cp "$ROOT/deploy/vela-gateway.service" "$DIST/deploy/" 2>/dev/null || true
cp "$ROOT/deploy/vela.env.example" "$DIST/deploy/" 2>/dev/null || true
cp "$ROOT/deploy/vela.env.example" "$DIST/vela.env.example" 2>/dev/null || true

echo "==> Write README-DEPLOY.md"
cat > "$DIST/README-DEPLOY.md" <<EOF
# DP DB GATEWAY — 部署包 ($VERSION, $GOOS_/$GOARCH_)

单二进制同时提供 API 与前端 SPA。默认读取 \`configs/config.yaml\`(profile=prod → MySQL)。
敏感配置一律走环境变量,不要写进 config.yaml。

## 1. 准备
- 一个可连接的 MySQL 8 实例,并创建空库(库名与 DSN 一致):
  \`CREATE DATABASE vela_gateway DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;\`
- 复制 \`vela.env.example\` 为 \`vela.env\` 并填写(\`chmod 600 vela.env\`):
  - \`VELA_JWT_SECRET\`:\`openssl rand -hex 32\`(≥32 位,否则拒绝启动)
  - \`VELA_MYSQL_DSN\`:\`user:pass@tcp(host:3306)/vela_gateway?charset=utf8mb4&parseTime=true&loc=Local\`

## 2. 迁移表结构(幂等,可重复执行)
\`\`\`
set -a; . ./vela.env; set +a
./$BIN$EXT migrate
\`\`\`
迁移记录在 \`schema_migrations\` 表,已应用的版本会跳过。

### 升级已有环境时(不是首次部署,读这一段)

1. **先备份数据库**,再做任何事。迁移里可能包含列改名 / 结构变更,不是所有步骤都能原地退回。
2. 顺序是 **停旧进程 → \`migrate\` → 起新进程**。反过来做,新二进制会对着旧表结构跑。
3. 迁移之后**不要**再回滚到上一个版本的二进制:旧代码按旧列名查询,列已经不在了。
   这类失败是"关"的方向——规则查不到就拒绝——所以不会变成放行,但会是一场故障。
   真要回滚,数据库也得一起回滚到备份。
4. 起来之后用 \`./$BIN$EXT version\` 确认跑的是这一版。

## 3. 初始化引用数据 + 平台管理员(仅首次)
\`\`\`
./$BIN$EXT init --admin-email you@corp.io --admin-password 'A-Strong-Passw0rd!'
\`\`\`
(\`init\` 也会先跑迁移;口令需 ≥12 位且含大小写/数字/符号中 ≥3 类。)

## 4. 运行
\`\`\`
./$BIN$EXT -config configs/config.yaml
\`\`\`
默认监听 \`:8080\`,同源提供 SPA。生产建议:
- 前置反向代理终止 TLS,或配 \`VELA_TLS_CERT\`/\`VELA_TLS_KEY\` 让网关直接跑 HTTPS。
- 用 systemd 托管:见 \`deploy/vela-gateway.service\`。

## 5. (可选)启用外部飞书审批(审批魔方)

将高危命令审批推送到飞书交互卡片、结果经回调驱动执行。均为**运行时设置**,在
\`设置 › 审批 › 外部飞书审批\` 里配置(默认关,建议先在 dev/gli 灰度)。回调是能触发
生产执行的高危入口——务必配回调密钥、优先 HTTPS。完整步骤/配置项/安全说明与排查见
\`docs/external-approval-setup.md\`(与 \`docs/adr/0003-external-lark-approval-integration.md\`)。

## 常用命令
- \`./$BIN$EXT version\`   查看版本
- \`./$BIN$EXT migrate\`   仅应用待执行的迁移
- \`./$BIN$EXT init ...\`  迁移 + 引用数据 + 管理员
EOF

echo "==> Create tarball"
#
# The server binary must extract as 0755. Windows has no execute bit and chmod
# is a no-op on an NTFS working copy under Git Bash, so a bundle built on a
# workstation packaged the binary 0644 — the operator extracted it on the target
# and the first command in the deploy guide failed with "Permission denied". The
# mode therefore has to be stamped into the ARCHIVE rather than read off the
# build host's filesystem.
#
# Everything else (web assets, config, SQL) is data and stays 0644, so the
# binary is appended separately with an explicit mode instead of using a blanket
# --mode over the whole tree. Appending needs an uncompressed archive, hence the
# build-then-gzip.
ARCHIVE="vela-gateway-$VERSION-$GOOS_-$GOARCH_.tar.gz"
TARTMP="$ROOT/.vela-bundle.tar"
rm -f "$TARTMP"
tar -C "$DIST" -cf "$TARTMP" --exclude="./$BIN$EXT" .
tar -C "$DIST" -rf "$TARTMP" --mode=0755 "./$BIN$EXT"
gzip -9 -c "$TARTMP" > "$ROOT/$ARCHIVE"
rm -f "$TARTMP"

# Verify rather than assume: this is the property the deploy depends on, and it
# is invisible until someone is on the target box with a broken bundle.
if ! tar -tvzf "$ROOT/$ARCHIVE" | grep -qE "^-rwxr-xr-x.*\./$BIN$EXT\$"; then
  echo "FATAL: $BIN$EXT is not 0755 inside the archive — it would not be runnable on the target" >&2
  tar -tvzf "$ROOT/$ARCHIVE" | grep -E "\./$BIN$EXT\$" >&2
  exit 1
fi

echo ""
echo "==> Done."
echo "    Bundle:  $DIST"
echo "    Archive: $ROOT/$ARCHIVE"
echo "    Deploy:  see dist/README-DEPLOY.md  (migrate -> init -> run)"
