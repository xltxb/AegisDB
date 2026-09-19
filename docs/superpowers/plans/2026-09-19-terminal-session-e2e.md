# 终端会话端到端测试 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给终端会话补上一条真正走完「连接 → 键入 → 提交 → 回执 → 断开 → 重连」的端到端测试，外加三条这段代码已经踩过的坑。

**Architecture:** 复用现有的 `e2e` 层（真实 Chromium + Vite dev server），用 `page.routeWebSocket()` 在浏览器里拦下 `new WebSocket()`，由测试脚本扮演网关。不新增测试层、不起后端、零新增 devDependency。

**Tech Stack:** Playwright 1.63.0 · React 19 (StrictMode) · xterm.js 5.5（DOM renderer）

**Spec:** `docs/superpowers/specs/2026-09-19-terminal-session-e2e-design.md`

## Global Constraints

- Playwright 版本 `^1.63.0`。`page.routeWebSocket()` 需要 ≥1.48。
- **不新增任何 devDependency。** 这是选这条路线的核心理由。
- **不修改 `frontend/playwright.unit.config.ts`。** 那一层的取舍不变。
- **不让 e2e 依赖 Go 后端。** 所有 HTTP 走 `page.route` 打桩，WebSocket 走替身。
- 默认语言是 zh，断言对的是 `src/locales/zh.ts` 里的字串。
- 已知选择器：状态灯 `.tv-dot`（类名取 `connecting|open|closed`）、当前实例 `.tv-target`、
  终端文字 `.xterm-rows`、结果表 `.rg`、实例树条目 `.tv-inst`、
  导出日志按钮 `导出日志`、重连按钮 `title="重连会话"`。
- 开场白帮助行：`# 语句以 ; 结束并执行`（`termHelpLine`）。**数开场白只能数这一行**——
  实例名在提示符里每行都出现。
- 提示符形如 `sandbox/appdb ❯ `。

---

### Task 1: 把终端的 HTTP 打桩提进 fixtures

`stubCommon` 现在是 `env-tier-tree.spec.ts` 的私有函数，新规格要用同一套。留在原地就得抄第二份，两份迟早对不上。

**Files:**
- Modify: `frontend/e2e/fixtures.ts`
- Modify: `frontend/e2e/env-tier-tree.spec.ts:43-63`（删掉私有 `stubCommon`，改调公共的）

**Interfaces:**
- Produces: `stubTerminal(page: Page, conns?: Conn[], tiers?: Tier[], envs?: Env[]): Promise<void>`，
  以及导出的默认数据 `DEV_TIERS`、`DEV_ENVS`、`DEV_CONNS`。

- [ ] **Step 1: 在 `fixtures.ts` 末尾加入公共打桩**

```ts
/** 终端页开一条会话要喂的全部 HTTP。e2e 不起后端,这些全部由规格自己描述。 */
export async function stubTerminal(
  page: Page,
  conns: unknown[] = DEV_CONNS,
  tiers: unknown[] = DEV_TIERS,
  envs: unknown[] = DEV_ENVS,
) {
  await seedSession(page)
  await stubShell(page, ADMIN)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(conns)))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(tiers)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(envs)))
  await page.route('**/api/v1/environments/usage', (r) => r.fulfill(envelope({})))
  await page.route('**/api/v1/connections/*/schema**', (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [] })))
  await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/snippets**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/script-uploads**', (r) => r.fulfill(envelope([])))
  // 预检默认放行。要验拦截的规格自己覆盖这一条。
  await page.route('**/api/v1/risk/check', (r) =>
    r.fulfill(envelope({ action: 'allow', requiresApproval: false, matchedRule: '', matchedRuleRef: null })))
  // 补全的词典与导出日志的审计。不打桩它们会去打真实后端,在 e2e 里就是一条
  // ECONNREFUSED,让规格的失败信息里混进一堆与它无关的噪声。
  await page.route('**/api/v1/risk-commands**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/terminal/transcript-export', (r) => r.fulfill(envelope({})))
}

const conn = (id: number, name: string, env: string, engine = 'mysql', database = 'appdb') => ({
  id, name, env, engine, host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database, status: 'online',
})

export const DEV_TIERS = [
  { code: 'dev', displayName: '测试 · DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L4', defaultRole: 'developer' },
]
export const DEV_ENVS = [{ code: 'dev', displayName: '测试 · DEV', tierCode: 'dev', sortOrder: 0 }]
/** 两台实例:切实例那条规格要有地方可切。 */
export const DEV_CONNS = [conn(1, 'sandbox', 'dev'), conn(2, 'staging', 'dev', 'mysql', 'shopdb')]
```

- [ ] **Step 2: 让 `env-tier-tree.spec.ts` 改用公共函数**

删掉它自己的 `stubCommon`，把 import 改成：

```ts
import { envelope, seedSession, stubShell, stubTerminal } from './fixtures'
```

并把 `stubCommon(page)` 的两处调用改成 `stubTerminal(page, CONNS, TIERS, ENVIRONMENTS)`。
`seedSession` / `stubShell` 仍被它的 `connections page` 那条用到，保留 import。

- [ ] **Step 3: 跑既有 e2e，确认没改坏**

Run: `cd frontend && npx playwright test env-tier-tree --reporter=list`
Expected: 7 passed（这个文件原有的条数），无新增失败。

- [ ] **Step 4: Commit**

```bash
git add frontend/e2e/fixtures.ts frontend/e2e/env-tier-tree.spec.ts
git commit -m "test(e2e): lift the terminal page's stub set into fixtures"
```

---

### Task 2: WebSocket 替身，跑通主路

**Files:**
- Create: `frontend/e2e/wsFake.ts`
- Create: `frontend/e2e/terminal-session.spec.ts`

**Interfaces:**
- Consumes: Task 1 的 `stubTerminal`、`DEV_CONNS`。
- Produces:
  - `installWsFake(page: Page, onFrame?: (f: Frame, fake: WsFake) => void): Promise<WsFake>`
  - `interface Frame { type: string; [k: string]: unknown }`
  - `WsFake`：`sent: Frame[]`、`sentOf(type: string): Frame[]`、
    `waitFor(type: string, n?: number): Promise<Frame>`、`send(f: Frame): void`、
    `drop(): void`、`setOffline(v: boolean): void`、
    `stats(): { opened: number; peakLive: number; live: number }`

- [ ] **Step 1: 写替身**

```ts
import { expect, type Page, type WebSocketRoute } from '@playwright/test'

export interface Frame { type: string; [k: string]: unknown }

export interface WsFake {
  /** 客户端发过来的帧,按顺序。**不含 ping** —— 心跳由替身自己答掉。 */
  readonly sent: Frame[]
  sentOf(type: string): Frame[]
  /** 等到客户端发出第 n 条该类型的帧,并把它返回。 */
  waitFor(type: string, n?: number): Promise<Frame>
  /** 按剧本回一帧。说话对象永远是最新那条活 socket。 */
  send(frame: Frame): void
  /** 单方面断开。客户端会在 1 秒后自动重连 —— 要稳住断开状态请用 setOffline。 */
  drop(): void
  /**
   * 拒不接客。
   *
   * `drop()` 之后 `WsTerminal.scheduleReconnect` 第一次退避是 500×2¹ = **1 秒**,
   * 于是「断开后状态灯转红」这个断言会和自动重连赛跑。offline 让断开成为一个
   * 稳定状态:新 socket 一建起就关掉,客户端一直停在 closed。
   */
  setOffline(v: boolean): void
  stats(): { opened: number; peakLive: number; live: number }
}

/**
 * 终端那条 WebSocket 的脚本化替身。
 *
 * 它顶替的是网关,所以要守住网关这一侧的两条约定,否则规格会毫无征兆地飘:
 *
 *  · **自动回 pong。** 客户端每 20s 发一次 ping,5s 内收不到 pong 就判定半开并
 *    强制重连(见 lib/wsTerminal.ts 的 startHeartbeat)。
 *  · **永远对最新那条活 socket 说话。** StrictMode 下 effect 跑两遍,第一条被
 *    清理函数关掉,留活的是第二条。
 */
export async function installWsFake(
  page: Page,
  onFrame?: (frame: Frame, fake: WsFake) => void,
): Promise<WsFake> {
  const sent: Frame[] = []
  const live: WebSocketRoute[] = []
  let opened = 0
  let peakLive = 0
  let offline = false

  const newest = () => live[live.length - 1]

  const fake: WsFake = {
    sent,
    sentOf: (type) => sent.filter((f) => f.type === type),
    async waitFor(type, n = 1) {
      await expect
        .poll(() => fake.sentOf(type).length, { message: `等客户端发出第 ${n} 条 "${type}"` })
        .toBeGreaterThanOrEqual(n)
      return fake.sentOf(type)[n - 1]
    },
    send(frame) {
      const ws = newest()
      if (!ws) throw new Error('没有活着的 socket 可发')
      ws.send(JSON.stringify(frame))
    },
    drop() {
      const ws = newest()
      if (!ws) throw new Error('没有活着的 socket 可断')
      ws.close()
    },
    setOffline(v) {
      offline = v
      if (v) for (const ws of [...live]) ws.close()
    },
    stats: () => ({ opened, peakLive, live: live.length }),
  }

  await page.routeWebSocket('**/terminal/ws', (ws) => {
    opened++
    if (offline) { ws.close(); return }
    live.push(ws)
    peakLive = Math.max(peakLive, live.length)
    ws.onClose(() => {
      const i = live.indexOf(ws)
      if (i >= 0) live.splice(i, 1)
    })
    ws.onMessage((raw) => {
      let frame: Frame
      try { frame = JSON.parse(String(raw)) } catch { return }
      if (frame.type === 'ping') { ws.send(JSON.stringify({ type: 'pong' })); return }
      sent.push(frame)
      onFrame?.(frame, fake)
    })
  })

  return fake
}

/** 一份看得出是「哪一行」的结果。 */
export const ROWS_OUTPUT: Frame = {
  type: 'output', columns: ['id', 'name'], data: [['1', 'alice']], rows: 1, ms: 12,
}
```

- [ ] **Step 2: 写主路规格**

新建 `frontend/e2e/terminal-session.spec.ts`：

```ts
import { test, expect, type Page } from '@playwright/test'
import { DEV_CONNS, stubTerminal } from './fixtures'
import { installWsFake, ROWS_OUTPUT, type WsFake } from './wsFake'

// 终端会话唯一走得完整条路的地方。
//
// 单元测试(tests/unit)在 Node 里跑纯逻辑,碰不到 effect;而这条路上出问题的方式
// ——「偶尔断连」「切实例后编辑器一直 busy」——都是 effect 与 socket 生命周期
// 的事。所以它必须在真浏览器里、对着一条真的(被替身顶掉的)WebSocket 跑。
//
// 替身而不是真后端:e2e 的前提是「纯前端、几秒钟」,起一个 Go 网关加一个库会把
// 这个前提换掉。而 StrictMode 只在 npm run dev 里有,所以这一层反而是唯一能
// 白拿到 effect 双跑的地方。

const term = (page: Page) => page.locator('.xterm-rows')
const dot = (page: Page) => page.locator('.tv-dot')

async function openSession(page: Page): Promise<WsFake> {
  const fake = await installWsFake(page)
  await stubTerminal(page)
  await page.goto('/terminal')
  await expect(page.locator('.tv-tree')).toBeVisible()
  // 开场白到屏幕上,才算这条会话真的起来了。
  await expect(term(page)).toContainText('# 语句以 ; 结束并执行', { timeout: 10_000 })
  return fake
}

/** 往终端里敲一条语句并回车。 */
async function type(page: Page, text: string) {
  await page.locator('.xterm').first().click()
  await page.keyboard.type(text)
}
async function submit(page: Page, sql: string) {
  await type(page, sql)
  await page.keyboard.press('Enter')
}

test.describe('终端会话 · 一条路走完', () => {
  test('连接 → 键入 → 提交 → 回执 → 落屏落日志 → 断开 → 重连', async ({ page }) => {
    const fake = await openSession(page)

    // 开场白点名这条会话连的是谁、什么角色、什么策略。
    await expect(term(page)).toContainText('sandbox')
    await expect(term(page)).toContainText('ro')
    await expect(term(page)).toContainText('strict')
    await expect(page.locator('.tv-target')).toHaveText('dev-sandbox')

    await submit(page, 'select id,name from users;')

    // 断言**真正送进网关的那一帧**,不只是屏幕。屏幕对了而帧错了(比如漏了
    // database,语句就悄悄跑到别的库上),正是最难手点出来的一类回归。
    const exec = await fake.waitFor('exec')
    expect(exec).toMatchObject({
      type: 'exec', connectionId: 1, sql: 'select id,name from users', database: 'appdb',
    })

    fake.send(ROWS_OUTPUT)

    // 落屏:行进了终端。
    await expect(term(page)).toContainText('alice')
    await expect(term(page)).toContainText('1 行')
    // 落结果表。
    await expect(page.locator('.rg')).toContainText('alice')
    // 落日志:导出按钮从灰变亮,说明这次会话有东西可导。
    await expect(page.getByRole('button', { name: '导出日志' })).toBeEnabled()

    // 断开。offline 把它稳住 —— 否则 1 秒后的自动重连会和这个断言赛跑。
    fake.setOffline(true)
    await expect(dot(page)).toHaveClass(/closed/)

    // 重连。
    fake.setOffline(false)
    const before = fake.stats().opened
    await page.getByTitle('重连会话').click()
    await expect(dot(page)).toHaveClass(/open/)
    expect(fake.stats().opened).toBeGreaterThan(before)

    // 重连之后这条会话仍然能用 —— 断了一次不该把终端留在半死状态。
    await submit(page, 'select 2;')
    await fake.waitFor('exec', 2)
  })
})
```

- [ ] **Step 3: 跑它**

Run: `cd frontend && npx playwright test terminal-session --reporter=list`
Expected: 1 passed。

若 `select id,name from users` 的 `sql` 断言失败，先打印 `fake.sent` 看真实形状再调断言，**不要**把断言放宽成 `expect.any(String)`——那等于不验。

- [ ] **Step 4: Commit**

```bash
git add frontend/e2e/wsFake.ts frontend/e2e/terminal-session.spec.ts
git commit -m "test(e2e): drive a whole terminal session against a scripted socket"
```

---

### Task 3: 坑 1（StrictMode）与坑 2（在途断连）

**Files:**
- Modify: `frontend/e2e/terminal-session.spec.ts`

**Interfaces:**
- Consumes: Task 2 的 `openSession`、`submit`、`type`、`term`、`WsFake`。

- [ ] **Step 1: 写两条坑规格**

追加到 `terminal-session.spec.ts`：

```ts
test.describe('终端会话 · 踩过的坑', () => {
  /*
   * StrictMode 下 effect 跑两遍。两遍之间什么都没变,所以第二遍再打一次开场白,
   * 就是屏幕上凭空多出一段;第二条 socket 若不关,两条各收一半消息。
   *
   * 判据是**同时存活只有一条**,不是「只开一条」—— StrictMode 下必然开两条
   * (挂载 → 清理 → 再挂载),断言开一条是误报。
   */
  test('effect 跑两遍,但开场白只打一次、socket 只活一条', async ({ page }) => {
    const fake = await openSession(page)

    expect(fake.stats().opened).toBeGreaterThan(0)
    expect(fake.stats().peakLive).toBe(1)

    // 数开场白只能数帮助行:实例名在提示符里每行都出现,数它会把 1 段开场白
    // 数成 3 次。
    const screen = await term(page).innerText()
    expect(screen.split('# 语句以 ; 结束并执行').length - 1).toBe(1)
  })

  /*
   * 语句在途时 socket 断掉,应答永远不会来。编辑器若停在 busy,之后每个按键都被
   * 吞进粘贴队列(lib/lineEditor.ts 的 handleData),终端看着就像死了 —— 而这正是
   * 「切实例后编辑器一直 busy」那类报告的现场。
   *
   * 放它出来的是 useTerminalSession 里 onStatus('closed') → editor.resume()。
   */
  test('语句在途时断连,编辑器被放出来而不是永远 busy', async ({ page }) => {
    const fake = await openSession(page)

    await submit(page, 'select pg_sleep(60);')
    await fake.waitFor('exec')

    // 不回执,直接断。
    fake.setOffline(true)
    await expect(dot(page)).toHaveClass(/closed/)

    // 断线之后敲的字必须看得见。看不见就说明按键被吞进了队列。
    await type(page, 'select 1')
    await expect(term(page)).toContainText('select 1')
  })
})
```

- [ ] **Step 2: 跑,确认两条都绿**

Run: `cd frontend && npx playwright test terminal-session --reporter=list`
Expected: 3 passed。

- [ ] **Step 3: 证明坑 2 那条抓得住**

把修复临时改坏 —— 在 `frontend/src/hooks/useTerminalSession.ts` 找到：

```ts
        if (s === 'closed') editor.resume()
```

改成：

```ts
        if (s === 'closed') { /* 临时改坏,验证规格抓得住 */ }
```

Run: `cd frontend && npx playwright test terminal-session --reporter=list -g "编辑器被放出来"`
Expected: **FAIL** —— `.xterm-rows` 里等不到 `select 1`。

看到红之后把那一行**改回原样**，再跑一次确认回到绿。

不做这一步，写出来的只是一条永远绿的装饰。

- [ ] **Step 4: 证明坑 1 那条抓得住**

把 `frontend/src/pages/terminal/index.tsx` 的开场白闸门临时改坏：

```ts
    if (!connKey || banneredFor.current === connKey) return
```

改成：

```ts
    if (!connKey) return
```

Run: `cd frontend && npx playwright test terminal-session --reporter=list -g "开场白只打一次"`
Expected: **FAIL** —— 帮助行出现 2 次而不是 1 次。

确认红之后改回原样，再跑一次确认回到绿。

- [ ] **Step 5: Commit**

```bash
git add frontend/e2e/terminal-session.spec.ts
git commit -m "test(e2e): pin the StrictMode and mid-flight-disconnect traps"
```

---

### Task 4: 坑 3（切实例要把屏幕和日志一起清）

**Files:**
- Modify: `frontend/e2e/terminal-session.spec.ts`

**Interfaces:**
- Consumes: Task 2/3 的全部辅助函数。

- [ ] **Step 1: 写规格**

在 `terminal-session.spec.ts` 顶部补 import：

```ts
import { readFileSync } from 'node:fs'
```

追加到「踩过的坑」那个 describe 里：

```ts
  /*
   * 换实例就是换一次会话。屏幕和日志必须一起清:只清日志会留下一个**看得见却
   * 导不出**的落差 —— 屏幕上还挂着上一台的输出,而导出的文件从新开场白才开始,
   * 头部却只写着当前这台。两者要说同一件事。
   */
  test('切实例把屏幕和日志一起清,不留上一台的输出', async ({ page }) => {
    const fake = await openSession(page)

    // 在 sandbox 上留下一条看得出是它的输出。
    await submit(page, 'select id,name from users;')
    await fake.waitFor('exec')
    fake.send(ROWS_OUTPUT)
    await expect(term(page)).toContainText('alice')

    // 切到第二台。
    await page.locator('.tv-inst').filter({ hasText: 'staging' }).click()
    await expect(page.locator('.tv-target')).toHaveText('dev-staging')

    // 屏幕这一半:新开场白在,上一台的输出没了。
    await expect(term(page)).toContainText('staging')
    await expect(term(page)).not.toContainText('alice')

    // 日志那一半:导出的文件里也不该有上一台的输出。只断言屏幕就只验了一半 ——
    // 而这条规格存在的理由正是「两者说同一件事」。
    await submit(page, 'select 1;')
    await fake.waitFor('exec', 2)
    fake.send({ type: 'output', text: 'ok', ms: 3 })
    await expect(page.getByRole('button', { name: '导出日志' })).toBeEnabled()

    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.getByRole('button', { name: '导出日志' }).click(),
    ])
    const file = await download.path()
    const text = readFileSync(file, 'utf8')
    expect(text).toContain('staging')
    expect(text).not.toContain('alice')
  })
```

- [ ] **Step 2: 跑**

Run: `cd frontend && npx playwright test terminal-session --reporter=list`
Expected: 4 passed。

若 download 这一段别扭（拿不到文件、文件名编码等），按 spec 的「已知取舍」退成
断言「导出按钮可用 + 屏幕已清」，并在规格注释里**写明少验了什么**——不要悄悄砍掉。

- [ ] **Step 3: 证明它抓得住**

把 `frontend/src/pages/terminal/index.tsx` 开场白 effect 里的这两行临时注释掉一行：

```ts
    transcript.current.clear()
```

Run: `cd frontend && npx playwright test terminal-session --reporter=list -g "切实例"`
Expected: **FAIL** —— 导出的文件里仍然有 `alice`。

这正是「看得见却导不出」那个落差的反面。确认红之后改回原样，再跑一次确认回到绿。

- [ ] **Step 4: 全量回归**

```bash
cd frontend
npm run test:unit      # 273 passed,不变
npm run test:e2e       # 25 → 30 passed
npm run type-check
npm run lint
```

四项全绿才算完。

- [ ] **Step 5: Commit**

```bash
git add frontend/e2e/terminal-session.spec.ts
git commit -m "test(e2e): switching instance clears the screen and the log together"
```

---

## Self-Review

**Spec coverage**

| spec 要求 | 落在哪 |
|---|---|
| 路线 = routeWebSocket 替身 | Task 2 |
| `fixtures.ts` 提公共打桩 + 补漏网路由 | Task 1 |
| `wsFake.ts` 三条职责（回 pong / 对最新 socket 说话 / 记峰值） | Task 2 Step 1 |
| 主路含「断言发出去的 exec 帧」 | Task 2 Step 2 |
| 坑 1 StrictMode | Task 3 |
| 坑 2 在途断连 | Task 3 |
| 坑 3 屏幕与日志一起清（含 download 那一半） | Task 4 |
| 「先证明规格抓得住」 | Task 3 Step 3/4、Task 4 Step 3 |
| 验收四项全绿 | Task 4 Step 4 |
| 已知取舍的退路写进规格注释 | Task 4 Step 2 |

无遗漏。

**Placeholder scan**：无 TBD / TODO / 「类似 Task N」/ 「加上适当的错误处理」。每个代码步骤都给了可直接落盘的代码。

**Type consistency**：`installWsFake` / `WsFake` / `Frame` / `ROWS_OUTPUT` 在 Task 2 定义，Task 3、4 按同名同签名使用；`stubTerminal` / `DEV_CONNS` 在 Task 1 定义，Task 2 使用。`fake.stats()` 的三个字段（`opened` / `peakLive` / `live`）定义与使用一致。
