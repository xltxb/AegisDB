# 全站 HUD 视觉升级 · 第一阶段实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Vela 设计系统里已经定义但从未落地的科技感词汇（品牌渐变、霓虹辉光、背景模糊、渐变裁字、按下态）用起来，并补一层 HUD 界面底座（网格、辉光、角标、活体脉冲），亮暗两套都做到一等主题。

**Architecture:** 三层各管一件事，任一条规则只出现在一个地方，不存在同选择器覆盖。token 层在 `colors.css` / `effects.css` 追加 HUD 语义变量；骨架层在 `theme.css` 的共用段（约 30–550 行）就地改既有声明；装饰层是新建的 `hud.css`，只放伪元素与新类，由 `main.tsx` 在 `theme.css` 之后 import。

**Tech Stack:** 纯 CSS（CSS 自定义属性、`color-mix(in oklch)`、`backdrop-filter`、`background-clip:text`）+ React 19 TSX（仅登录页一个装饰元素）。测试用 Playwright，两套 seam：`playwright.unit.config.ts`（Node，无浏览器）与 `playwright.config.ts`（真浏览器 + Vite dev server，API 由 spec 自行打桩）。本阶段全部测试落在 e2e seam —— 层叠与几何只有浏览器算得出来。

**Spec:** `docs/superpowers/specs/2026-09-20-hud-visual-refresh-design.md`

## Global Constraints

每个任务的要求都隐含包含本节。

- **不动布局尺寸。** 栏宽、行距、断点、列数、控件高度一律不改。`theme.css` 里写明理由的既有决定（为什么 `.page-wide .c-trow` 是 7px、为什么 `.rail-item` 是 64px、为什么只有 768/1080 两个断点）全部保留。
- **不动文字色与语义状态色 token。** `--text-*`、`--success/warning/danger*` 不改，对比度不退化。
- **装饰一律 `pointer-events: none`，且不改变盒模型。** 角标用 `position:absolute`，网格用 `background-image`，高光线用 1px 绝对定位伪元素或 `background-image`。任何一处改用 `border` / `padding` / `margin` 实现都会挪动布局，`e2e/responsive.spec.ts` 会红。
- **所有新增动画必须在 `@media (prefers-reduced-motion: reduce)` 下 `animation: none`。** `e2e/reduced-motion.spec.ts` 守着这条。
- **渐变只走 azure→cyan。** 设计系统明令禁止 azure→violet（"reads generic"）。
- **业务样式只用语义 token，不写死色值**（`theme.css` 文件头的约定）。唯一例外：品牌渐变 `#3b6ef6 → #2dcde6`，它在 `.rail-logo` / `.rail-avatar` 已经是写死的两个色标，登录页的 `.login-mark` 沿用同一组值以保持品牌标一致。
- **不加斑马线**（审计页一屏 22 行的密度是刻意压出来的）、**不加新依赖**、**不动 i18n 文案**、**不做手机端专门形态**。
- 所有 e2e 测试写进同一个新文件 `frontend/e2e/hud-visual.spec.ts`，每个任务往里追加自己的 `test.describe`。
- 所有命令在 `frontend/` 目录下执行。

---

## File Structure

| 文件 | 职责 | 动作 |
|---|---|---|
| `frontend/src/styles/colors.css` | HUD 语义色 token，亮/暗各一套 | 修改（追加 HUD 段） |
| `frontend/src/styles/effects.css` | HUD 尺寸与渐变 token | 修改（追加 HUD 段） |
| `frontend/src/styles/hud.css` | **装饰层**：页面底座、卡片高光线与角标、脉冲 keyframes、`.hud-corners` | 新建 |
| `frontend/src/styles/theme.css` | **骨架层**：就地改 `.c-card` `.c-table` `.c-btn` `.c-badge` `.top` `.rail` `.login-*` `.dash-*` | 修改（仅共用段与两个样板页段） |
| `frontend/src/styles/base.css` | `:focus-visible` 光晕 | 修改（1 处） |
| `frontend/src/main.tsx` | 在 `theme.css` 之后 import `hud.css` | 修改（1 行） |
| `frontend/src/pages/login/index.tsx` | 登录卡的四角装饰元素 | 修改（1 行 JSX） |
| `frontend/e2e/hud-visual.spec.ts` | 本阶段全部回归 | 新建 |

**为什么装饰层单独成文件**：`theme.css` 已经 2864 行，再塞 200 行装饰会让"骨架"和"皮肤"混在一起；而装饰层全是伪元素与新类，与骨架层没有同选择器冲突，物理分开正好对应职责分开。**为什么不塞进 `theme.css` 的 `@import` 块**：CSS 的 `@import` 必须位于所有规则之前，放在那里会被后面 2800 行同权重规则压过去。

---

### Task 1: HUD token 层 + 装饰层骨架 + 页面底座

**Files:**
- Modify: `frontend/src/styles/colors.css`（`:root` 末尾、`[data-theme="dark"]` 末尾各追加一段）
- Modify: `frontend/src/styles/effects.css`（`:root` 末尾追加一段）
- Create: `frontend/src/styles/hud.css`
- Modify: `frontend/src/main.tsx:7`
- Test: `frontend/e2e/hud-visual.spec.ts`（新建）

**Interfaces:**
- Consumes: 既有语义 token `--glow-accent`（= `--cyan-400`，亮暗同值）、`--surface-page`。
- Produces: 供后续任务使用的 token —— `--hud-grid`、`--hud-grid-size`、`--hud-glow-a`、`--hud-glow-b`、`--hud-corner`、`--hud-corner-size`、`--hud-hairline`、`--hud-edge`。以及 `hud.css` 这个文件本身（后续任务往里追加规则，不再重复 import 步骤）。

- [ ] **Step 1: 写失败的测试**

新建 `frontend/e2e/hud-visual.spec.ts`：

```ts
import { test, expect, type Page } from '@playwright/test'
import { ADMIN, envelope, seedSession, stubShell } from './fixtures'

// HUD 视觉层盯的是"层叠算完之后才成立"的事:一个伪元素有没有背景、一条装饰
// 会不会跟着横滚容器滚出可视区、按下时有没有位移、动效在 reduce 下有没有塌到 0。
// 类型检查、构建、Node 里的单测都看不见这些 —— 只有真浏览器算得出来。
//
// 全部集中在这一个文件里:它们测的是同一层东西(装饰层),改动也总是一起发生。

/** 打开一个已登录的后台页面。gateway/stats 覆盖掉 fixtures 的默认值 —— 总览页
 *  要同时有"普通数字"和"告警数字"两种 .dash-nums,拦截数必须非零。 */
async function open(page: Page, path: string) {
  await seedSession(page)
  // 兜底先注册:Playwright 后注册者优先,stubShell 才盖得住它。
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page, ADMIN)
  await page.route('**/api/v1/gateway/stats**', (r) => r.fulfill(
    envelope({ online: true, p50Ms: 3, p95Ms: 11, samples: 128, intercepts: 2 })))
  // 传 'about:blank' 表示"只装桩,先别跳" —— 调用方还要再加自己的路由,
  // 而 Playwright 的路由是后注册者优先,必须在 goto 之前注册完。
  if (path !== 'about:blank') await page.goto(path)
}

/** 切主题。stores/ui.ts 把主题写在 <html data-theme> 上。 */
async function setTheme(page: Page, theme: 'light' | 'dark') {
  await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
}

/** 读一个元素(或它的伪元素)的计算样式。 */
function styleOf(page: Page, sel: string, prop: string, pseudo?: string) {
  return page.evaluate(([s, p, pe]) => {
    const el = document.querySelector(s!)
    if (!el) throw new Error(`no element for ${s}`)
    return getComputedStyle(el, pe || undefined).getPropertyValue(p!)
  }, [sel, prop, pseudo ?? ''] as const)
}

test.describe('HUD 底座', () => {
  test('两套主题都解析出 HUD token,且取值不同', async ({ page }) => {
    await open(page, '/dashboard')
    const read = () => page.evaluate(() => {
      const cs = getComputedStyle(document.documentElement)
      return {
        grid: cs.getPropertyValue('--hud-grid').trim(),
        glowA: cs.getPropertyValue('--hud-glow-a').trim(),
        corner: cs.getPropertyValue('--hud-corner').trim(),
        size: cs.getPropertyValue('--hud-grid-size').trim(),
      }
    })

    await setTheme(page, 'light')
    const light = await read()
    await setTheme(page, 'dark')
    const dark = await read()

    for (const v of [light.grid, light.glowA, light.corner, light.size]) expect(v).not.toBe('')
    expect(light.size).toBe('32px')
    // 亮暗必须是两套值,不是同一套照抄 —— 亮色网格是冷灰蓝,暗色是白色低透明。
    expect(dark.grid).not.toBe(light.grid)
    expect(dark.glowA).not.toBe(light.glowA)
    expect(dark.corner).not.toBe(light.corner)
  })

  test('底座画在 .main::before 上,不吃点击', async ({ page }) => {
    await open(page, '/dashboard')
    const bg = await styleOf(page, '.main', 'background-image', '::before')
    // 两层径向辉光 + 两层网格线
    expect(bg).toContain('radial-gradient')
    expect(bg).toContain('linear-gradient')
    expect(await styleOf(page, '.main', 'pointer-events', '::before')).toBe('none')
    expect(await styleOf(page, '.main', 'position')).toBe('relative')
  })

  test('底座不随内容滚动 —— 它挂在 .main 上,不是滚动容器 .content', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.content', 'background-image')).toBe('none')
    expect(await styleOf(page, '.content', 'overflow-y')).toBe('auto')
  })

  test('登录页也有底座', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login', 'background-image', '::before')
    expect(bg).toContain('radial-gradient')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 四条全 FAIL。前两条因为 `--hud-grid` 解析为空串、`background-image` 是 `none`。

- [ ] **Step 3: 追加 token（colors.css）**

在 `colors.css` 的 `:root { ... }` 块**末尾**（`--selection-bg` 那行之后、闭合 `}` 之前）追加：

```css
  /* ---- HUD 装饰层(见 hud.css)---- */
  /* 网格取 slate-500 稀释:亮色底座要冷灰蓝,不要黑 —— 黑网格压在白卡片旁边
     像脏,蓝灰网格才像蓝图纸。 */
  --hud-grid:     rgba(105, 116, 139, 0.09);
  /* 辉光压到 6–7%:它的活是把四角从死白里兜住,不是让人看见一团蓝。超过 10%
     在白底上就成了色斑,卡片的白跟着脏。 */
  --hud-glow-a:   rgba(59, 110, 246, 0.07);
  --hud-glow-b:   rgba(45, 205, 230, 0.06);
  --hud-corner:   rgba(59, 110, 246, 0.30);
  --hud-hairline: rgba(59, 110, 246, 0.45);
```

在 `colors.css` 的 `[data-theme="dark"] { ... }` 块**末尾**（`--selection-bg` 那行之后）追加：

```css
  /* ---- HUD 装饰层 ---- */
  /* 暗色底座反过来:网格是白色低透明(深蓝黑上画蓝线看不见),辉光放到能看出
     纵深的量,角标换 cyan —— 暗色里 azure 和卡片边框的亮度差不够。 */
  --hud-grid:     rgba(255, 255, 255, 0.045);
  --hud-glow-a:   rgba(59, 110, 246, 0.16);
  --hud-glow-b:   rgba(45, 205, 230, 0.12);
  --hud-corner:   rgba(45, 205, 230, 0.42);
  --hud-hairline: rgba(94, 131, 251, 0.75);
```

- [ ] **Step 4: 追加 token（effects.css）**

在 `effects.css` 的 `:root { ... }` 块**末尾**（`--blur-lg` 那行之后）追加：

```css
  /* ---- HUD 装饰层 ---- */
  --hud-grid-size:   32px;  /* 4px 栅格 ×8 */
  --hud-corner-size: 12px;
  /* 卡片顶沿那条 1px 高光。两端透明,中段 azure→cyan —— 全宽等浓会变成第二条
     边框,而它要做的是"这块面板的上沿被光扫过",不是再描一次边。 */
  --hud-edge: linear-gradient(90deg,
    transparent,
    var(--hud-hairline),
    color-mix(in oklch, var(--glow-accent) 70%, transparent),
    transparent);
```

`--hud-edge` 放 `effects.css` 而不是 `colors.css`：它是一条复合效果，不是单个色值；而它引用的 `--hud-hairline` 在 `colors.css` 里按主题重定义，渐变会跟着主题自动重算。

- [ ] **Step 5: 新建装饰层文件**

新建 `frontend/src/styles/hud.css`：

```css
/* ============================================================
   HUD 装饰层
   ------------------------------------------------------------
   这里只放"加在骨架之上的装饰":页面底座、卡片高光线与角标、活体脉冲。
   骨架本身(卡片的边框圆角底色、按钮的高度内距、表格的栅格)仍在 theme.css
   里就地改 —— 一条规则只出现在一个地方,两边没有同选择器,也就没有权重战争。

   本文件由 main.tsx 在 theme.css **之后** import。不能塞进 theme.css 的
   @import 块:CSS 的 @import 必须位于所有规则之前,放在那里会被后面 2800 行
   同权重的规则压过去。

   通则:装饰一律 pointer-events: none,且不改变盒模型 —— 全部用绝对定位的
   伪元素或 background-image。用 border / padding 实现的任何一处都会挪动布局,
   e2e/responsive.spec.ts 的溢出断言会红。
   ============================================================ */

/* ---- 页面底座:网格 + 双径向辉光 ---- */

/* 挂 .main 不挂 .content:.content 是 overflow:auto 的滚动容器,背景挂上去会
   随内容滚动,并在每帧重绘整张渐变。.main 是固定高度的 flex 列,画一次就不动。
   而且——固定不动的网格才像 HUD 底座,跟着内容滚的网格像壁纸。 */
.main { position: relative; isolation: isolate; }
.main > * { position: relative; z-index: 1; }
.main::before {
  content: ''; position: absolute; inset: 0; z-index: 0; pointer-events: none;
  background:
    radial-gradient(60% 50% at 88% 0%,  var(--hud-glow-b), transparent 70%),
    radial-gradient(50% 45% at 0% 100%, var(--hud-glow-a), transparent 70%),
    linear-gradient(var(--hud-grid) 1px, transparent 1px)
      0 0 / 100% var(--hud-grid-size),
    linear-gradient(90deg, var(--hud-grid) 1px, transparent 1px)
      0 0 / var(--hud-grid-size) 100%;
}

/* 登录页不在 .main 里,它是独立的全屏 grid。 */
.login { position: relative; isolation: isolate; }
.login > * { position: relative; z-index: 1; }
.login::before {
  content: ''; position: absolute; inset: 0; z-index: 0; pointer-events: none;
  background:
    radial-gradient(55% 45% at 80% 8%,  var(--hud-glow-b), transparent 70%),
    radial-gradient(55% 45% at 12% 92%, var(--hud-glow-a), transparent 70%),
    linear-gradient(var(--hud-grid) 1px, transparent 1px)
      0 0 / 100% var(--hud-grid-size),
    linear-gradient(90deg, var(--hud-grid) 1px, transparent 1px)
      0 0 / var(--hud-grid-size) 100%;
}
```

- [ ] **Step 6: 挂上装饰层**

修改 `frontend/src/main.tsx`，把第 7 行

```ts
import '@/styles/theme.css'
```

改成

```ts
import '@/styles/theme.css'
// 装饰层必须在骨架层之后 —— 见 hud.css 文件头。
import '@/styles/hud.css'
```

- [ ] **Step 7: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 4 passed。

- [ ] **Step 8: 跑既有回归,确认底座没撑破布局**

Run: `npx playwright test e2e/responsive.spec.ts --reporter=list`
Expected: 全绿。底座是 `position:absolute; inset:0`，不参与布局，五档宽度下都不该产生溢出。若红，说明 `.main > *` 那条把某个原本 `position:static` 的子元素提成了 `relative` 并改变了其定位上下文 —— 检查 `AppShell.tsx:229` 的 `.main` 下有哪些子元素。

- [ ] **Step 9: 提交**

```bash
git add src/styles/colors.css src/styles/effects.css src/styles/hud.css src/main.tsx e2e/hud-visual.spec.ts
git commit -m "feat(ui): HUD token layer and the page bed

The Vela tokens already describe a techy surface; nothing drew one. Add
semantic HUD tokens (grid, dual radial glow, corner mark, top hairline) in
both themes, and a decoration layer that paints the bed behind every screen.

The bed hangs on .main rather than the scrolling .content: a background on a
scroll container repaints the whole gradient every frame and, worse, scrolls
with the content — a grid that moves reads as wallpaper, not as an instrument
bed.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 卡片高光线与角标

**Files:**
- Modify: `frontend/src/styles/theme.css:307-310`（`.c-card` 加 `position: relative`）、`:341-345`（`.c-table` 加 `background-image`）、`:1483-1488`（`.dash-card` 加 `position: relative`）
- Modify: `frontend/src/styles/hud.css`（追加卡片装饰段）
- Test: `frontend/e2e/hud-visual.spec.ts`（追加 `test.describe('HUD 卡片')`）

**Interfaces:**
- Consumes: Task 1 的 `--hud-edge`、`--hud-corner`、`--hud-corner-size`。
- Produces: 无新符号。后续任务只依赖这些类名已带装饰这一事实。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 卡片', () => {
  test('卡片顶沿有 1px 渐变高光线', async ({ page }) => {
    await open(page, '/settings')
    expect(await styleOf(page, '.c-card', 'background-image', '::before')).toContain('gradient')
    expect(await styleOf(page, '.c-card', 'height', '::before')).toBe('1px')
    expect(await styleOf(page, '.c-card', 'pointer-events', '::before')).toBe('none')
  })

  test('卡片右上角有 L 形角标', async ({ page }) => {
    await open(page, '/settings')
    const w = await styleOf(page, '.c-card', 'border-top-width', '::after')
    const r = await styleOf(page, '.c-card', 'border-right-width', '::after')
    expect(parseFloat(w)).toBeGreaterThan(0)
    expect(parseFloat(r)).toBeGreaterThan(0)
    // L 形 —— 只有上和右,左和下必须是 0,否则画出来是个方框。
    expect(parseFloat(await styleOf(page, '.c-card', 'border-left-width', '::after'))).toBe(0)
    expect(parseFloat(await styleOf(page, '.c-card', 'border-bottom-width', '::after'))).toBe(0)
  })

  test('.c-table 的高光线用 background-image,不用伪元素', async ({ page }) => {
    await open(page, '/audit')
    await page.waitForSelector('.c-table')
    // 它是 overflow-x:auto 的横滚容器。绝对定位的后代会跟着内容滚出可视区,
    // 而 background-attachment 默认 scroll,锚在元素自己的边框盒上 —— 不随内容滚。
    expect(await styleOf(page, '.c-table', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.c-table', 'overflow-x')).toBe('auto')
    expect(await styleOf(page, '.c-table', 'background-image', '::before')).toBe('none')
  })

  test('卡头分隔线是渐变,不是一条等浓的实线', async ({ page }) => {
    await open(page, '/settings')
    await page.waitForSelector('.c-card-head')
    expect(await styleOf(page, '.c-card-head', 'background-image', '::after')).toContain('gradient')
    // 实色 border 必须让位,否则渐变线叠在实线上等于没换。
    expect(await styleOf(page, '.c-card-head', 'border-bottom-width')).toBe('0px')
  })

  test('表头去掉了实底,底线是渐变', async ({ page }) => {
    await open(page, '/audit')
    await page.waitForSelector('.c-thead')
    const cs = await page.locator('.c-thead').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { color: s.backgroundColor, img: s.backgroundImage, border: s.borderBottomWidth }
    })
    // 透明:实底把表头从卡片里切出来,而它本来就是这张卡的一部分。
    expect(cs.color).toBe('rgba(0, 0, 0, 0)')
    expect(cs.img).toContain('gradient')
    expect(cs.border).toBe('0px')
  })

  test('.perm-rcard 的选中色条没被装饰顶掉', async ({ page }) => {
    await open(page, '/permissions')
    await page.waitForSelector('.perm-rcard')
    await page.click('.perm-rcard')
    await page.waitForSelector('.perm-rcard.on')
    // theme.css:1060 的 ::before 是这张卡的选中态左色条 —— 全站唯一的伪元素
    // 冲突点。卡片装饰不能画到它身上。
    expect(await styleOf(page, '.perm-rcard.on', 'width', '::before')).toBe('3px')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 卡片" --reporter=list`
Expected: 前五条 FAIL（`background-image` 为 `none`、border 宽度为 0、表头仍是实底）；第六条应当**已经绿**——它是回归护栏，测的是既有行为不被破坏。

- [ ] **Step 3: 骨架层加定位上下文**

`theme.css` 的 `.c-card` 规则（约 307 行）：

```css
.c-card {
  border: 1px solid var(--border-subtle); border-radius: 14px;
  background: var(--surface-card); overflow: hidden;
}
```

改为（只加一行 `position: relative`，其余原样）：

```css
.c-card {
  position: relative; /* HUD 装饰的定位上下文,见 hud.css */
  border: 1px solid var(--border-subtle); border-radius: 14px;
  background: var(--surface-card); overflow: hidden;
}
```

`.dash-card` 规则（约 1483 行）同样只加 `position: relative;` 一行。

同时把两处卡头的实色分隔线删掉——渐变线由装饰层接管，两条叠在一起等于没换：

- `.c-card-head`（约 311 行）：删掉 `border-bottom: 1px solid var(--border-subtle);`
- `.dash-card > header`（约 1489 行）：删掉 `border-bottom: 1px solid var(--border-subtle);`

**只删这两处。** `.c-card-row + .c-card-row`、`.c-trow + .c-trow`、`.c-modal-head` 等行间分隔线一律保留——它们是密集列表的读行线索，不是装饰。

- [ ] **Step 4: 骨架层给表格加高光线**

`theme.css` 的 `.c-table` 规则（约 341 行）：

```css
.c-table {
  border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card);
  overflow-x: auto; overflow-y: hidden;
}
```

改为：

```css
.c-table {
  border: 1px solid var(--border-subtle); border-radius: 14px;
  overflow-x: auto; overflow-y: hidden;
  /* 顶沿那条高光走 background-image,不走伪元素:这是个横滚容器,绝对定位的
     后代会跟着内容一起滚 —— 1440 宽的审计表往右滚一屏,线就跑到可视区外面了。
     background-attachment 默认 scroll,锚在元素自己的边框盒上,不随内容滚动。
     底色从 background 简写拆成 background-color,否则被下面的 background-image
     连带清掉。 */
  background-color: var(--surface-card);
  background-image: var(--hud-edge);
  background-repeat: no-repeat;
  background-size: 100% 1px;
  background-position: 0 0;
}
```

紧接着把 `.c-thead` 的实底与实线一并换掉：

```css
.c-thead {
  padding: 12px 18px;
  font: 600 10.5px var(--font-mono); letter-spacing: .06em; text-transform: uppercase; color: var(--text-faint);
  /* 实底把表头从卡片里切出来,而它本来就是这张卡的一部分 —— 改透明,只留
     一条渐变底线分隔。
     底线同样走 background-image 而不是 border:border 画不了渐变(border-image
     会连带吃掉 .c-table 的圆角)。.c-thead 的盒宽等于表格内容全宽,所以这条线
     在横滚时跟着表头一起走,和表头对齐 —— 这是对的,它是行内分隔线,不是
     .c-table 那条锚在容器上的顶沿高光。 */
  background-color: transparent;
  background-image: linear-gradient(90deg,
    var(--border-subtle),
    color-mix(in oklch, var(--hud-hairline) 45%, transparent),
    var(--border-subtle));
  background-repeat: no-repeat;
  background-size: 100% 1px;
  background-position: 0 100%;
}
```

（原 `.c-thead` 规则里的 `background: var(--surface-sunken);` 与
`border-bottom: 1px solid var(--border-subtle);` 两行删掉，其余原样。）

- [ ] **Step 5: 装饰层画高光线与角标**

在 `hud.css` 末尾追加：

```css
/* ---- 卡片:顶沿高光 + 右上角标 ---- */

/* .c-table 不在这组里 —— 它是横滚容器,高光线走 background-image,见 theme.css。
   .perm-rcard 也不在 —— 它的 ::before 被选中态的左色条占着(theme.css:1060),
   是全站唯一的伪元素冲突点。 */
.c-card::before,
.dash-card::before {
  content: ''; position: absolute; top: 0; left: 0; right: 0; height: 1px;
  pointer-events: none; background: var(--hud-edge);
}

/* 只做右上一个角。一个元素只有两个伪元素,四角要么加 DOM、要么把角标画进
   background-image —— 而 .c-card 的 background 常被页面级 CSS 覆盖
   (如 .us-stat.warning)。一个角已经够给出"这是一块仪表面板"的读感。 */
.c-card::after,
.dash-card::after {
  content: ''; position: absolute; top: 10px; right: 10px;
  width: var(--hud-corner-size); height: var(--hud-corner-size);
  pointer-events: none;
  border-top: 1px solid var(--hud-corner);
  border-right: 1px solid var(--hud-corner);
}

/* 卡头分隔线:左浓右淡的渐变,不是一条等浓实线。
   骨架层要把 border-bottom 去掉(见下),否则渐变叠在实线上等于没换。 */
.c-card-head::after,
.dash-card > header::after {
  content: ''; position: absolute; left: 0; right: 0; bottom: 0; height: 1px;
  pointer-events: none;
  background: linear-gradient(90deg,
    color-mix(in oklch, var(--hud-hairline) 55%, transparent),
    var(--border-subtle) 60%,
    transparent);
}
.c-card-head, .dash-card > header { position: relative; }

/* 卡头图标从"淡底色块"升级成"带描边的仪表位"。 */
.c-card-icon, .dash-ico {
  border: 1px solid var(--accent-subtle-border);
  box-shadow: inset 0 0 12px -4px var(--accent);
}
```

- [ ] **Step 6: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 10 passed（Task 1 的 4 条 + 本任务 6 条）。

- [ ] **Step 7: 跑既有回归**

Run: `npx playwright test e2e/responsive.spec.ts e2e/permissions-matrix.spec.ts --reporter=list`
Expected: 全绿。角标是绝对定位、`.c-table` 改的是背景，都不进布局。

- [ ] **Step 8: 提交**

```bash
git add src/styles/theme.css src/styles/hud.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): card hairline and corner mark

Cards were a 1px outline and a flat fill. Give them the top hairline and a
right-hand corner mark so a panel reads as an instrument, not a div.

.c-table gets its hairline from background-image, not a pseudo element: it is
an overflow-x:auto container, and an absolutely positioned descendant scrolls
away with the content — scroll the audit table one screen right and the line
is gone. background-attachment defaults to scroll, anchored to the element's
own border box.

.perm-rcard is left out entirely: its ::before is the selected-state accent
bar (theme.css:1060), the one pseudo-element collision in the codebase.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 可交互卡抬升 + 表格行 hover 竖条

**Files:**
- Modify: `frontend/src/styles/hud.css`（追加交互段）
- Modify: `frontend/src/styles/theme.css:354`（`.c-trow.clickable:hover`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: 既有 token `--shadow-lg`、`--glow-sm`、`--accent`、`--transition-transform`。
- Produces: 无新符号。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 交互态', () => {
  // open() 的兜底桩返回 envelope([]),而审计页读的是 data.items —— 取不到值,
  // 表格渲染 0 行,.c-trow 根本不存在。可点的行必须自己喂数据。
  // 字段名必须对上 types/index.ts 的 AuditRow —— 页面读的是 occurredAt /
  // instance / result / approvalNo,写成别的名字行会渲染出来但全是空格,
  // hover 测的是几何,空格量掉了就测不出位移。
  const AUDIT_ROWS = {
    items: [
      {
        id: 1, occurredAt: '2026-09-20T10:00:00Z', actor: 'linwei',
        instance: 'prod-mysql-01', database: 'shop', command: 'select 1',
        risk: 'low', result: 'executed', approvalNo: '', hash: 'a1',
      },
      {
        id: 2, occurredAt: '2026-09-20T10:01:00Z', actor: 'linwei',
        instance: 'prod-mysql-01', database: 'shop',
        command: 'update orders set a = 1 where id = 1',
        risk: 'high', result: 'executed', approvalNo: 'CR-2026-0001', hash: 'b2',
      },
    ],
    total: 2, page: 1, pageSize: 20,
  }
  async function openAudit(page: Page) {
    await open(page, 'about:blank')
    await page.route('**/api/v1/audit**', (r) => r.fulfill(envelope(AUDIT_ROWS)))
    await page.goto('/audit')
    await page.waitForSelector('.c-trow.clickable')
  }

  test('表格行 hover 时文字没有横向位移', async ({ page }) => {
    await openAudit(page)
    const cell = page.locator('.c-trow.clickable .c-td').first()
    const before = await cell.boundingBox()
    await page.locator('.c-trow.clickable').first().hover()
    const after = await cell.boundingBox()
    // 左侧竖条必须用 inset 阴影,不能用 border-left —— border 会把整行内容
    // 往右挤 2px,hover 一次行内文字跳一下。同 .rail-item.on 的做法。
    expect(after!.x).toBeCloseTo(before!.x, 1)
    expect(after!.width).toBeCloseTo(before!.width, 1)
  })

  test('表格行 hover 时出现左侧 accent 竖条', async ({ page }) => {
    await openAudit(page)
    const row = page.locator('.c-trow.clickable').first()
    await row.hover()
    const shadow = await row.evaluate((el) => getComputedStyle(el).boxShadow)
    expect(shadow).toContain('inset')
    expect(shadow).not.toBe('none')
  })

  test('可交互卡 hover 抬升,静态卡不动', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.dash-card')
    const card = page.locator('.dash-card').first()
    expect(await card.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
    await card.hover()
    // translateY(-2px) → matrix(1, 0, 0, 1, 0, -2)
    const t = await card.evaluate((el) => getComputedStyle(el).transform)
    expect(t).not.toBe('none')
    expect(t).toContain('-2')
  })

  test('.portal-opt 选中态的辉光不被 hover 顶掉', async ({ page }) => {
    await page.goto('/login')
    const on = page.locator('.portal-opt.on').first()
    const before = await on.evaluate((el) => getComputedStyle(el).boxShadow)
    await on.hover()
    const after = await on.evaluate((el) => getComputedStyle(el).boxShadow)
    // hover 规则必须写成 :hover:not(.on),否则选中态的 --glow-sm 被投影盖掉。
    expect(after).toBe(before)
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 交互态" --reporter=list`
Expected: 第 2、3 条 FAIL。第 1、4 条此刻可能已绿（既有行为），保留它们作为护栏。

- [ ] **Step 3: 表格行竖条（骨架层）**

`theme.css` 约 354 行：

```css
.c-trow.clickable:hover { background: var(--surface-sunken); }
```

改为：

```css
/* 左侧竖条用 inset 阴影,不用 border-left —— 同 .rail-item.on 和终端实例树
   选中态的理由:border 会把整行内容向右挤 2px,hover 时行内文字跳一下。 */
.c-trow.clickable:hover {
  background: var(--surface-sunken);
  box-shadow: inset 2px 0 0 var(--accent);
}
```

- [ ] **Step 4: 可交互卡抬升（装饰层）**

在 `hud.css` 末尾追加：

```css
/* ---- 可交互卡:hover 抬升 ---- */

/* 只给确实可点的三类。整站卡片一起飘会显得廉价,而"能不能点"正是 hover
   反馈唯一要回答的问题。 */
.dash-card, .perm-rcard, .portal-opt {
  transition: var(--transition-colors), var(--transition-transform);
}
.dash-card:hover,
.perm-rcard:hover,
.portal-opt:hover:not(.on) {
  transform: translateY(-2px);
  box-shadow: var(--shadow-lg);
}
/* 暗色不投影,靠辉光 —— 设计系统:dark mode drops drop-shadows。 */
[data-theme="dark"] .dash-card:hover,
[data-theme="dark"] .perm-rcard:hover,
[data-theme="dark"] .portal-opt:hover:not(.on) {
  box-shadow: var(--glow-sm);
}
/* .portal-opt.on 已经带 --glow-sm(theme.css 的登录段)。hover 写成
   :hover:not(.on),否则选中态的辉光被 hover 的投影盖掉。 */
```

- [ ] **Step 5: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 14 passed。

- [ ] **Step 6: 提交**

```bash
git add src/styles/theme.css src/styles/hud.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): lift interactive cards, mark hovered rows

A hovered table row now grows an accent bar on its left edge, as an inset
shadow rather than a border-left — a border pushes the row's content 2px right
and the text jumps under the cursor. Same reasoning as .rail-item.on.

Only the three genuinely clickable card kinds lift. Cards floating everywhere
reads cheap, and 'can I click this' is the only question hover has to answer.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 按钮渐变、按下态与焦点光晕

**Files:**
- Modify: `frontend/src/styles/theme.css:367-382`（`.c-btn` 段）
- Modify: `frontend/src/styles/base.css:47-50`（`:focus-visible`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--accent`、`--glow-accent`、`--glow-sm`、`--accent-subtle-border`、`--focus-ring`。
- Produces: 无新符号。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 按钮与焦点', () => {
  test('主按钮是 azure→cyan 渐变,不是实色', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login-submit', 'background-image')
    expect(bg).toContain('gradient')
    // 设计系统明令禁止 azure→violet。紫色的 red 通道会明显高于 blue 通道之外
    // 还带出 red,这里只做一件事:确认渐变里没有出现紫。
    expect(bg).not.toMatch(/rgb\(\s*(1[0-9]{2}|[6-9][0-9])\s*,\s*[0-9]{1,2}\s*,\s*2[0-9]{2}/)
  })

  test('按钮按下时下沉', async ({ page }) => {
    // 用 /audit 不用 /dashboard:总览页自己一个 .c-btn 都没有,唯一可能的来源是
    // ErrorState 的重试按钮,而兜底桩不制造错误 —— 元素根本不存在。审计页头的
    // 风险筛选按钮(audit/index.tsx:212)是常驻控件,不依赖数据。
    await open(page, '/audit')
    await page.waitForSelector('.c-btn')
    const btn = page.locator('.c-btn').first()
    const box = (await btn.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    const t = await btn.evaluate((el) => getComputedStyle(el).transform)
    await page.mouse.up()
    // translateY(1px) scale(.99) → 一个非 none 的矩阵
    expect(t).not.toBe('none')
  })

  test('键盘焦点除 outline 外还有光晕', async ({ page }) => {
    await page.goto('/login')
    // 必须用真键盘走到它,不能用 .focus():Chrome 对按钮的程序化聚焦
    // **不**匹配 :focus-visible,而这条规则正是挂在 :focus-visible 上的。
    // 也不能拿输入框做靶子:theme.css:444 的 .login-card input:focus 写了
    // outline: none,特异度 (0,2,1) 压过 :focus-visible 的 (0,1,0),
    // 输入框上根本不该有 outline。
    for (let i = 0; i < 15; i++) {
      await page.keyboard.press('Tab')
      if (await page.locator('.login-submit:focus').count()) break
    }
    const cs = await page.locator('.login-submit').evaluate((el) => {
      const s = getComputedStyle(el)
      return { fv: el.matches(':focus-visible'), outline: s.outlineWidth, shadow: s.boxShadow }
    })
    expect(cs.fv).toBe(true)
    // outline 保留不动 —— 它是键盘可达性的底线,光晕只是叠加。
    expect(parseFloat(cs.outline)).toBeGreaterThan(0)
    expect(cs.shadow).not.toBe('none')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 按钮与焦点" --reporter=list`
Expected: 三条全 FAIL。

- [ ] **Step 3: 改按钮**

`theme.css` 的 `.c-btn` 段，把这四行

```css
.c-btn:hover:not(:disabled) { border-color: var(--accent-subtle-border); color: var(--accent-text); }
.c-btn.v-primary { height: 40px; border-color: transparent; background: var(--accent); color: var(--text-on-accent); }
.c-btn.v-primary:hover:not(:disabled) { background: var(--accent-hover); color: var(--text-on-accent); }
.c-btn.v-danger { border-color: transparent; background: var(--danger-subtle); color: var(--danger-text); }
```

改为：

```css
.c-btn:hover:not(:disabled) {
  border-color: var(--accent-subtle-border); color: var(--accent-text);
  box-shadow: inset 0 0 0 1px var(--accent-subtle-border);
}
/* 渐变只走 azure→cyan,cyan 混入 30% —— 设计系统禁止 azure→violet。
   混得再多主按钮就偏绿,和"成功"的语义色撞上。 */
.c-btn.v-primary {
  height: 40px; border-color: transparent; color: var(--text-on-accent);
  background: linear-gradient(120deg,
    var(--accent), color-mix(in oklch, var(--glow-accent) 30%, var(--accent)));
}
.c-btn.v-primary:hover:not(:disabled) {
  color: var(--text-on-accent);
  background: linear-gradient(120deg,
    var(--accent-hover), color-mix(in oklch, var(--glow-accent) 34%, var(--accent-hover)));
  box-shadow: var(--glow-sm);
}
/* 一直缺的按下态。设计系统:press → translateY(1px) scale(.99),一个很轻的
   物理下压。transition 时长已被 motion token 统一收口,reduce 下自动退化。 */
.c-btn:active:not(:disabled) { transform: translateY(1px) scale(.99); }
.c-btn.v-danger { border-color: transparent; background: var(--danger-subtle); color: var(--danger-text); }
```

登录页的提交按钮不是 `.c-btn`，单独改。`theme.css` 登录段：

```css
.login-submit {
  width: 100%; margin-top: var(--space-5, 20px); padding: 11px; border: 0; cursor: pointer;
  border-radius: var(--radius-md); background: var(--accent); color: var(--text-on-accent);
  font: 600 14px var(--font-body);
}
.login-submit:hover { background: var(--accent-hover); }
```

改为：

```css
.login-submit {
  width: 100%; margin-top: var(--space-5, 20px); padding: 11px; border: 0; cursor: pointer;
  border-radius: var(--radius-md); color: var(--text-on-accent);
  background: linear-gradient(120deg,
    var(--accent), color-mix(in oklch, var(--glow-accent) 30%, var(--accent)));
  font: 600 14px var(--font-body);
  transition: var(--transition-colors), var(--transition-transform);
}
.login-submit:hover {
  background: linear-gradient(120deg,
    var(--accent-hover), color-mix(in oklch, var(--glow-accent) 34%, var(--accent-hover)));
  box-shadow: var(--glow-sm);
}
.login-submit:active:not(:disabled) { transform: translateY(1px) scale(.99); }
```

- [ ] **Step 4: 改焦点态**

`base.css` 的

```css
:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
```

改为：

```css
/* outline 保留不动 —— 它是键盘可达性的底线。光晕只是叠加在外面的一圈,
   让焦点在深色页面上也一眼看得到。--focus-ring 亮暗各有值(见 colors.css)。

   注意:光晕是 box-shadow,所以任何特异度更高、又自己设了 box-shadow 的组件
   规则都会把它整条替换掉 —— .rail-item.on (0,2,0)、.c-btn:hover:not(:disabled)
   (0,3,1)、.portal-opt.on 都是。这是可接受的:outline 在那些元素上仍然在,
   可达性不受影响,丢的只是装饰。不要为了追平这一圈去给每个组件规则手动
   append 光晕 —— 那是一张永远补不完的表。 */
:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
  box-shadow: 0 0 0 4px var(--focus-ring);
}
```

- [ ] **Step 5: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 17 passed。

注意：`.login-card input:focus` 原本就设了 `box-shadow: var(--focus-ring)`（`theme.css` 登录段）。它是 `:focus` 不是 `:focus-visible`，两条规则同时命中时后者（`base.css` 先 import，`theme.css` 的规则在后）会赢 —— 输入框的光晕仍然存在，测试只断言 `boxShadow !== 'none'`，两种情况都通过。不要为此去删任何一条。

- [ ] **Step 6: 提交**

```bash
git add src/styles/theme.css src/styles/base.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): brand gradient on primary buttons, press state, focus halo

The design system asks for an azure-to-cyan primary fill, a glow on hover, and
a 1px physical push on press. None of the three existed. Cyan is mixed in at
30%: more and the button drifts green, colliding with the success semantics.

:focus-visible keeps its outline — that is the keyboard accessibility floor —
and gains a --focus-ring halo around it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 徽标描边与活体脉冲

**Files:**
- Modify: `frontend/src/styles/theme.css:313-320`（`.c-badge` 段）
- Modify: `frontend/src/styles/hud.css`（追加脉冲段）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--glow-accent`。
- Produces: `@keyframes hud-pulse` —— 后续任务不再定义同名动画。

- [ ] **Step 1: 写失败的测试**

**先做一步重构**：`AUDIT_ROWS` 与 `openAudit()` 现在定义在 Task 3 的
`test.describe('HUD 交互态')` 块内部，本任务是第二个消费者。把这两个声明原样上提到
模块作用域（紧跟 `styleOf()` 之后），两个 describe 块都从那里取。只移动，不改内容 ——
Task 3 的四条测试的行为必须一个字都不变。

然后在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 活体状态', () => {
  test('健康指示灯在脉冲', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.pill-health')
    const name = await styleOf(page, '.pill-health svg', 'animation-name')
    expect(name).toBe('hud-pulse')
  })

  test('reduce 下脉冲塌到 0', async ({ browser }) => {
    const ctx = await browser.newContext({ reducedMotion: 'reduce' })
    const page = await ctx.newPage()
    await open(page, '/dashboard')
    await page.waitForSelector('.pill-health')
    const name = await styleOf(page, '.pill-health svg', 'animation-name')
    await ctx.close()
    // e2e/reduced-motion.spec.ts 守的是同一条线:新动效必须能被 reduce 关掉。
    expect(name).toBe('none')
  })

  test('徽标有描边,不只是一块淡底', async ({ page }) => {
    // 不能用 /dashboard:总览页的每个 <Badge> 都在 rows.map() 里,兜底桩返回
    // 空数组,一个 .c-badge 都不渲染。审计表每行都有一枚(result 列),而
    // openAudit() 已经在喂数据了。
    await openAudit(page)
    await page.waitForSelector('.c-badge')
    expect(parseFloat(await styleOf(page, '.c-badge', 'border-top-width'))).toBeGreaterThan(0)
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 活体状态" --reporter=list`
Expected: 三条全 FAIL（`animation-name` 是 `none`、`border-top-width` 是 0）。

注意第二条此刻也是 FAIL —— 它期望 `none`，而当前值确实是 `none`，所以它会**假绿**。这是可接受的：它的价值在 Step 4 加完动画之后才体现。若要看到它真的红，可临时把断言改成 `expect(name).toBe('hud-pulse')` 确认 reduce 分支被覆盖后再改回。

- [ ] **Step 3: 徽标描边（骨架层）**

`theme.css` 的 `.c-badge` 规则：

```css
.c-badge {
  display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 10px;
  border-radius: 999px; white-space: nowrap; font: 600 11px var(--font-mono);
  background: var(--surface-sunken); color: var(--text-muted);
}
```

改为（只加 border 一行，高度不变 —— `box-sizing: border-box` 在 `theme.css:12` 全局生效，1px 边框不会把 22px 顶高）：

```css
.c-badge {
  display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 10px;
  border-radius: 999px; white-space: nowrap; font: 600 11px var(--font-mono);
  background: var(--surface-sunken); color: var(--text-muted);
  /* 描边取自身文字色的 28%,不是实心框 —— 各 tone 的 color 不同,描边跟着走,
     不用为五个 tone 各写一条。全局 box-sizing: border-box,22px 不被顶高。 */
  border: 1px solid color-mix(in oklch, currentColor 28%, transparent);
}
```

- [ ] **Step 4: 脉冲（装饰层）**

在 `hud.css` 末尾追加：

```css
/* ---- 活体状态 ---- */

/* 脉冲只给"系统健康"这一枚。全站到处闪等于没有重点,而它是唯一表示
   "网关此刻在跑"的指示灯。 */
@keyframes hud-pulse {
  0%, 100% { filter: drop-shadow(0 0 2px var(--glow-accent)); opacity: .85; }
  50%      { filter: drop-shadow(0 0 7px var(--glow-accent)); opacity: 1; }
}
.pill-health svg {
  animation: hud-pulse 2.4s var(--ease-in-out) infinite;
}
@media (prefers-reduced-motion: reduce) {
  /* 关掉动画但把辉光留在它的静止态 —— 不动不等于不亮。 */
  .pill-health svg {
    animation: none;
    filter: drop-shadow(0 0 4px var(--glow-accent));
  }
}
```

- [ ] **Step 5: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 21 passed。

- [ ] **Step 6: 跑动效回归**

Run: `npx playwright test e2e/reduced-motion.spec.ts --reporter=list`
Expected: 全绿。

- [ ] **Step 7: 提交**

```bash
git add src/styles/theme.css src/styles/hud.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): badge outline and a live pulse on the health pill

Badges were a tinted fill with no edge; they now take a hairline mixed from
their own text color, so all five tones follow without five extra rules.

The pulse goes on the gateway health dot and nowhere else. Something blinking
on every screen is the same as nothing blinking, and this is the one light
that means 'the gateway is answering right now'. Under prefers-reduced-motion
the animation stops but the glow stays at rest — still is not the same as dark.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: 外壳 —— 顶栏模糊、侧栏渐变边、选中项辉光

**Files:**
- Modify: `frontend/src/styles/theme.css:176-182`（`.top`）、`:53-68`（`.rail`）、`:143-150`（`.rail-item.on`）
- Modify: `frontend/src/styles/hud.css`（追加侧栏边线）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--surface-card`、`--blur-sm`、`--hud-edge`、`--accent`。
- Produces: 无新符号。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 外壳', () => {
  test('顶栏是半透明 + 背景模糊', async ({ page }) => {
    await open(page, '/dashboard')
    const f = await styleOf(page, '.top', 'backdrop-filter')
    expect(f).toContain('blur')
  })

  test('侧栏边缘是渐变竖线,不是一条等浓的灰线', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.rail', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.rail', 'border-right-width')).toBe('0px')
    // 不能用伪元素:.rail 是 overflow-y:auto 的滚动容器,绝对定位的后代会跟着
    // 内容滚 —— 菜单项一多,这条线就滚出可视区了。同 .c-table 那条。
    expect(await styleOf(page, '.rail', 'background-image', '::after')).toBe('none')
  })

  test('侧栏边线不参与布局,不挤窄菜单项', async ({ page }) => {
    await open(page, '/dashboard')
    const w = await page.locator('.rail').evaluate((el) => el.getBoundingClientRect().width)
    // 74px 是写死的栏宽(theme.css 的注释解释了为什么是 74)。装饰不能改它。
    expect(w).toBeCloseTo(74, 0)
  })

  test('侧栏选中项在色条之外还有外溢辉光', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.rail-item.on')
    const s = await styleOf(page, '.rail-item.on', 'box-shadow')
    // 既有的 inset 色条 + 新加的外溢辉光 = 两段阴影。数颜色函数的个数,
    // 不数逗号 —— 一段阴影自己就带好几个逗号。
    expect(s).toContain('inset')
    expect((s.match(/rgba?\(/g) ?? []).length).toBeGreaterThanOrEqual(2)
    // 外溢那一段不能也是 inset,否则辉光画在里面,外面看不见。
    expect(s.replace(/inset/, '')).not.toContain('inset')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 外壳" --reporter=list`
Expected: 第 1、2、4 条 FAIL；第 3 条已绿（护栏）。

- [ ] **Step 3: 顶栏（骨架层）**

`theme.css` 的 `.top` 规则：

```css
.top {
  height: 54px; flex: 0 0 54px;
  display: flex; align-items: center; gap: 14px; padding: 0 22px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-card);
}
```

改为：

```css
.top {
  height: 54px; flex: 0 0 54px;
  display: flex; align-items: center; gap: 14px; padding: 0 22px;
  border-bottom: 1px solid var(--border-subtle);
  /* 设计系统对顶栏的原文要求:半透明 + backdrop blur。实色顶栏把下面的
     HUD 底座切断了,页面看着像两张拼起来的图。 */
  background: color-mix(in oklch, var(--surface-card) 78%, transparent);
  backdrop-filter: blur(8px);
}
```

- [ ] **Step 4: 侧栏边线（骨架层：去 border，改 background-image）**

`theme.css` 的 `.rail` 规则，把最后一行

```css
  border-right: 1px solid var(--border-subtle);
```

删掉（其余原样保留，包括上面那两段解释 padding 与 overflow 的注释）。

这条线走 `background-image`，**不是伪元素** —— 理由和 `.c-table` 完全一样：
`.rail` 是 `overflow-y: auto` 的滚动容器（菜单项多于视口高度时要滚，见该处注释），
绝对定位的后代会跟着内容一起滚，菜单一长这条线就滑出可视区了。
`background-attachment` 默认 `scroll`，定位锚在元素自己的边框盒上，不随内容滚动。

所以改的是 `theme.css` 里 `.rail` 那条规则本身（骨架层），不是往 `hud.css` 加伪元素。
把 `.rail` 的

```css
  background: var(--surface-sunken);
  border-right: 1px solid var(--border-subtle);
```

替换为

```css
  /* 底色拆成 background-color:下面的 background-image 会把 background 简写
     里的颜色一并清掉。 */
  background-color: var(--surface-sunken);
  /* 一条从头到尾一样浓的灰线是"隔断",渐变线才是"边缘"。
     不用伪元素:.rail 会滚(见上面那段 overflow-y 的注释),绝对定位的后代跟着
     内容滚,菜单一长线就滑走了 —— 同 .c-table 的做法。
     border-right 必须去掉,不能只是盖住:74px 的栏宽是算过的(见 .rail-item
     的注释:64px 项 + 两边各 5px),多 1px 边框就是 75px。 */
  background-image: linear-gradient(180deg,
    transparent,
    var(--border-subtle) 18%,
    color-mix(in oklch, var(--hud-hairline) 60%, transparent) 50%,
    var(--border-subtle) 82%,
    transparent);
  background-repeat: no-repeat;
  background-size: 1px 100%;
  background-position: 100% 0;
```

注意 `border-right: 1px solid var(--border-subtle)` 在 `theme.css` 里出现四次
（第 69 行的 `.rail`、2131 的 `.tv-rail.left`、2316、2481）。**只动第 69 行那条**，
其余三处属于终端页的分栏，与本轮无关。

- [ ] **Step 5: 选中项辉光（骨架层）**

`theme.css` 的 `.rail-item.on` 规则，把

```css
  box-shadow: inset 3px 0 0 var(--accent);
```

改为

```css
  /* 外溢那一段是给暗色的:淡背景在深色上和 hover 几乎分不出来,色条又太细。 */
  box-shadow: inset 3px 0 0 var(--accent), 0 0 16px -6px var(--accent);
```

- [ ] **Step 6: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 25 passed。

- [ ] **Step 7: 跑侧栏回归 —— 这一步不能跳**

Run: `npx playwright test e2e/responsive.spec.ts --reporter=list`
Expected: 全绿，特别是 `e2e/responsive.spec.ts:206` 的「左侧栏装不下时滚动，不把 logo 和菜单项压扁」。`.rail` 里有两处 sticky 元素（`.portal-sw`、`.rail-foot`）靠外扩的 `box-shadow` 补缝，改 `border-right` 为伪元素后要确认它们仍然盖得住。若这条红，最可能的原因是 `.rail` 的宽度变了 —— 检查 `border-right` 是否真的删干净。

- [ ] **Step 8: 提交**

```bash
git add src/styles/theme.css src/styles/hud.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): translucent top bar, gradient rail edge, glow on the active item

The top bar was an opaque fill that cut the HUD bed in half, so the page read
as two images stitched together. It now does what the design system asked for
all along: 78% surface plus an 8px backdrop blur.

The rail's border-right becomes an absolutely positioned gradient line — a
line of even weight is a divider, a gradient is an edge. The border has to go
rather than stay: 74px is a computed width (64px item plus 5px each side), and
a 1px border would make it 75.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: 指标数字渐变裁字

**Files:**
- Modify: `frontend/src/styles/theme.css:1509`（`.dash-nums b`）、`:1445`（`.us-statv`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--text-strong`、`--accent-text`、`--warning-text`。
- Produces: 无新符号。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 指标数字', () => {
  test('普通指标数字是渐变裁字', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.dash-nums b')
    const cs = await page.locator('.dash-nums div:not(.warn) > b').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { clip: s.backgroundClip || s.webkitBackgroundClip, color: s.color, bg: s.backgroundImage }
    })
    expect(cs.clip).toBe('text')
    expect(cs.bg).toContain('gradient')
    // 裁字要求文字本身透明,否则实色盖在渐变上,渐变白画。
    expect(cs.color).toBe('rgba(0, 0, 0, 0)')
  })

  test('告警数字不裁字 —— 语义色不能被渐变冲淡', async ({ page }) => {
    await open(page, '/dashboard')
    // open() 把 intercepts 打成 2,GatewayCard 因此渲染出一个 .warn。
    await page.waitForSelector('.dash-nums .warn b')
    const cs = await page.locator('.dash-nums .warn > b').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { clip: s.backgroundClip || s.webkitBackgroundClip, color: s.color }
    })
    expect(cs.clip).not.toBe('text')
    expect(cs.color).not.toBe('rgba(0, 0, 0, 0)')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 指标数字" --reporter=list`
Expected: 第 1 条 FAIL（`clip` 是 `border-box`）；第 2 条已绿（护栏）。

- [ ] **Step 3: 改数字样式**

`theme.css` 的

```css
.dash-nums b { font: 700 22px var(--font-display); color: var(--text-strong); letter-spacing: -.02em; }
```

改为：

```css
/* 渐变幅度刻意小(strong → accent-text,不是 azure → cyan):大数字是要读的,
   不是要发光的。设计系统允许 big-stat numerals 用 background-clip:text,
   这里就用在它规定的位置上。 */
.dash-nums b {
  font: 700 22px var(--font-display); letter-spacing: -.02em;
  background: linear-gradient(135deg, var(--text-strong), var(--accent-text));
  -webkit-background-clip: text; background-clip: text;
  color: transparent;
}
/* 单位后缀(.dash-nums b i,如 "ms")不跟着裁 —— 它是 11px 的 mono,裁字会
   把这么小的字号啃掉笔画。 */
.dash-nums b i {
  background: none; -webkit-background-clip: border-box; background-clip: border-box;
}
```

紧接着的 `.dash-nums b i` 原有规则（`font: 500 11px var(--font-mono); ... color: var(--text-faint);`）保持在**新加的这条之后**，让 `color: var(--text-faint)` 生效覆盖掉继承来的 `transparent`。若顺序反了，单位后缀会变成透明。

`.dash-nums .warn b` 原规则：

```css
.dash-nums .warn b { color: var(--warning-text); }
```

改为：

```css
/* 告警数字不裁字:语义色是它唯一要传达的东西,不能被渐变冲淡。 */
.dash-nums .warn b {
  background: none; -webkit-background-clip: border-box; background-clip: border-box;
  color: var(--warning-text);
}
```

`.us-statv`（`/users` 页的统计数字）同样处理：

```css
.us-statv { font: 700 22px var(--font-display); color: var(--text-strong); }
```

改为

```css
.us-statv {
  font: 700 22px var(--font-display);
  background: linear-gradient(135deg, var(--text-strong), var(--accent-text));
  -webkit-background-clip: text; background-clip: text;
  color: transparent;
}
```

并把它下面的 `.us-stat.warning .us-statv { color: var(--warning-text); }` 改为

```css
.us-stat.warning .us-statv {
  background: none; -webkit-background-clip: border-box; background-clip: border-box;
  color: var(--warning-text);
}
```

- [ ] **Step 4: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 27 passed。

- [ ] **Step 5: 提交**

```bash
git add src/styles/theme.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): gradient-clip the stat numerals

The design system calls for gradient-clipped big-stat numerals; the overview
and the user stats rendered them in flat --text-strong. The ramp is kept short
on purpose — strong to accent-text, not azure to cyan. These numbers are there
to be read, not to glow.

Warning numerals opt out: the semantic color is the only thing they carry, and
a gradient would dilute it. So does the unit suffix — an 11px mono 'ms' loses
strokes to text clipping.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: 登录页

**Files:**
- Modify: `frontend/src/pages/login/index.tsx:65`（`.login-card` 内加一个装饰元素）
- Modify: `frontend/src/styles/theme.css`（登录段的 `.login-card`、`.login-mark`）
- Modify: `frontend/src/styles/hud.css`（追加 `.hud-corners`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--hud-corner`、`--hud-corner-size`、`--hud-edge`、`--blur-sm`。
- Produces: CSS 类 `.hud-corners` —— 一个纯装饰的四角取景框，任何需要它的容器只要是 `position: relative` 就能挂。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 登录页', () => {
  test('登录卡有四角取景框,且对辅助技术隐藏、不吃点击', async ({ page }) => {
    await page.goto('/login')
    const el = page.locator('.login-card .hud-corners')
    await expect(el).toHaveCount(1)
    await expect(el).toHaveAttribute('aria-hidden', 'true')
    expect(await styleOf(page, '.hud-corners', 'pointer-events')).toBe('none')
    // 四个角:自身画两个(上),两个伪元素各画一个(下)。
    expect(parseFloat(await styleOf(page, '.hud-corners', 'border-left-width', '::before'))).toBeGreaterThan(0)
    expect(parseFloat(await styleOf(page, '.hud-corners', 'border-right-width', '::after'))).toBeGreaterThan(0)
  })

  test('装饰元素不改变登录卡尺寸', async ({ page }) => {
    await page.goto('/login')
    const w = await page.locator('.login-card').evaluate((el) => el.getBoundingClientRect().width)
    // 380px 是写死的卡宽。取景框是绝对定位,不能把它顶宽。
    expect(w).toBeCloseTo(380, 0)
  })

  test('登录页品牌标与侧栏 logo 用同一组渐变', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login-mark', 'background-image')
    expect(bg).toContain('gradient')
    // #3b6ef6 → rgb(59, 110, 246);#2dcde6 → rgb(45, 205, 230)
    expect(bg).toContain('59, 110, 246')
    expect(bg).toContain('45, 205, 230')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "HUD 登录页" --reporter=list`
Expected: 第 1、3 条 FAIL；第 2 条已绿（护栏）。

- [ ] **Step 3: 加装饰元素**

`frontend/src/pages/login/index.tsx`，把

```tsx
      <form className="login-card" onSubmit={submit}>
        <div className="login-brand">
```

改为

```tsx
      <form className="login-card" onSubmit={submit}>
        {/* 四角取景框。伪元素只够画两个角,而登录页是第一印象页,四角完整的
            取景框值这一个装饰元素 —— 这是全站唯一为装饰加的 DOM。 */}
        <span className="hud-corners" aria-hidden="true" />
        <div className="login-brand">
```

- [ ] **Step 4: 画取景框（装饰层）**

在 `hud.css` 末尾追加：

```css
/* ---- 四角取景框 ---- */

/* 挂在任何 position: relative 的容器里。自身画上面两个角(用两层
   background-image 定位到左上/右上),两个伪元素各画下面一个。
   全程绝对定位 + pointer-events:none —— 不进布局,不吃点击。 */
.hud-corners {
  position: absolute; inset: 10px; pointer-events: none;
  background-repeat: no-repeat;
  background-size: var(--hud-corner-size) var(--hud-corner-size);
  background-position: left top, right top;
  background-image:
    linear-gradient(to right,  var(--hud-corner) 1px, transparent 1px),
    linear-gradient(to left,   var(--hud-corner) 1px, transparent 1px);
}
/* 上面那两层只画了水平的一划;竖划与下面两个角交给伪元素,它们各自是一个
   真正的 L(两条 border),画起来比再堆四层背景干净。 */
.hud-corners::before,
.hud-corners::after {
  content: ''; position: absolute; bottom: 0;
  width: var(--hud-corner-size); height: var(--hud-corner-size);
}
.hud-corners::before {
  left: 0; border-left: 1px solid var(--hud-corner); border-bottom: 1px solid var(--hud-corner);
}
.hud-corners::after {
  right: 0; border-right: 1px solid var(--hud-corner); border-bottom: 1px solid var(--hud-corner);
}
```

- [ ] **Step 5: 登录卡与品牌标（骨架层）**

`theme.css` 登录段的 `.login-card`：

```css
.login-card {
  width: 380px; padding: var(--space-7, 28px);
  border-radius: var(--radius-2xl); background: var(--surface-card);
  border: 1px solid var(--border-subtle); box-shadow: var(--shadow-lg);
}
```

改为：

```css
.login-card {
  position: relative; /* .hud-corners 的定位上下文 */
  width: 380px; padding: var(--space-7, 28px);
  border-radius: var(--radius-2xl);
  /* 半透明 + 模糊,让底座的网格从卡片下面透出来一点 —— 实色卡片扣在网格上
     像贴纸,透一点才像悬在上面。 */
  background: color-mix(in oklch, var(--surface-card) 88%, transparent);
  backdrop-filter: blur(var(--blur-sm));
  border: 1px solid var(--border-subtle); box-shadow: var(--shadow-lg);
  overflow: hidden;
}
/* 顶沿高光,同卡片。 */
.login-card::before {
  content: ''; position: absolute; top: 0; left: 0; right: 0; height: 1px;
  pointer-events: none; background: var(--hud-edge);
}
```

`.login-mark`：

```css
.login-mark {
  width: 30px; height: 30px; border-radius: var(--radius-md); display: grid; place-items: center;
  background: var(--accent); color: var(--text-on-accent);
}
```

改为：

```css
.login-mark {
  width: 30px; height: 30px; border-radius: var(--radius-md); display: grid; place-items: center;
  color: #fff;
  /* 与 .rail-logo 完全一致的两个色标 —— 同一个品牌标在登录页和侧栏不该长得
     不一样。token 表里没有对应物,原型里也是写死的。 */
  background: linear-gradient(135deg, #3b6ef6, #2dcde6);
  box-shadow: 0 0 18px -4px rgba(45, 205, 230, .6);
}
```

- [ ] **Step 6: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 30 passed。

- [ ] **Step 7: 类型检查与构建**

Run: `npm run build`
Expected: 成功。这是本阶段唯一改 TSX 的任务，必须过 `tsc --noEmit`。

- [ ] **Step 8: 提交**

```bash
git add src/pages/login/index.tsx src/styles/theme.css src/styles/hud.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): HUD treatment for the login screen

The login card gets a four-corner frame, a top hairline, and a translucent
fill so the grid bed shows through — an opaque card on a grid reads as a
sticker, a translucent one reads as hovering above it.

The frame is the one decorative DOM node in the whole console: two pseudo
elements only reach two corners, and login is the first impression. The brand
mark now uses the same two stops as .rail-logo; the same mark should not look
different in two places.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: 全量回归与人工验收

本任务不写新代码。它存在的理由：前八个任务各自只跑了相关的那几个 spec，而装饰层是**全局**的——它可能在任何一个没被针对性测到的页面上出问题。

**Files:**
- Modify: 仅在发现问题时回改对应文件
- Test: 全部

**Interfaces:**
- Consumes: Task 1–8 的全部产出。
- Produces: 四张验收截图 + 一份结论。

- [ ] **Step 1: 类型检查与构建**

Run: `npm run build`
Expected: 成功，无 TS 错误。

- [ ] **Step 2: 单元测试**

Run: `npm run test:unit`
Expected: 全绿。本阶段没碰 `src/lib/*`，若有红说明改错了文件。

- [ ] **Step 3: 全量 e2e**

Run: `npm run test:e2e`
Expected: 全绿。重点看三个：

| spec | 它守的东西 |
|---|---|
| `responsive.spec.ts` | 390/768/1024/1280/1440 五档下无页面级横向溢出；侧栏装不下时滚动而不压扁 |
| `reduced-motion.spec.ts` | 新增的 `hud-pulse` 能被 reduce 关掉 |
| `hud-visual.spec.ts` | 本阶段的 30 条 |

若 `responsive.spec.ts` 红：几乎一定是某处装饰用了 `border`/`padding`/`margin` 而不是绝对定位，或 `.rail` 的 `border-right` 没删干净。按报错的宽度档去查对应元素的 `getBoundingClientRect()`。

- [ ] **Step 4: 跑起真应用**

用 `run-app` skill 启动（Go 后端 :8080 + Vite :5173，网关自身的库是 PostgreSQL，需先 `createdb vela_gateway`），用 `linwei@vela.io / vela123` 登录。

- [ ] **Step 5: 截四张图**

亮/暗 × 登录页/总览页，共四张。主题在账户菜单里切（`stores/ui.ts` 把它写到 `<html data-theme>`）。

- [ ] **Step 6: 逐条对照验收标准**

对着截图核对，四条都要过：

1. **亮色下网格与辉光"看得出底座、看不出色斑"** —— 把视线移开时还像白底。若看见明显的蓝斑，把 `--hud-glow-a/b` 从 .07/.06 往下调，不要调网格。
2. **暗色下卡片靠高光线与角标分层，不靠投影** —— 设计系统：dark mode drops drop-shadows。若暗色卡片看着有黑边阴影，检查 Task 3 的暗色分支是否真的用 `--glow-sm` 覆盖掉了 `--shadow-lg`。
3. **总览页的指标数字仍然一眼可读** —— 渐变没有把它冲淡，单位后缀 "ms" 没有变透明。若后缀不见了，是 Task 7 的 `.dash-nums b i` 规则顺序反了。
4. **表格行 hover 的左竖条出现时，行内文字没有横向位移** —— 这条 `hud-visual.spec.ts` 已经自动测了，但用眼睛再确认一次：把鼠标在几行之间上下移动，文字不该抖。

- [ ] **Step 7: 提交截图之外的任何修正**

如果第 6 步调了数值：

```bash
git add -A
git commit -m "fix(ui): tune HUD glow after visual review

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 8: 汇报**

把四张截图和逐条结论给用户。**第一阶段到此为止** —— 剩下 20 页的页面级对齐是第二阶段，需要用户看过截图、确认共用层的规则站得住之后另起计划。

特别要在汇报里点名的：如果总览页看起来还是平的，**回去改共用层，不要给总览写特例**。总览这一页在本阶段的作用就是验证共用层够不够。

---

## 第二阶段预告（不在本计划内）

本计划只覆盖 spec 第六节的第一阶段。第二阶段要处理的是那些**自建了卡片外观而没复用 `.c-card`** 的容器，它们不会自动吃到 Task 2 的装饰：

`.perm-roles`、`.us-stat`、`.gov-previewbox`、`.perm-toggle`、`.perm-menucard`、连接页/终端页/脚本页的各类面板。

第二阶段另起 spec 与计划，前提是第一阶段的四张验收截图通过。
