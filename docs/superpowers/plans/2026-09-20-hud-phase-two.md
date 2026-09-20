# HUD 第二阶段实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把第一阶段的 HUD 装饰铺到那 18 个自建了卡片外观、却没复用 `.c-card` / `.c-table` / `.dash-card` 的容器上，按"面板 / 列表行 / 仪器"三档区别对待。

**Architecture:** 扩展 `src/styles/hud.css` 的既有选择器列表，不新建机制。骨架层 `theme.css` 只补各容器缺的 `position: relative`。唯一新增的 DOM 是终端外框的一个装饰节点，复用登录页已有的 `.hud-corners` 类。

**Tech Stack:** 纯 CSS + 一行 TSX。测试用 Playwright e2e（真浏览器，API 由 spec 自行打桩，Go 后端无需运行）。

**Spec:** `docs/superpowers/specs/2026-09-20-hud-phase-two-design.md`

## Global Constraints

- **不动布局尺寸。** 栏宽、行距、断点、内距、列模板一律不改。
- **不动文字色与语义状态色 token**（`--text-*`、`--success/warning/danger*`）。
- **装饰一律 `pointer-events: none`，不改变盒模型。** `e2e/responsive.spec.ts` 断言 390/768/1024/1280/1440 五档无页面级横向溢出，并单独守着侧栏滚动与终端三档栏宽。
- **新增动画必须在 `prefers-reduced-motion` 下塌到 0。**（本轮无新动画。）
- **渐变只走 azure→cyan。**
- **语义 token，不写死色值。**
- 不加依赖、不改 i18n、不加斑马线、不碰 `.ib-empty`、不碰 `.scan-nums`。
- 所有新测试追加到 `frontend/e2e/hud-visual.spec.ts`，复用模块作用域助手 `open(page, path)`（传 `'about:blank'` 只装桩不跳页）、`setTheme`、`styleOf`（async，先等选择器）、`AUDIT_ROWS` / `openAudit`。
- 命令都在 `frontend/` 下执行。当前基线：build 干净、291 单测、**78** e2e。

---

## File Structure

| 文件 | 动作 |
|---|---|
| `frontend/src/styles/hud.css` | 扩展选择器列表 + 新增列表行、终端两段 |
| `frontend/src/styles/theme.css` | 补 `position: relative`；`.ib-side` 改 `background-image`；两处媒体查询 `static`→`relative` |
| `frontend/src/pages/terminal/index.tsx` | 一行：终端外框的装饰节点 |
| `frontend/e2e/hud-visual.spec.ts` | 追加各档断言 + 三条结构性守卫 |

---

### Task 1: 面板档 · 伪元素组（11 个容器）

**Files:**
- Modify: `frontend/src/styles/hud.css`（扩展两处选择器列表、订正一处假注释）
- Modify: `frontend/src/styles/theme.css`（补 `position: relative`；两处媒体查询）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: 既有 token `--hud-edge`、`--hud-corner`、`--hud-corner-size`。
- Produces: 面板档的选择器列表 —— Task 2 的 `.ib-side` 明确**不**进这个列表，Task 5 的数量断言按这个列表计数。

- [ ] **Step 1: 写失败的测试**

在 `e2e/hud-visual.spec.ts` 末尾追加：

```ts
test.describe('HUD 第二阶段 · 面板档', () => {
  // 每个容器配一条能把它渲染出来的最小路径。列在一起是因为它们是同一档待遇,
  // 一条断言重复十一遍没有意义 —— 要验的是"这一档的规则挂上了"。
  const PANELS: Array<[string, string]> = [
    ['.perm-roles', '/permissions'],
    ['.set-nav', '/settings'],
    ['.set-save', '/settings'],
    ['.cat-side', '/catalog'],
    ['.cat-detail', '/catalog'],
  ]

  for (const [sel, path] of PANELS) {
    test(`${sel} 拿到顶沿高光线与右上角标`, async ({ page }) => {
      await open(page, path)
      expect(await styleOf(page, sel, 'background-image', '::before')).toContain('gradient')
      expect(await styleOf(page, sel, 'height', '::before')).toBe('1px')
      expect(await styleOf(page, sel, 'pointer-events', '::before')).toBe('none')
      expect(parseFloat(await styleOf(page, sel, 'border-top-width', '::after'))).toBeGreaterThan(0)
      expect(parseFloat(await styleOf(page, sel, 'border-right-width', '::after'))).toBeGreaterThan(0)
      // L 形,不是方框
      expect(parseFloat(await styleOf(page, sel, 'border-left-width', '::after'))).toBe(0)
    })
  }

  test('窄屏下 .set-nav / .cat-side 仍然是装饰的定位祖先', async ({ page }) => {
    await open(page, '/settings')
    await page.setViewportSize({ width: 768, height: 900 })
    // ≤768 的媒体查询把它们从 sticky 解除。解除必须落到 relative,不能落到
    // static —— static 会让 ::before 跑到更外层祖先上定位,高光线就画到别处去了。
    expect(await styleOf(page, '.set-nav', 'position')).toBe('relative')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "面板档" --reporter=list`
Expected: 6 条全 FAIL —— 前五条因为 `background-image` 是 `none`、border 宽度是 0；第六条因为窄屏下解析出 `static`。

- [ ] **Step 3: 订正假注释并扩展高光线列表**

`hud.css` 的这段注释含一句**已知为假**的话 —— 实测 `theme.css` 里有三处 `.on::before`
选中色条，不是一处。把

```css
/* .c-table 不在这组里 —— 它是横滚容器,高光线走 background-image,见 theme.css。
   .perm-rcard 也不在 —— 它的 ::before 被选中态的左色条占着(theme.css 的
   `.perm-rcard.on::before`),是全站唯一的伪元素冲突点。按选择器引,不按行号:
   theme.css 会长,行号一改就成了假话。 */
```

改为

```css
/* 不在这组里的两类:
   一是横滚容器 —— .c-table 与 .ib-side 的高光线走 background-image,见 theme.css。
     绝对定位的后代会跟着内容滚出可视区。
   二是 ::before 已被选中态左色条占用的容器 —— .perm-rcard、.chg-item、.pl-item
     三处都是这个模式(`.on::before` 画一根 3px 竖条)。第一阶段的注释说这是"全站
     唯一"的冲突点,那是错的,实测三处。列表行整档因此改用 ::after,见下面那组。
   按选择器引,不按行号:theme.css 会长,行号一改就成了假话。 */
```

然后把高光线的选择器列表

```css
.c-card::before,
.dash-card::before,
.login-card::before {
```

扩展为

```css
.c-card::before,
.dash-card::before,
.login-card::before,
.perm-roles::before,
.set-nav::before,
.set-save::before,
.cat-side::before,
.cat-detail::before,
.rr-stat::before,
.chg-detail::before,
.pl-editor::before,
.notif-panel::before,
.usermenu::before,
.conn-bulkbar::before {
```

- [ ] **Step 4: 扩展角标列表**

把

```css
.c-card::after,
.dash-card::after {
```

扩展为

```css
.c-card::after,
.dash-card::after,
.perm-roles::after,
.set-nav::after,
.set-save::after,
.cat-side::after,
.cat-detail::after,
.rr-stat::after,
.chg-detail::after,
.pl-editor::after,
.notif-panel::after,
.usermenu::after,
.conn-bulkbar::after {
```

**`.login-card` 不进角标列表** —— 它有自己的四角取景框（`.hud-corners`），再加一个右上角标
会和取景框的右上角重叠成双线。第一阶段就是这么安排的，保持不变。

- [ ] **Step 5: 骨架层补定位上下文**

`theme.css` 里这七个容器没有任何 `position`，绝对定位的装饰会跑到更外层祖先上去。
各加一行 `position: relative;`，并在第一处写明理由（其余六处不必重复同一句注释）：

- `.perm-roles`
- `.set-save`
- `.cat-detail`
- `.rr-stat`
- `.chg-detail`
- `.pl-editor`

第一处的注释：

```css
  position: relative; /* HUD 装饰的定位上下文,见 hud.css */
```

`.notif-panel`（absolute）、`.usermenu`（fixed）、`.conn-bulkbar`（fixed）、
`.set-nav`（sticky）、`.cat-side`（sticky）**已经是定位元素，不要动它们的 `position`**。

- [ ] **Step 6: 修两处窄屏的 static 回退**

`theme.css` 的 `@media (max-width: 768px)` 里：

```css
  .set-nav, .cat-side { position: static; }
```

改为

```css
  /* 解除 sticky 用 relative 不用 static:两者都不粘,但 static 会让 HUD 装饰的
     ::before/::after 跑到更外层祖先上定位 —— 高光线就画到别的盒子上去了。
     relative 不带偏移量,不产生任何位移。 */
  .set-nav, .cat-side { position: relative; }
```

- [ ] **Step 7: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 84 passed（基线 78 + 本任务 6）。

- [ ] **Step 8: 跑既有回归**

Run: `npx playwright test e2e/responsive.spec.ts e2e/permissions-matrix.spec.ts --reporter=list`
Expected: 全绿。装饰都是绝对定位，不进布局；`static`→`relative` 无偏移量，同样不进布局。

- [ ] **Step 9: 提交**

```bash
git add src/styles/hud.css src/styles/theme.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): extend the panel decoration to the bespoke containers

Eleven containers hand-rolled the same hairline, radius and fill as a card
without ever reusing one, so the refresh passed them by. They join the
existing selector lists rather than gaining rules of their own.

Two of them release their sticky positioning at narrow widths by going
static, which would send the decoration off to position against some
further ancestor. Relative releases sticky just as well and keeps the
containing block, with no offset and so no movement.

Also corrects a comment: phase one called .perm-rcard.on::before the only
pseudo-element collision in the codebase. There are three.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `.ib-side` 的 background-image 机制 + 滚动容器普查守卫

**Files:**
- Modify: `frontend/src/styles/theme.css`（`.ib-side`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--hud-edge`。
- Produces: 滚动容器普查断言 —— 这条守的是**一类错误**，后续任务新增容器时它自动生效。

- [ ] **Step 1: 写失败的测试**

追加：

```ts
test.describe('HUD 第二阶段 · 滚动容器', () => {
  test('.ib-side 的高光线走 background-image,不走伪元素', async ({ page }) => {
    await open(page, '/inbox')
    expect(await styleOf(page, '.ib-side', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.ib-side', 'background-image', '::before')).toBe('none')
  })

  // 这条不针对某个容器,针对一类错误:本项目已经在 .c-table 和 .rail 上各犯过
  // 一次"给滚动容器挂绝对定位装饰"。将来任何人把某个装饰过的容器改成可滚,
  // 这条立刻红。
  test('凡是会滚的装饰容器,都不用绝对定位的伪元素承载装饰', async ({ page }) => {
    await open(page, '/inbox')
    const bad = await page.evaluate(() => {
      const out: string[] = []
      for (const el of document.querySelectorAll<HTMLElement>('*')) {
        const cs = getComputedStyle(el)
        const scrolls = ['auto', 'scroll'].includes(cs.overflowX)
          || ['auto', 'scroll'].includes(cs.overflowY)
        if (!scrolls) continue
        for (const pe of ['::before', '::after']) {
          const p = getComputedStyle(el, pe)
          if (p.content === 'none') continue
          if (p.position === 'absolute' && p.backgroundImage !== 'none') {
            out.push(`${el.className || el.tagName}${pe}`)
          }
        }
      }
      return out
    })
    expect(bad).toEqual([])
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "滚动容器" --reporter=list`
Expected: 第一条 FAIL（`.ib-side` 的 `background-image` 是 `none`）。第二条此刻应当**已绿** ——
Task 1 没有把 `.ib-side` 放进伪元素列表，所以还没有违例者；它是守卫，不是新行为。

- [ ] **Step 3: 改 `.ib-side`**

`theme.css` 的 `.ib-side` 规则，把它的 `background: var(--surface-card);`（或等价的简写）
拆开并补上背景图：

```css
  /* 它是 overflow:auto 的滚动容器(见下面那行),绝对定位的伪元素会跟着内容滚出
     可视区 —— 同 .c-table 和 .rail 的理由。background-attachment 默认 scroll,
     锚在元素自己的边框盒上,不随内容滚动。
     底色必须从 background 简写拆成 background-color,否则被 background-image 连带清掉。 */
  background-color: var(--surface-card);
  background-image: var(--hud-edge);
  background-repeat: no-repeat;
  background-size: 100% 1px;
  background-position: 0 0;
```

`.ib-side` 不加角标：单靠 `background-image` 拼一个 L 形要四个背景层，为一个角标不值得，
而顶沿那条线已经把它分出层了 —— 与 `.c-table` 同一个取舍。

**注意** `@media (max-width: 1080px)` 里 `.ib-side` 会变成 `overflow: visible`，
那一档它不再滚动，但 `background-image` 在两档下都成立，不必分档处理。

- [ ] **Step 4: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 86 passed。

- [ ] **Step 5: 提交**

```bash
git add src/styles/theme.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): hairline the inbox sidebar without a pseudo element

The sidebar scrolls, so an absolutely positioned decoration would slide out
of view with its content — the third container in this codebase to hit that,
after the table and the rail. It gets its hairline from background-image,
anchored to its own border box.

The accompanying guard is not about this container. It walks every scrolling
element on the page and fails if any of them carries an absolutely positioned
decoration, so the next one to go scrollable is caught rather than discovered.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 列表行档（4 个）+ 选中态色条普查守卫

**Files:**
- Modify: `frontend/src/styles/hud.css`（新增列表行段）
- Modify: `frontend/src/styles/theme.css`（`.aj-item` / `.osc-item` 补 `position: relative`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: `--hud-edge`、`--shadow-lg`、`--glow-sm`、`--transition-colors`、`--transition-transform`。
- Produces: 列表行档的选择器列表 —— Task 5 的数量断言按它计数。

- [ ] **Step 1: 写失败的测试**

追加：

```ts
test.describe('HUD 第二阶段 · 列表行', () => {
  // 变更工单要有数据才渲染得出 .chg-item。字段名照 types/index.ts 的 Release
  // 抄,不要凭印象写:列表读的是 relNo / changeType / pipelineName,写成 no / stage
  // 之类的行会照常渲染出来但每格是空的,而下面那条 hover 测的是几何,
  // 空格量掉了就等于没测。接口路径是 /releases,不是 /changes。
  const RELEASES = {
    items: [
      {
        id: 1, relNo: 'REL-2026-0001', title: '清理过期订单备注',
        pipelineId: 1, pipelineName: '标准发布', connectionId: 1,
        instance: 'prod-mysql-01', database: 'shop', env: 'prod',
        tierCode: 'prod', engine: 'mysql', changeType: 'dml',
        status: 'pending', creator: 'linwei', createdAt: '2026-09-20T10:00:00Z',
      },
      {
        id: 2, relNo: 'REL-2026-0002', title: '补充索引',
        pipelineId: 1, pipelineName: '标准发布', connectionId: 2,
        instance: 'demo-dev', database: 'shop', env: 'dev',
        tierCode: 'dev', engine: 'mysql', changeType: 'ddl',
        status: 'pending', creator: 'linwei', createdAt: '2026-09-20T10:05:00Z',
      },
    ],
    total: 2, page: 1, pageSize: 50,
  }

  async function openChanges(page: Page) {
    await open(page, 'about:blank')
    await page.route('**/api/v1/releases**', (r) => r.fulfill(envelope(RELEASES)))
    await page.goto('/changes')
    await page.waitForSelector('.chg-item')
  }

  test('列表行的高光线走 ::after,不走 ::before', async ({ page }) => {
    await openChanges(page)
    // ::before 被选中态的 3px 左色条占着 —— 三处之一。
    expect(await styleOf(page, '.chg-item', 'background-image', '::after')).toContain('gradient')
    expect(await styleOf(page, '.chg-item', 'height', '::after')).toBe('1px')
  })

  test('列表行不拿角标', async ({ page }) => {
    await openChanges(page)
    // 角标是 border 画的;高光线是 background 画的。同一个 ::after 上,
    // 有 background 没有 border 才是"只要线不要角"。
    expect(parseFloat(await styleOf(page, '.chg-item', 'border-top-width', '::after'))).toBe(0)
  })

  test('列表行 hover 抬升', async ({ page }) => {
    await openChanges(page)
    const row = page.locator('.chg-item').first()
    expect(await row.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
    await row.hover()
    await page.waitForTimeout(300) // --dur-base 220ms,读的要是落定值
    const t = await row.evaluate((el) => getComputedStyle(el).transform)
    expect(t).toContain('-2')
  })

  // 三处选中态色条 —— 装饰不得覆盖它们。它们是这三个容器上唯一回答
  // "现在选的是哪一个"的东西。
  test('三处选中态色条都还在', async ({ page }) => {
    await open(page, '/permissions')
    await page.waitForSelector('.perm-rcard')
    await page.click('.perm-rcard')
    await page.waitForSelector('.perm-rcard.on')
    expect(await styleOf(page, '.perm-rcard.on', 'width', '::before')).toBe('3px')
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "列表行" --reporter=list`
Expected: 前三条 FAIL。第四条应当**已绿**（回归守卫）。

若第一条报的是 `no element for .chg-item` 而不是断言失败，先查两件事：接口路径是不是
`/releases`（`api/modules/pipeline.ts` 的 `releasesQueryOptions`），以及桩的字段名是不是
`types/index.ts:760` 的 `Release`。**停下来按真实类型改桩，不要改断言** —— 第一阶段在审计页
上因为编造字段名，让一条几何断言量了两行空单元格，照样全绿。

- [ ] **Step 3: 骨架层补定位上下文**

`theme.css` 给 `.aj-item` 和 `.osc-item` 各加 `position: relative;`。
`.chg-item, .pl-item` 已经是 `relative`（选中色条要用），不要动。

- [ ] **Step 4: 装饰层新增列表行段**

在 `hud.css` 的角标那组之后追加：

```css
/* ---- 列表行:只要线,不要角 ---- */

/* 一屏二十个角标是噪声,不是科技感 —— 角标回答的是"这是一块独立面板",
   而列表行不是,它们是一列同类项。这条是第一阶段"整站卡片一起飘会显得廉价"
   的同一个道理。
   走 ::after 不走 ::before:.chg-item 和 .pl-item 的 ::before 被选中态的
   3px 左色条占着。四个容器统一用 ::after,不做两个用这个两个用那个的分裂 ——
   同一档待遇用两种机制,下一个人要读两遍才知道它们是一回事。 */
.chg-item::after,
.pl-item::after,
.aj-item::after,
.osc-item::after {
  content: ''; position: absolute; top: 0; left: 0; right: 0; height: 1px;
  pointer-events: none; background: var(--hud-edge);
}

/* 它们本来就可点,所以拿 hover 抬升 —— 和 .dash-card 同一组理由。 */
.chg-item, .pl-item, .aj-item, .osc-item {
  transition: var(--transition-colors), var(--transition-transform);
}
.chg-item:hover,
.pl-item:hover,
.aj-item:hover,
.osc-item:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-lg);
}
[data-theme="dark"] .chg-item:hover,
[data-theme="dark"] .pl-item:hover,
[data-theme="dark"] .aj-item:hover,
[data-theme="dark"] .osc-item:hover {
  box-shadow: var(--glow-sm);
}
```

- [ ] **Step 5: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 90 passed。

- [ ] **Step 6: 跑既有回归**

Run: `npx playwright test e2e/responsive.spec.ts e2e/changes-osc.spec.ts e2e/osc.spec.ts --reporter=list`
Expected: 全绿。`changes-osc` 与 `osc` 两套直接踩在本任务改的页面上。

- [ ] **Step 7: 提交**

```bash
git add src/styles/hud.css src/styles/theme.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): hairline and lift the card-shaped list rows

Four kinds of list row look like cards and repeat a dozen times a page. They
take the hairline and the hover lift but not the corner mark: twenty corner
marks down a screen is noise, and a corner mark says 'this is a panel of its
own', which a row in a list is not.

The hairline rides ::after because ::before is already the selected-state bar
on two of the four. All four use ::after so the tier has one mechanism rather
than two.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 终端仪器档

**Files:**
- Modify: `frontend/src/pages/terminal/index.tsx`（外框内加一个装饰节点）
- Modify: `frontend/src/styles/hud.css`（终端三栏段）
- Modify: `frontend/src/styles/theme.css`（三栏补 `position: relative`）
- Test: `frontend/e2e/hud-visual.spec.ts`

**Interfaces:**
- Consumes: 既有 `.hud-corners`（登录页建的，本轮是它的第二个宿主）、`--hud-edge`。
- Produces: 无新符号。

- [ ] **Step 1: 写失败的测试**

追加：

```ts
test.describe('HUD 第二阶段 · 终端', () => {
  test('外框有四角取景框,且不吃点击', async ({ page }) => {
    await open(page, '/terminal')
    await page.waitForSelector('.tv-grid')
    const el = page.locator('.tv-grid > .hud-corners')
    await expect(el).toHaveCount(1)
    await expect(el).toHaveAttribute('aria-hidden', 'true')
    expect(await styleOf(page, '.tv-grid > .hud-corners', 'pointer-events')).toBe('none')
  })

  test('取景框是绝对定位,不占终端的网格轨道', async ({ page }) => {
    await open(page, '/terminal')
    await page.waitForSelector('.tv-grid')
    // .tv-grid 是 display:grid 且列模板写死三列。装饰节点若不是 absolute
    // 就会变成第四个网格项,把三栏挤位。
    expect(await styleOf(page, '.tv-grid > .hud-corners', 'position')).toBe('absolute')
  })

  test('三栏各有顶沿高光线,但不各自加角标', async ({ page }) => {
    await open(page, '/terminal')
    await page.waitForSelector('.term-main')
    for (const sel of ['.term-tree', '.term-main', '.term-insp']) {
      expect(await styleOf(page, sel, 'background-image', '::before')).toContain('gradient')
      // 角标只属于外框。三栏再各加一个,一屏就是四个角标。
      expect(parseFloat(await styleOf(page, sel, 'border-top-width', '::after'))).toBe(0)
    }
  })
})
```

- [ ] **Step 2: 跑测试,确认它红**

Run: `npx playwright test e2e/hud-visual.spec.ts -g "终端" --reporter=list`
Expected: 三条全 FAIL（节点不存在 / 高光线是 `none`）。

若 `.term-tree` 或 `.term-insp` 取不到，可能是终端页在折叠态（`tree-collapsed` / `insp-collapsed`）
下用 `.tv-rail` 替换了它们 —— 先确认默认态是展开的，必要时在测试里先展开，**不要改断言**。

- [ ] **Step 3: 加装饰节点**

`frontend/src/pages/terminal/index.tsx`，在 `.tv-grid` 的开标签之后、第一个子元素之前插入：

```tsx
      {/* 四角取景框。终端是一台分了三区的仪器,不是三张拼在一起的卡 ——
          角标只属于这圈外框,三栏各自只拿一条顶沿高光线。
          absolute,所以不会变成第四个网格项。 */}
      <span className="hud-corners" aria-hidden="true" />
```

这是全站第二个、也是最后一个为装饰添加的 DOM 节点。

- [ ] **Step 4: 骨架层补定位上下文**

`theme.css` 给 `.term-tree, .term-insp` 与 `.term-main` 各加 `position: relative;`。
`.tv-grid` 已经是 `relative`（≤1280 下检查器要以它为定位祖先），**不要动**。

- [ ] **Step 5: 装饰层新增终端段**

在 `hud.css` 末尾追加：

```css
/* ---- 终端:一台分了三区的仪器 ---- */

/* 外框的四角取景框由 TSX 里的 .hud-corners 节点承担(同登录卡)。三栏在这里
   只拿一条顶沿高光线,不各自加角标 —— 加了一屏就是四个角标,外框那圈反而
   读不出来了。
   这三栏是 overflow:hidden 不是 auto(真正滚的是里面的 .term-tree-list),
   所以可以安全用伪元素;不要套用 .ib-side 那条结论。 */
.term-tree::before,
.term-main::before,
.term-insp::before {
  content: ''; position: absolute; top: 0; left: 0; right: 0; height: 1px;
  pointer-events: none; background: var(--hud-edge); z-index: 1;
}
```

`z-index: 1` 是必要的：三栏里装着 xterm 画布与列表，高光线要浮在它们之上才看得见。
这是整套装饰里唯一需要 `z-index` 的一处 —— 其余都画在空白的容器边缘上。

- [ ] **Step 6: 跑测试,确认它绿**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 93 passed。

- [ ] **Step 7: 跑终端回归 —— 这一步不能跳**

Run: `npx playwright test e2e/terminal-session.spec.ts e2e/responsive.spec.ts --reporter=list`
Expected: 全绿。`terminal-session.spec.ts` 跑的是"连接 → 键入 → 提交 → 回执 → 断开 → 重连"
一整条路，以及 StrictMode 双建、切实例清屏几个踩过的坑；`responsive.spec.ts` 守着终端自己的
1500/1280/1040 三档栏宽。本任务往 `.tv-grid` 里插了 DOM，这两套是它的安全网。

- [ ] **Step 8: 类型检查与构建**

Run: `npm run build`
Expected: 成功。本轮唯一改 TSX 的任务。

- [ ] **Step 9: 提交**

```bash
git add src/pages/terminal/index.tsx src/styles/hud.css src/styles/theme.css e2e/hud-visual.spec.ts
git commit -m "feat(ui): frame the terminal as one instrument, not three cards

The terminal is an outer frame around three panes. Copying the card rules
would have put three corner marks and three hairlines on the densest screen
in the product. Instead the frame takes one set of corners — reusing the
class the login card introduced, which is its second host — and each pane
takes only its top hairline.

The pane hairlines need a z-index, the only place in this refresh that does:
they are drawn over an xterm canvas and a tree, not over empty container
edge.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 覆盖数量断言 + 全量回归 + 十张验收截图

本任务不新增视觉效果。它存在的理由：前四个任务各自只验了自己那一档，而"有没有漏掉某个容器、
有没有把选择器写宽误伤别的容器"只有把三档放在一起数才看得出来。

**Files:**
- Modify: `frontend/e2e/hud-visual.spec.ts`（数量断言）
- Test: 全部

- [ ] **Step 1: 写数量断言**

追加：

```ts
test.describe('HUD 第二阶段 · 覆盖面', () => {
  // 防两件事:漏掉一个容器(装饰不全),以及选择器写宽了误伤别的容器(装饰过度)。
  // 数字写死是有意的 —— 将来增删容器时这条会红,逼人回来改这份清单,
  // 而不是让覆盖面悄悄漂移。
  test('拿到面板档装饰的容器,正好是清单上那些', async ({ page }) => {
    await open(page, '/settings')
    const n = await page.evaluate(() => {
      const list = ['.c-card', '.dash-card', '.login-card', '.perm-roles', '.set-nav',
        '.set-save', '.cat-side', '.cat-detail', '.rr-stat', '.chg-detail',
        '.pl-editor', '.notif-panel', '.usermenu', '.conn-bulkbar']
      // 直接数样式表里的选择器,不数页面上的元素 —— 一个页面不会同时渲染出
      // 这十四个,但规则是全站一份。
      let hit = 0
      for (const sheet of document.styleSheets) {
        let rules: CSSRuleList
        try { rules = sheet.cssRules } catch { continue }
        for (const r of rules) {
          const t = (r as CSSStyleRule).selectorText
          if (!t) continue
          if (t.includes('::before') && t.includes('.c-card::before')) {
            hit = list.filter((s) => t.includes(`${s}::before`)).length
          }
        }
      }
      return hit
    })
    expect(n).toBe(14)
  })

  test('拿到列表行装饰的容器,正好是四个', async ({ page }) => {
    await open(page, '/settings')
    const n = await page.evaluate(() => {
      const list = ['.chg-item', '.pl-item', '.aj-item', '.osc-item']
      for (const sheet of document.styleSheets) {
        let rules: CSSRuleList
        try { rules = sheet.cssRules } catch { continue }
        for (const r of rules) {
          const t = (r as CSSStyleRule).selectorText
          if (t && t.includes('.chg-item::after')) {
            return list.filter((s) => t.includes(`${s}::after`)).length
          }
        }
      }
      return -1
    })
    expect(n).toBe(4)
  })
})
```

- [ ] **Step 2: 跑测试**

Run: `npx playwright test e2e/hud-visual.spec.ts --reporter=list`
Expected: 95 passed。这两条在前四个任务做完之后应当直接绿 —— 它们是账本，不是新行为。
若红，说明某一档的选择器列表和这份清单对不上，**先查列表再改数字**。

- [ ] **Step 3: 全量自动化**

依次运行并报告实际输出：

```
npm run build
npm run test:unit
npm run test:e2e
```

Expected: build 干净、291 单测、95 e2e。任何一个数字低于此，就是回归 —— 贴出失败输出，
不要四舍五入过去。

- [ ] **Step 4: 跑起真应用**

Go 后端 :8080 + Vite :5173，PostgreSQL 已就绪（`vela_gateway` / `vela_test` 均存在）。
用 `linwei@vela.io` / `vela123` 登录。

- [ ] **Step 5: 截十张图**

5 个代表页 × 亮暗：

| 页面 | 路径 | 它能暴露什么 |
|---|---|---|
| 终端 | `/terminal` | 信息密度最高的屏，唯一的"仪器"档 |
| 变更工单 | `/changes` | 长列表 —— 十几条高光线叠在一起的观感 |
| 权限 | `/permissions` | 面板档与选中态色条同屏 |
| 资产目录 | `/catalog` | 双栏面板相邻，高光线与角标会不会打架 |
| 审批待办 | `/inbox` | 唯一的 `background-image` 机制容器，外加空状态 |

主题在账户菜单里切（写到 `<html data-theme>`，默认 `dark`）。全视口 1440×900，
落盘到会话的 scratchpad 目录。

**不要评价这些截图** —— 交给控制方做视觉判断。你的任务是产出十张命名正确、主题正确、
内容完整（非空数据）的图。若某页渲染出来全是空状态，**说出来**，不要把空页面当成成功截图。

- [ ] **Step 6: 汇报**

写清三条命令的真实输出、十张图的路径、每张图的主题如何确认、哪些页面数据为空。
不提交任何代码改动。

---

## 验收标准（控制方逐条对照，与第一阶段同一套）

1. 装饰读作底座与分层，不读作色斑与噪声 —— **长列表页尤其看这条**。
2. 暗色靠高光线与角标分层，不靠投影。
3. 终端页的信息密度没有被装饰吃掉。
4. 三处选中态色条在装饰加上之后仍然一眼可辨。
