---
name: run-app
description: Launch and drive this gateway locally — Go backend on :8080 (its own store is PostgreSQL) plus Vite frontend on :5173 — and click through the UI with Playwright. Use when asked to run, start, boot, or debug the app, to screenshot a screen, or to confirm a change works in the real app rather than in tests.
---

Two background processes and a Playwright driver. This machine is **macOS**; every command below is bash/zsh from the repo root.

## 0. Prerequisite: the store

The gateway keeps its **own** metadata in PostgreSQL — in dev exactly as in prod (ADR 0018). The zero-dependency SQLite profile is gone, so a PostgreSQL has to be running and the database has to exist:

```bash
createdb vela_gateway    # once per machine — already done on this one
```

`APP_ENV=dev` does **not** pick a storage engine any more. It gates the demo seed, the prod JWT-strength check, and how permissive CORS and the outbound SSRF guard are. The DSN comes from `backend/configs/config.yaml` (`postgres_dsn`, defaulting to `host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable` — no `user=`, so libpq falls back to the OS user), and `VELA_PG_DSN` overrides it.

Nothing else needs preparing: the serve path applies the embedded SQL migrations itself, and with `database.seed: true` (the dev default) an empty database also gets the reference data and one admin. So the whole startup sequence is:

```bash
createdb vela_gateway        # only if it does not exist yet
cd backend && ./run-dev.sh   # migrates, seeds, serves — in that order
```

**Do not run `server init` on the way there.** It is the *production* first-install path (it creates a real named admin from `--admin-email` / `--admin-password`, and fails with `create admin: admin email required` without them). It is **mutually exclusive with the dev demo seed**: `init` writes the reference data, including the 5 rows of `tbl_role`, and the demo seed decides "is this database new?" by `repo.Count(&model.Role{}) == 0` (`internal/bootstrap/seed.go`). Run `init` first and that check is already false, so **`linwei@vela.io` is never created** — the subsequent serve seeds nothing and the documented demo login returns `{"code":40100,"msg":"邮箱或密码错误"}` against a database that looks fully populated. Measured, not inferred. Pick one path: `init` for a production-shaped install, plain serve for anything you intend to click through.

## 1. Launch

Start each as its own background task, logging to the scratchpad:

```bash
cd backend && APP_ENV=dev go run ./cmd/server > "$SCRATCH/backend.log" 2>&1
cd frontend && npm run dev > "$SCRATCH/frontend.log" 2>&1
```

`go` is on PATH here (`/usr/local/go/bin/go`) — no absolute path needed. `backend/run-dev.sh` does the same thing with `APP_ENV` baked in and works as a background task too; the explicit form above just keeps the env visible in the command.

**Check :8080 is free before launching** — a gateway left running from an earlier session answers `/healthz` perfectly well, and the only symptom of the collision is `bind: address already in use` buried at the *end* of a very long gin route dump:

```bash
lsof -nP -iTCP:8080 -sTCP:LISTEN     # empty = free
```

**If :8080 is *not* free, it is someone's live gateway — do not reuse its port or its database.** Run a fully separate instance instead: copy `configs/config.yaml` to the scratchpad, change `server.addr` to a free port, point `database.postgres_dsn` at a throwaway database, and list your frontend's port under `cors_origins`. Then `createdb vela_e2e_check`, launch with `--config <that copy>`, and start Vite from a config of your own (`server.port`, and the `/api` proxy target pointed at your backend port — same-origin through the proxy keeps CORS and the terminal WebSocket working). Stop only the processes you started, `dropdb` only the database you created. The server addr has **no env override**, so the config copy is the only way to move it.

Done when both ports listen.

## 2. Wait for ready

```bash
until lsof -nP -iTCP:8080 -sTCP:LISTEN >/dev/null 2>&1 \
   && lsof -nP -iTCP:5173 -sTCP:LISTEN >/dev/null 2>&1; do sleep 1; done
```

Poll the **ports**, never the Vite log. Its banner is full of ANSI escapes, so `Local:` and `5173` are not adjacent and the obvious grep hangs forever on a server that is already up.

(`lsof` exits non-zero when nothing matches, which is what drives the loop. `netstat -ano | grep LISTENING` is Windows — macOS `netstat` prints neither that word nor the port in that shape, so it would loop forever.)

## 3. Drive the API

Demo login is `linwei@vela.io` / `vela123`.

`python` is **not** on this machine (`python3` and `jq` are). Parse with `jq`:

```bash
TOK=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"linwei@vela.io","password":"vela123"}' \
  | jq -r '.data.token')
curl -s http://localhost:8080/api/v1/connections -H "Authorization: Bearer $TOK"
```

Every response is enveloped as `{code, msg, data}`; `code: 0` is success, and an interception comes back as `code: 42200` rather than an HTTP error.

### The two connections worth having

**The seed creates no connections** — on a fresh database `/connections` returns `data: []`. Create them yourself, one per tier, because **judgement depends on the tier**:

```bash
mkdir -p ~/.aegisdb-demo
for E in dev prod; do
  curl -s -X POST http://localhost:8080/api/v1/connections \
    -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
    -d "{\"name\":\"demo-$E\",\"engine\":\"sqlite\",\"host\":\"local\",\"port\":0,
         \"env\":\"$E\",\"policy\":\"audit-only\",\"defaultRole\":\"dba_l2\",
         \"database\":\"$HOME/.aegisdb-demo/demo-$E.db\"}"
done
```

**Put the fixture files in `~/.aegisdb-demo/`, not in the scratchpad.** The connection row outlives the session — it is stored in `vela_gateway`, not on disk next to the run — so a `database` under `$SCRATCH` points into a directory that gets cleaned up, and the next session inherits two connections that look perfectly healthy in the list and fail the moment a session opens on them. Write `$HOME/...`, not `~/...`: the value is stored verbatim and handed to the sqlite driver, which does not expand a tilde and will happily create a directory literally named `~`.

Already have connections pointing somewhere temporary? `PUT /connections/:id` moves them (`PATCH` only takes `tags` / `policy` / `status`). It needs the whole `ConnectionUpdateReq` — `name`, `engine`, `host`, `env`, `policy` are all required — but `port`, `defaultRole` and `status` are preserved across the update:

```bash
curl -s -X PUT http://localhost:8080/api/v1/connections/2 \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d "{\"name\":\"demo-prod\",\"engine\":\"sqlite\",\"host\":\"local\",\"env\":\"prod\",
       \"policy\":\"audit-only\",\"database\":\"$HOME/.aegisdb-demo/demo-prod.db\"}"
```

On an otherwise untouched database these land as **id 1 `demo-dev` (dev)** and **id 2 `demo-prod` (prod)**. The risk dictionary is `off` on dev, so a DROP that sails through on id 1 is gated on id 2 — measured, not assumed:

```bash
curl -s -X POST http://localhost:8080/api/v1/risk/check \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"connectionId":2,"sql":"DROP TABLE t;"}'
```

The statement goes in **`sql`**, not `statement` (`dto.RiskCheckReq`, `internal/dto/dto.go`). Guess the name wrong and the only symptom is a bare `code: 40001` — the envelope never says which field it wanted, so it reads like the endpoint is broken.

| `POST /risk/check` with `DROP TABLE t;` | result |
|---|---|
| `connectionId: 1` (dev) | `risk: low`, `action: allow` |
| `connectionId: 2` (prod) | `risk: high`, `action: approve` |

**Test gating against a prod connection or you will conclude the gate is broken.**

`engine: sqlite` on purpose: SQLite is a **target-database** driver (backend only — the UI's engine dropdown does not offer it), so the fixture needs no real MySQL or Oracle anywhere. This has nothing to do with the gateway's own store, which is PostgreSQL either way.

## 4. Drive the UI

Playwright and its Chromium are already installed (`@playwright/test` is a devDependency).

Write the driver to `frontend/` and run it with that as the working directory. Node resolves `playwright` from the importing file's location, so a script sitting in the scratchpad dies with `ERR_MODULE_NOT_FOUND`.

```js
import { chromium } from 'playwright'
const page = await (await chromium.launch()).newPage({ viewport: { width: 1560, height: 900 } })
page.on('pageerror', (e) => console.log('pageerror:', e.message))
await page.goto('http://localhost:5173/', { waitUntil: 'networkidle' })
await page.click('button.portal-opt:has-text("运维前台")')   // pick the portal first — see below
await page.fill('input[type="email"]', 'linwei@vela.io')
await page.fill('input[type="password"]', 'vela123')
await page.click('button.login-submit')
await page.waitForTimeout(2500)                              // lands on /dashboard
await page.goto('http://localhost:5173/terminal', { waitUntil: 'networkidle' })
```

The router uses **ordinary paths, not hashes**: `/login`, `/dashboard`, `/terminal`, `/approvals`, `/inbox`, `/audit`. `#/terminal` addresses nothing. Login lands on **`/dashboard`**, not `/terminal`.

### Two portals — pick the right one or the screen you want is unreachable

The login page offers a choice (`button.portal-opt`) that decides which half of the app you get, and it **silently redirects** anything outside that half to `/terminal`:

| Portal | Screens it reaches |
|---|---|
| **运维前台** (ops) | `/dashboard` `/terminal` `/approvals` `/inbox` `/changes` `/scripts` `/export` `/async-jobs` `/exec-windows` `/catalog` |
| **管理后台** (admin) | `/dashboard` `/connections` `/risk-rules` `/sql-review` `/gov` `/inbox` `/permissions` `/pipelines` `/users` `/audit` `/settings` |

So **`/audit` and `/connections` need 管理后台**. Navigate to `/audit` from the ops portal and you land on a perfectly healthy-looking terminal page with no error of any kind — the symptom of going to the wrong portal is *the wrong screen*, not a failure. Check `page.url()` after every `goto`.

Delete the driver from `frontend/` when done — it is untracked, and leaving it there dirties the next `git status`.

### The production-entry gate blocks everything until you dismiss it

Opening a session on a **prod** instance raises a 你正在进入生产环境 modal, and its `.c-overlay` swallows every click behind it. Nothing on the page is clickable until it is answered, and the failure mode is a 30-second Playwright timeout whose message blames the button you clicked (`<div class="c-overlay">…</div> intercepts pointer events`) rather than the modal. Dismiss it first:

```js
const guard = await page.$('button:has-text("我确认,继续")')   // or 走错了,退出
if (guard) { await guard.click(); await page.waitForTimeout(800) }
```

It appears once per session, so probe with `$` and only click when present.

### Selectors that hold

Verified against the running app, not inferred:

| Target | Selector |
|---|---|
| Portal chooser (login page) | `button.portal-opt` filtered by its label text |
| Login submit | `button.login-submit` |
| Toolbar button (terminal) | `button.tv-act` filtered by its label text — `粘贴 SQL`, `快捷脚本`, `导出日志`, `表格视图` |
| Paste dialog textarea | `textarea.paste-box` |
| Dialog confirm / cancel | `button.c-btn.v-primary` / `button.c-btn.v-ghost` |
| Modal (any) | `.c-overlay` (backdrop) / `.c-modal` (panel); close X is `.c-modal-x` |
| Approval modal | command in `.cmd`, matched rule in `.notice.danger`, reason field in `.fld` |
| Audit chain check | `button:has-text("校验链")`; the verdict banner is rendered inline above the table |
| Terminal surface | `.xterm-rows` — real DOM text, assertable (see below) |

**Always target `textarea.paste-box` by class.** The first `<textarea>` on a terminal page is xterm's own hidden `textarea.xterm-helper-textarea`, so `(await page.$$('textarea'))[0]` fills the wrong element and nothing happens.

**The terminal is *not* a canvas.** xterm runs its DOM renderer here — `.xterm-rows` is present and the page has **zero** `canvas` elements — so the scrollback is ordinary text. `(await page.innerText('body'))` contains the prompts, the echoed statements, the box-drawn result grid and the `✓ 执行成功 · N 行受影响 · 耗时 Nms` lines, and you can assert on all of it directly. Asserting only on the modals the terminal raises still works, but it is no longer the only option.

Done when you have **looked at the screenshot**. A blank frame means the launch failed, not that the assertion passed.

## 5. Stop

Stop both background tasks by id. State now lives in **PostgreSQL**, not in a file next to the binary — so for a clean re-seed, drop the database instead of deleting anything:

```bash
dropdb vela_gateway && createdb vela_gateway   # next launch migrates and seeds from scratch
```

Only when `vela_gateway` is yours to destroy. If you launched an isolated instance because :8080 was busy (§1), drop **that** database instead — the one on :8080 has someone's work in it.

Two things in `backend/` are **not** gateway state and must survive that: `target.db` is the demo *target* database (managed data, not the store), and `uploads/` / `export/` hold whatever a run wrote there. `~/.aegisdb-demo/` is the same kind of thing — target data, not store — and dropping `vela_gateway` does not touch it. What a drop *does* remove is the connection rows pointing at those files, so after a re-seed you recreate the two connections (§3) against the fixtures still sitting there.

Leave `vela_test` alone — that is the test suite's database, not this one's.
