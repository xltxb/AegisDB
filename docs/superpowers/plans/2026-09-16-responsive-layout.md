# 前端自适应布局 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把控制台 19 个页面在 768–1080 宽度下的「挤烂」修掉：断点口径收敛成两个值，9 张表按列优先级在窄屏收列而不是压扁。

**Architecture:** 一组纯函数（`breakpointOf` / `visibleCols` / `gridTemplate`）决定「当前该显示哪些列、grid 模板长什么样」，共用 `Table` 与 4 张手搓表都调它；`useBreakpoint` 只负责订阅 `matchMedia`，不参与计算。CSS 侧把 13 处散落断点迁到 `≤768` / `≤1080` 两档。

**Tech Stack:** React 19 · TypeScript · CSS（无预处理器，纯 `@media`）· Playwright（单测 runner + e2e）

**Spec:** `docs/superpowers/specs/2026-09-16-responsive-layout-design.md`

## Global Constraints

- 断点常量只有两个：`BP_NARROW = 768`、`BP_MID = 1080`。边界含义是 `width <= 768` 为 narrow、`width <= 1080` 为 mid，其余 wide。
- **不改任何业务逻辑与接口**。本计划不触碰 `api/`、`hooks/`（`useBreakpoint` 除外）、后端。
- **不动** `components/permission/CapabilityMatrix.tsx` 及其 `.cap-scroll` 横滚——那是有意设计。
- **不动** `styles/theme.css` 中 `.tv-grid` 的 1500 / 1280 / 1040 三档（终端页例外）。
- 表格一律 CSS grid，不引入 `<table>`。
- grid 轨道数必须恒等于渲染出的单元格数。这是本次改动唯一可能引入的新缺陷类别。
- 单测跑 `npm run test:unit`（Node，无 DOM，无 `window`/`matchMedia`）；e2e 跑 `npm run test:e2e`（真浏览器，API 由 spec 自打桩）。
- 中文注释，说明「为什么」而不是「做了什么」，与现有代码风格一致。

---

### Task 1: 断点基元

**Files:**
- Create: `frontend/src/lib/breakpoints.ts`
- Create: `frontend/src/hooks/useBreakpoint.ts`
- Test: `frontend/tests/unit/breakpoints.spec.ts`

**Interfaces:**
- Consumes: 无
- Produces: `BP_NARROW: 768`、`BP_MID: 1080`、`type Breakpoint = 'wide' | 'mid' | 'narrow'`、`breakpointOf(width: number): Breakpoint`、`useBreakpoint(): Breakpoint`

**为什么拆成两个文件:** `useBreakpoint` 要用 `matchMedia`，而单测跑在 Node 里没有这个全局。把判档逻辑抽成 `breakpointOf(width)` 纯函数，它才测得到；hook 退化成一层订阅。（规格里写的是「`useBreakpoint` 只做订阅」，这里是它的落地形态。）

- [ ] **Step 1: 写失败的测试**

```ts
// frontend/tests/unit/breakpoints.spec.ts
import { test, expect } from '@playwright/test'
import { BP_MID, BP_NARROW, breakpointOf } from '../../src/lib/breakpoints'

// 边界取闭区间:768 自己算 narrow,不是 mid。平板竖屏正好是 768,
// 把它判成 mid 等于这套改动对最典型的目标设备不生效。
test('768 及以下是 narrow,769 起是 mid', () => {
  expect(breakpointOf(320)).toBe('narrow')
  expect(breakpointOf(BP_NARROW)).toBe('narrow')
  expect(breakpointOf(BP_NARROW + 1)).toBe('mid')
})

test('1080 及以下是 mid,1081 起是 wide', () => {
  expect(breakpointOf(BP_MID)).toBe('mid')
  expect(breakpointOf(BP_MID + 1)).toBe('wide')
  expect(breakpointOf(1920)).toBe('wide')
})

// 视口宽度不会是负数或 NaN,但 `useBreakpoint` 在挂载前读到的可能是 0。
// 0 判成 narrow 而不是抛错 —— 首帧渲染一张只有主干列的表,比白屏好。
test('0 与非法值退回 narrow,不抛错', () => {
  expect(breakpointOf(0)).toBe('narrow')
  expect(breakpointOf(Number.NaN)).toBe('narrow')
})
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `cd frontend && npx playwright test -c playwright.unit.config.ts breakpoints`
Expected: FAIL — `Cannot find module '../../src/lib/breakpoints'`

- [ ] **Step 3: 写最小实现**

```ts
// frontend/src/lib/breakpoints.ts

/**
 * 两个断点,不是八个。
 *
 * 改之前全站有 13 处 `@media`,散在 720/900/960/1040/1080/1100/1280/1500 八个值上。
 * 实测这些值分两簇:720-960 与 1040-1100 —— 也就是说它们本来就想表达两件事,
 * 只是每次有人加断点时各挑了一个手边的数。
 *
 * 边界取 1080 而不是 1280:现有 7 处折叠集中在 1040-1100,上推到 1280 会让
 * 1100-1280 屏宽的人失去今天已有的双栏密度。收敛是为了统一口径,不是降密度。
 */
export const BP_NARROW = 768
export const BP_MID = 1080

export type Breakpoint = 'wide' | 'mid' | 'narrow'

/** 视口宽 → 档位。非法值(挂载前读到的 0、NaN)退回最保守的一档,不抛错。 */
export function breakpointOf(width: number): Breakpoint {
  if (!Number.isFinite(width) || width <= BP_NARROW) return 'narrow'
  if (width <= BP_MID) return 'mid'
  return 'wide'
}
```

```ts
// frontend/src/hooks/useBreakpoint.ts
import { useSyncExternalStore } from 'react'
import { BP_MID, BP_NARROW, breakpointOf, type Breakpoint } from '@/lib/breakpoints'

/**
 * 当前档位。**只订阅,不判断** —— 判断在 `breakpointOf` 里,那是纯函数,测得到。
 *
 * 用 matchMedia 而不是 resize:拖窗口时 resize 每帧都发,而档位一次会话里通常只
 * 变零次。两条 media query 各自只在跨过边界时回调一次。
 */
const QUERIES = [`(max-width: ${BP_NARROW}px)`, `(max-width: ${BP_MID}px)`]

function subscribe(cb: () => void) {
  const mqls = QUERIES.map((q) => window.matchMedia(q))
  for (const m of mqls) m.addEventListener('change', cb)
  return () => { for (const m of mqls) m.removeEventListener('change', cb) }
}

export function useBreakpoint(): Breakpoint {
  return useSyncExternalStore(
    subscribe,
    () => breakpointOf(window.innerWidth),
    () => 'wide' as Breakpoint,
  )
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx playwright test -c playwright.unit.config.ts breakpoints`
Expected: PASS — 3 passed

- [ ] **Step 5: 类型检查并提交**

```bash
cd frontend && npm run type-check
git add frontend/src/lib/breakpoints.ts frontend/src/hooks/useBreakpoint.ts frontend/tests/unit/breakpoints.spec.ts
git commit -m "feat(layout): converge the breakpoint vocabulary onto two values"
```

---

### Task 2: 列优先级的两个纯函数

**Files:**
- Create: `frontend/src/lib/tableColumns.ts`
- Test: `frontend/tests/unit/tableColumns.spec.ts`

**Interfaces:**
- Consumes: Task 1 的 `Breakpoint`
- Produces:
  - `type ColPriority = 1 | 2 | 3`
  - `interface ColSpec { key: string; width: string; priority?: ColPriority }`
  - `visibleCols<C extends ColSpec>(cols: C[], bp: Breakpoint): C[]`
  - `gridTemplate(cols: ColSpec[]): string`

**为什么放在 `lib/` 而不是 `Table.tsx`:** 手搓的 4 张表不经过 `Table` 组件,但要守同一条约束。放在 lib 里两边都能取,而且它不 import React —— 单测能在 Node 里直接跑。

- [ ] **Step 1: 写失败的测试**

```ts
// frontend/tests/unit/tableColumns.spec.ts
import { test, expect } from '@playwright/test'
import { gridTemplate, visibleCols, type ColSpec } from '../../src/lib/tableColumns'

const COLS: ColSpec[] = [
  { key: 'name', width: '1.5fr' },                 // 不写优先级 = 1
  { key: 'engine', width: '0.9fr', priority: 2 },
  { key: 'addr', width: '1.6fr', priority: 3 },
  { key: 'status', width: '1fr', priority: 1 },
]

test('wide 下所有列都在,顺序不变', () => {
  expect(visibleCols(COLS, 'wide').map((c) => c.key)).toEqual(['name', 'engine', 'addr', 'status'])
})

test('mid 收起优先级 3', () => {
  expect(visibleCols(COLS, 'mid').map((c) => c.key)).toEqual(['name', 'engine', 'status'])
})

test('narrow 只留优先级 1', () => {
  expect(visibleCols(COLS, 'narrow').map((c) => c.key)).toEqual(['name', 'status'])
})

// 不写 priority 的列必须留到最后 —— 9 张表里有大量列没显式标注,
// 把「没标注」读成「最低优先级」会让它们在窄屏集体消失。
test('不写 priority 视同 1,narrow 下仍在场', () => {
  expect(visibleCols([{ key: 'a', width: '1fr' }], 'narrow').map((c) => c.key)).toEqual(['a'])
})

// 这条是整套改动的核心约束。grid 轨道数和渲染出的单元格数一旦脱节,
// 表格会整体错位一列,而类型检查与构建都看不见。
test('模板的轨道数恒等于可见列数', () => {
  for (const bp of ['wide', 'mid', 'narrow'] as const) {
    const vis = visibleCols(COLS, bp)
    expect(gridTemplate(vis).split(' ').length, `${bp} 档轨道数对不上`).toBe(vis.length)
  }
})

test('模板按列序拼接宽度', () => {
  expect(gridTemplate(visibleCols(COLS, 'narrow'))).toBe('1.5fr 1fr')
})
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `cd frontend && npx playwright test -c playwright.unit.config.ts tableColumns`
Expected: FAIL — `Cannot find module '../../src/lib/tableColumns'`

- [ ] **Step 3: 写最小实现**

```ts
// frontend/src/lib/tableColumns.ts
import type { Breakpoint } from '@/lib/breakpoints'

/**
 * 1 = 永远在场;2 = narrow 下收起;3 = mid 与 narrow 下都收起。
 *
 * **不写视同 1。** 9 张表里大部分列没有显式标注,把「没标注」读成最低优先级
 * 会让它们在窄屏集体消失 —— 而缺省应当是「保守地留着」。
 */
export type ColPriority = 1 | 2 | 3

export interface ColSpec {
  key: string
  /** grid 列宽,如 '1.5fr' / '96px'。 */
  width: string
  priority?: ColPriority
}

const CUTOFF: Record<Breakpoint, ColPriority> = { wide: 3, mid: 2, narrow: 1 }

/** 当前档位下该显示的列,顺序不变。 */
export function visibleCols<C extends ColSpec>(cols: C[], bp: Breakpoint): C[] {
  const max = CUTOFF[bp]
  return cols.filter((c) => (c.priority ?? 1) <= max)
}

/**
 * grid 模板。**必须喂 `visibleCols` 的结果**,不能喂原始列表 ——
 * 轨道数与单元格数一旦脱节,整张表错位一列,而这件事类型检查看不见。
 */
export function gridTemplate(cols: ColSpec[]): string {
  return cols.map((c) => c.width).join(' ')
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx playwright test -c playwright.unit.config.ts tableColumns`
Expected: PASS — 6 passed

- [ ] **Step 5: 类型检查并提交**

```bash
cd frontend && npm run type-check
git add frontend/src/lib/tableColumns.ts frontend/tests/unit/tableColumns.spec.ts
git commit -m "feat(table): decide visible columns from a column priority"
```

---

### Task 3: 共用 Table 接上优先级

**Files:**
- Modify: `frontend/src/components/common/Table.tsx`（`Column` 接口 + `Table` 函数体）
- Modify: `frontend/src/pages/audit/index.tsx:153-187`（7 列加 priority）
- Modify: `frontend/src/pages/catalog/index.tsx:351-379`（4 列加 priority）
- Modify: `frontend/src/pages/execWindows/index.tsx:56-95, 297-316`（两张表,6 列 + 5 列）
- Modify: `frontend/src/pages/gov/index.tsx:67-101`（5 列加 priority）

**Interfaces:**
- Consumes: Task 1 的 `useBreakpoint`；Task 2 的 `visibleCols` / `gridTemplate` / `ColPriority`
- Produces: `Column<Row>` 新增可选字段 `priority?: ColPriority`

- [ ] **Step 1: 改 `Table.tsx` 的接口与函数体**

```tsx
// frontend/src/components/common/Table.tsx —— 顶部新增 import
import { useBreakpoint } from '@/hooks/useBreakpoint'
import { gridTemplate, visibleCols, type ColPriority, type ColSpec } from '@/lib/tableColumns'

export interface Column<Row> extends ColSpec {
  head: ReactNode
  cell: (row: Row) => ReactNode
  mono?: boolean
}
```

函数体里把 `const tpl = columns.map((c) => c.width).join(' ')` 换成：

```tsx
  // 窄屏收列,而不是把八列压进 768px。被收起的值由各页自己的展开行交代 ——
  // 这里只负责「轨道与单元格始终一致」这一条。
  const bp = useBreakpoint()
  const cols = visibleCols(columns, bp)
  const tpl = gridTemplate(cols)
```

并把函数体里两处 `columns.map(...)` 改成 `cols.map(...)`（表头一处、行内一处）。

- [ ] **Step 2: 给 5 张表的列标优先级**

`pages/audit/index.tsx`：`time` 与 `cmd` 与 `result` 不写（视同 1）；`who` 与 `risk` 加 `priority: 2`；`inst` 与 `ap` 加 `priority: 3`。

`pages/catalog/index.tsx`：`name` 与 `comment` 不写；`type` 加 `priority: 2`；`idx` 加 `priority: 3`。

`pages/execWindows/index.tsx` 窗口表：`win`/`status`/`op` 不写；`db` 加 `priority: 2`；`tz`/`days` 加 `priority: 3`。
同文件放行记录表：`time`/`cmd` 不写；`risk` 加 `priority: 2`；`who`/`inst` 加 `priority: 3`。

`pages/gov/index.tsx`：`col`/`op` 不写；`style` 加 `priority: 2`；`preview`/`exempt` 加 `priority: 3`。

- [ ] **Step 3: 跑单测与类型检查**

Run: `cd frontend && npm run test:unit && npm run type-check`
Expected: 265 passed（256 原有 + Task 1 的 3 条 + Task 2 的 6 条；Task 3 只改渲染，不加单测）· 类型检查无输出

- [ ] **Step 4: 在跑着的应用上目视确认**

Run: 浏览器开 `http://localhost:5173/audit`，窗口拉到 768 宽
Expected: 只剩「时间 / 命令 / 结果」三列，表头与数据行对齐，没有空列

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/common/Table.tsx frontend/src/pages/audit/index.tsx \
        frontend/src/pages/catalog/index.tsx frontend/src/pages/execWindows/index.tsx \
        frontend/src/pages/gov/index.tsx
git commit -m "feat(table): shed columns on narrow viewports instead of crushing them"
```

---

### Task 4: 4 张手搓表接上优先级

**Files:**
- Modify: `frontend/src/pages/connections/index.tsx:40`（`COLS` 常量）与 `:529, :587`（两处 `gridTemplateColumns`）与其间的单元格
- Modify: `frontend/src/pages/users/index.tsx:28`（`COLS`）与 `:73, :130`
- Modify: `frontend/src/pages/connections/EnvTiers.tsx:64`（`TIER_COLS`）与 `:115, :132`；`:352`（`ENV_COLS`）与 `:377, :389`

**Interfaces:**
- Consumes: Task 1 的 `useBreakpoint`；Task 2 的 `visibleCols` / `gridTemplate` / `ColSpec`
- Produces: 无（页面内部改动）

**手法:** 把每个 `const COLS = '…'` 字符串换成 `ColSpec[]`，渲染处用一个 `show(key)` 判定是否渲染该单元格。**不要**用 CSS `display:none` 隐藏单元格——grid 轨道仍在，会留空列。

- [ ] **Step 1: 改 `connections/index.tsx`**

```tsx
// 顶部 import
import { useBreakpoint } from '@/hooks/useBreakpoint'
import { gridTemplate, visibleCols, type ColSpec } from '@/lib/tableColumns'

/**
 * 8 列。勾选框、实例名、状态、操作是主干,窄屏一律保留 —— 一张选不中、
 * 看不出死活、点不动的表,列再全也没用。
 */
const COLS: ColSpec[] = [
  { key: 'sel', width: '26px' },
  { key: 'inst', width: '1.45fr' },
  { key: 'engine', width: '0.9fr', priority: 2 },
  { key: 'addr', width: '1.6fr', priority: 3 },
  { key: 'role', width: '0.8fr', priority: 3 },
  { key: 'policy', width: '1fr', priority: 3 },
  { key: 'status', width: '1.05fr' },
  { key: 'ops', width: '112px' },
]
```

组件内（`ConnectionsPage` 函数体，与 `const [qInput, setQInput]` 等并列）：

```tsx
  const bp = useBreakpoint()
  const cols = visibleCols(COLS, bp)
  const tpl = gridTemplate(cols)
  const show = (key: string) => cols.some((c) => c.key === key)
```

`:529` 的 `style={{ gridTemplateColumns: COLS }}` 改为 `style={{ gridTemplateColumns: tpl }}`，`:587` 同。
表头与数据行里对应 `engine` / `addr` / `role` / `policy` 的那四个 `<div>` 各自包一层 `{show('engine') && ( … )}`。

- [ ] **Step 2: 改 `users/index.tsx`**

```tsx
/** 用户 / 账号 / 角色 / 最近活跃 / 状态。用户名与状态是主干。 */
const COLS: ColSpec[] = [
  { key: 'user', width: '1.4fr' },
  { key: 'account', width: '1.6fr', priority: 3 },
  { key: 'roles', width: '2.2fr', priority: 2 },
  { key: 'active', width: '0.9fr', priority: 3 },
  { key: 'status', width: '0.9fr' },
]
```

同样在组件内取 `cols` / `tpl` / `show`，改 `:73` 与 `:130` 的模板，并给 `account` / `roles` / `active` 三处单元格加 `show()` 判定。

- [ ] **Step 3: 改 `EnvTiers.tsx` 的两张表**

```tsx
/** 分层表:分层名、默认角色、操作是主干;四个开关列最先收。 */
const TIER_COLS: ColSpec[] = [
  { key: 'tier', width: '1.6fr' },
  { key: 'mfa', width: '88px', priority: 3 },
  { key: 'banner', width: '88px', priority: 3 },
  { key: 'pending', width: '88px', priority: 3 },
  { key: 'scan', width: '88px', priority: 3 },
  { key: 'layer', width: '120px', priority: 2 },
  { key: 'role', width: '1.2fr' },
  { key: 'ops', width: '76px' },
]

/**
 * 环境表只有 4 列,**任何档位都不收** —— 收列是为了给挤压让路,4 列在 768 下不挤。
 *
 * 第二列是「绑定分层」,不是显示名:它是一个环境最要紧的事实(归哪个分层管,
 * 也就决定了适用哪套规则),藏起来这张表就只剩一串环境名。
 */
const ENV_COLS: ColSpec[] = [
  { key: 'env', width: '1.6fr' },
  { key: 'tierBind', width: '1.6fr' },
  { key: 'insts', width: '120px' },
  { key: 'ops', width: '96px' },
]
```

`TierTable` 与 `EnvTable` 两个函数各自取 `cols` / `tpl` / `show`，改对应的 `gridTemplateColumns`，并给被标优先级的单元格加 `show()` 判定。

- [ ] **Step 4: 跑单测、类型检查，并在应用上确认**

Run: `cd frontend && npm run test:unit && npm run type-check`
Expected: 全部通过

Run: 浏览器开 `http://localhost:5173/connections`，拉到 768 宽
Expected: 只剩勾选框 / 实例 / 状态 / 操作四列；地址不再折成 6 行；表头「默认角色」不再断成两行

- [ ] **Step 5: 提交**

```bash
git add frontend/src/pages/connections/index.tsx frontend/src/pages/users/index.tsx \
        frontend/src/pages/connections/EnvTiers.tsx
git commit -m "feat(table): put the hand-rolled grids on the same column priority"
```

---

### Task 5: CSS 断点收敛

**Files:**
- Modify: `frontend/src/styles/spacing.css`（新增两个断点常量的注释说明）
- Modify: `frontend/src/styles/theme.css` 共 10 处 `@media`（行号见下）与 `:152-153`、`:475`、`:874`

**Interfaces:**
- Consumes: 无（纯 CSS，但值必须与 Task 1 的 `BP_NARROW` / `BP_MID` 一致）
- Produces: 全站只剩 `max-width: 768px` 与 `max-width: 1080px` 两种通用断点

**注意:** CSS 无法 import TS 常量。两处值必须靠人盯住一致，所以在 `spacing.css` 里写明它与 `lib/breakpoints.ts` 是一对，改一处要改两处。

- [ ] **Step 1: 在 `spacing.css` 末尾补上断点说明**

```css
  /* ---- Breakpoints ----
     两个断点,不是八个。值必须与 src/lib/breakpoints.ts 的 BP_NARROW / BP_MID
     一致 —— CSS 取不到 TS 常量,这一对只能靠人盯住。改一处就要改两处。

       narrow  max-width: 768px    只留主干列;卡片行上下堆叠
       mid     max-width: 1080px   次要列收起;双栏折单栏

     终端页 .tv-grid 的 1500/1280/1040 三档是写明的例外:它是全站唯一的三栏
     页面,栏宽微调与形态切换的阈值本就不该和通用页面共用一个值。 */
```

- [ ] **Step 2: 逐条迁移 10 处断点**

按下表改 `theme.css` 里的 `max-width` 值（选择器与规则体不动）：

| 现行号 | 选择器 | 现值 → 新值 |
|---|---|---|
| 477 | `.chg-wrap, .pl-wrap` | 1080 → 1080（值不变，确认即可） |
| 523 | `.chg-body` | 960 → 1080 |
| 561 | `.pl-egrid` | 960 → 1080 |
| 657 | `.aj-grid` | 1040 → 1080 |
| 720 | `.sc-grid` | 1040 → 1080 |
| 790 | `.sr-wrap` | 1080 → 1080（值不变） |
| 876 | `.perm-grid` | 1080 → 1080（值不变） |
| 930 | `.perm-menugrid` | 720 → 768 |
| 1278 | `.set-wrap, .cat-wrap` | 900 → 768 |
| 2413 | `.ib-grid` | 1100 → 1080 |

**1821 / 1826 / 1832 的 `.tv-grid` 三档不动。**

- [ ] **Step 3: 把四个硬上限改成随屏让边距**

`theme.css:152-153`：

```css
/* 窄屏自动让出边距,不再硬顶容器边缘。上限值不变 —— 它们是排版决定,
   不是自适应问题。 */
.page { max-width: min(100% - 32px, 1120px); }
.page-narrow { max-width: min(100% - 32px, 960px); }
```

`theme.css:475`：`.chg, .pl { max-width: min(100% - 32px, 1320px); }`
`theme.css:874`：`.perm-page { max-width: min(100% - 32px, 1240px); }`

- [ ] **Step 4: 确认没有漏网的旧断点**

Run: `cd frontend && grep -n "@media" src/styles/*.css | grep -v "prefers-\|print" | grep -vE "768px|1080px|1500px|1280px|1040px"`
Expected: 无输出（1280/1040/1500 只应出现在 `.tv-grid` 三条上）

- [ ] **Step 5: 提交**

```bash
git add frontend/src/styles/spacing.css frontend/src/styles/theme.css
git commit -m "refactor(css): converge thirteen ad-hoc breakpoints onto two"
```

---

### Task 6: 外壳与卡片行

**Files:**
- Modify: `frontend/src/styles/theme.css:110-119`（`.top` 一组）与 `:170-174`（`.c-card-row` 一组）

**Interfaces:**
- Consumes: Task 5 的断点值
- Produces: 无

- [ ] **Step 1: 修顶栏标题的折行**

在 `theme.css` 的 `.top-sub` 之后补：

```css
/* 390 宽下「数据库管理网关」会竖成一列单字,压在页面标题上 —— flex 子项的
   min-width 默认是 auto,它不肯缩,于是改为逐字折行。单行省略才是对的:
   标题是路牌,认不出来就该截断,不该把自己摊平成一张纸条。 */
.top-head { min-width: 0; }
.top-title { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

@media (max-width: 768px) {
  /* 窄屏下这两枚只留图标与数字,文字标签让位给标题。 */
  .pill-health span, .pill-pending span { display: none; }
}
```

- [ ] **Step 2: 修设置页卡片行的竖排单字**

在 `theme.css` 的 `.c-row-ctl` 之后补：

```css
/* 「左标题右控件」在窄屏会把标题挤成一列单字(390 下的「默认策略」就是)。
   改成上下堆叠 —— 控件比标题更需要宽度,而标题读不出来的一行等于没有。 */
@media (max-width: 768px) {
  .c-card-row { flex-direction: column; align-items: stretch; gap: 10px; }
  .c-row-ctl { margin-left: 0; }
}
```

- [ ] **Step 3: 在应用上确认**

Run: 浏览器开 `http://localhost:5173/settings`，拉到 390 宽
Expected: 顶栏标题单行省略、不再竖排；「默认策略」标题横排一行，下拉框在其下方占满宽度

- [ ] **Step 4: 确认外壳自身不再溢出**

在浏览器控制台跑：

```js
document.documentElement.scrollWidth - window.innerWidth
```

Expected: `<= 0`（改前是 33）

- [ ] **Step 5: 提交**

```bash
git add frontend/src/styles/theme.css
git commit -m "fix(shell): stop the top bar and card rows folding into single-character columns"
```

---

### Task 7: 页头按钮溢出菜单

**Files:**
- Create: `frontend/src/components/common/PageActions.tsx`
- Modify: `frontend/src/pages/connections/index.tsx:395-415`（3 个 Button）
- Modify: `frontend/src/pages/audit/index.tsx:202` 起的 `.grow`（4 个 Button）
- Modify: `frontend/src/styles/theme.css`（`.pa-more` 一组新样式）

**Interfaces:**
- Consumes: Task 1 的 `useBreakpoint`
- Produces: `<PageActions primary={ReactNode} extras={{ key: string; node: ReactNode }[]} />`

**为什么只改两页:** 全站 18 个 `.page-head` 里只有审计（4 个）与数据源（3 个）超过两个按钮。其余页面按钮不多，`.page-head` 的 flex 换行足够。

- [ ] **Step 1: 写 `PageActions` 组件**

```tsx
// frontend/src/components/common/PageActions.tsx
import { useState, type ReactNode } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { useBreakpoint } from '@/hooks/useBreakpoint'

/**
 * 页头动作区:窄屏保留首个主按钮,其余收进「⋯」。
 *
 * 不收会怎样:数据源页三个按钮在 768 下各折两行,页头长到把表挤下屏外。
 * 首个按钮留在外面,是因为一页总有一个「主要想干的事」(新建连接、导出),
 * 把它也藏起来等于每次都要多点一下。
 */
export function PageActions({
  primary, extras,
}: {
  primary: ReactNode
  extras: { key: string; node: ReactNode }[]
}) {
  const [open, setOpen] = useState(false)
  const narrow = useBreakpoint() === 'narrow'

  if (!narrow) return <>{primary}{extras.map((e) => <span key={e.key}>{e.node}</span>)}</>

  return (
    <>
      {primary}
      <div className="pa-more">
        <button type="button" className="iconbtn" onClick={() => setOpen((v) => !v)}>
          <MoreHorizontal size={16} />
        </button>
        {open && (
          <div className="pa-menu" onClick={() => setOpen(false)}>
            {extras.map((e) => <div key={e.key} className="pa-item">{e.node}</div>)}
          </div>
        )}
      </div>
    </>
  )
}

export default PageActions
```

- [ ] **Step 2: 补样式**

在 `theme.css` 末尾：

```css
/* ---- 页头动作的溢出菜单 ---- */
.pa-more { position: relative; }
.pa-menu {
  position: absolute; right: 0; top: calc(100% + 6px); z-index: var(--z-overlay);
  min-width: 168px; padding: 6px; display: flex; flex-direction: column; gap: 4px;
  border: 1px solid var(--border-subtle); border-radius: 12px;
  background: var(--surface-card); box-shadow: var(--shadow-overlay, 0 8px 24px rgb(0 0 0 / .28));
}
.pa-item > * { width: 100%; justify-content: flex-start; }
```

- [ ] **Step 3: 接到数据源页**

`connections/index.tsx` 的 `.grow` 里，把三个 `<Button>` 换成：

```tsx
          {view === 'instances' && (
            <PageActions
              primary={<Button variant="primary" onClick={() => openCreate()}><Plus size={15} />{t('connNew')}</Button>}
              extras={[
                { key: 'projects', node: <Button variant="secondary" onClick={() => setProjectsOpen(true)}><FolderKanban size={15} />{t('prTitle')}</Button> },
                { key: 'import', node: <Button variant="secondary" onClick={() => setImportOpen(true)}><Upload size={15} />{t('connImport')}</Button> },
              ]}
            />
          )}
```

（`primary` 用页面原有的「新建连接」按钮，保留它原本的 props；`Plus` 与 `connNew` 照该文件现有写法。）

- [ ] **Step 4: 接到审计页**

同样把审计页 `.grow` 里 4 个按钮中最主要的一个作为 `primary`，其余 3 个进 `extras`。

- [ ] **Step 5: 跑检查并在应用上确认**

Run: `cd frontend && npm run type-check && npm run test:unit`
Expected: 全部通过

Run: 浏览器开 `http://localhost:5173/connections`，拉到 768 宽
Expected: 页头只有一个主按钮 + 一个「⋯」，不再折行；点「⋯」弹出另两个

- [ ] **Step 6: 提交**

```bash
git add frontend/src/components/common/PageActions.tsx frontend/src/pages/connections/index.tsx \
        frontend/src/pages/audit/index.tsx frontend/src/styles/theme.css
git commit -m "feat(page-head): fold extra actions into an overflow menu on narrow"
```

---

### Task 8: 终端检查器改可唤回覆盖层

**Files:**
- Modify: `frontend/src/styles/theme.css:1826-1831`（`.tv-grid` 的 1280 档）
- Modify: `frontend/src/pages/terminal/index.tsx`（唤回按钮）

**Interfaces:**
- Consumes: 无
- Produces: 无

**改之前的问题:** `:1829` 的 `.tv-grid > :last-child { display: none; }` 在 ≤1280 把右侧检查器整个隐掉，`:1830` 连唤回的 `.tv-edge.right` 也一并隐了 —— **没有任何方式能把它调回来**。那一栏装着目标实例、目标库、控制分层、网关策略、会话链路与「风险检查」页签，也就是「这条语句为什么被拦」的答案。

- [ ] **Step 1: 改 CSS，把「隐掉」换成「移出文档流的覆盖层」**

把 `theme.css:1826-1831` 整段替换为：

```css
@media (max-width: 1280px) {
  .tv-grid { grid-template-columns: var(--tree-w, 248px) 1fr; }
  .tv-grid.tree-collapsed { grid-template-columns: 30px 1fr; }
  /* 不再 display:none —— 它是「这条语句为什么被拦」的答案,掉了就问不到了。
     改成从右侧滑出的覆盖层,默认收着,由页头的按钮唤回。 */
  .tv-grid > .tv-insp {
    position: absolute; right: 0; top: 0; bottom: 0; z-index: var(--z-overlay);
    width: min(320px, 86vw);
    transform: translateX(100%); transition: transform var(--dur-base, .18s) ease;
    box-shadow: var(--shadow-overlay, 0 8px 24px rgb(0 0 0 / .28));
  }
  .tv-grid > .tv-insp.open { transform: translateX(0); }
  .tv-edge.right { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  .tv-grid > .tv-insp { transition: none; }
}
```

并给 `.tv-grid` 本体加 `position: relative;`（覆盖层要以它为定位祖先）。

- [ ] **Step 2: 给检查器容器补 `tv-insp` 类名与开合状态**

在 `pages/terminal/index.tsx` 中，给右侧检查器的最外层元素加上 `className={clsx('tv-insp', inspOpen && 'open')}`，并在页头工具条补一个唤回按钮：

```tsx
  // ≤1280 时检查器变成覆盖层,默认收着 —— 宽屏下它是第三栏,这个状态用不上。
  const [inspOpen, setInspOpen] = useState(false)
```

```tsx
  <button className="tv-act" title={t('tvInspector')} onClick={() => setInspOpen((v) => !v)}>
    <PanelRightOpen size={15} />
  </button>
```

（`tvInspector` 词条需在 `locales/zh.ts` 与 `locales/en.ts` 各补一条，否则 `localeParity` 单测会红。）

- [ ] **Step 3: 跑单测确认词条对齐**

Run: `cd frontend && npm run test:unit`
Expected: 全部通过，`localeParity` 不红

- [ ] **Step 4: 在应用上确认**

Run: 浏览器开 `http://localhost:5173/terminal`，拉到 1024 宽
Expected: 右侧不再是空白；点页头按钮，检查器从右滑出，显示目标实例与控制分层

- [ ] **Step 5: 提交**

```bash
git add frontend/src/styles/theme.css frontend/src/pages/terminal/index.tsx \
        frontend/src/locales/zh.ts frontend/src/locales/en.ts
git commit -m "fix(terminal): make the inspector reachable below 1280 instead of dropping it"
```

---

### Task 9: e2e 自适应回归口

**Files:**
- Create: `frontend/e2e/responsive.spec.ts`
- Modify: `frontend/e2e/fixtures.ts`（导出 `OPS_ROUTES` / `ADMIN_ROUTES` 两张路由表）

**Interfaces:**
- Consumes: `e2e/fixtures.ts` 现有的 `ADMIN` / `envelope` / `seedSession` / `stubShell`
- Produces: `OPS_ROUTES: string[]`、`ADMIN_ROUTES: string[]`

- [ ] **Step 1: 在 `fixtures.ts` 末尾导出两张路由表**

```ts
/** 两个门户各自能到的页面。门户选错不会报错,只会被静默送回 /terminal。 */
export const OPS_ROUTES = [
  'dashboard', 'terminal', 'approvals', 'inbox', 'changes',
  'scripts', 'export', 'async-jobs', 'exec-windows', 'catalog',
]
export const ADMIN_ROUTES = [
  'connections', 'risk-rules', 'sql-review', 'gov',
  'permissions', 'pipelines', 'users', 'audit', 'settings',
]
```

- [ ] **Step 2: 写 e2e spec**

```ts
// frontend/e2e/responsive.spec.ts
import { test, expect, type Page } from '@playwright/test'
import { ADMIN_ROUTES, OPS_ROUTES, envelope, seedSession, stubShell } from './fixtures'

// 这一口盯的是排版算完之后才成立的事。改之前全站没有页面级横向溢出,
// 问题是**挤烂**:768 下数据源页的地址列折成六行、表头断成两截;390 下外壳
// 自身溢出、两处标签竖成一列单字。类型检查、构建、单测都看不见这些。

const WIDTHS = [1440, 1080, 768]

async function stubAll(page: Page) {
  await seedSession(page)
  // 兜底**先注册**:Playwright 后注册者优先,所以随后的 stubShell 才盖得住它。
  // 反过来写的话兜底会吃掉 /auth/me,整个会话拿不到身份。
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page)
}

/** 页面级横向溢出。>1px 才算,躲开亚像素舍入。 */
async function overflow(page: Page, vw: number) {
  return page.evaluate((w) => Math.max(
    document.documentElement.scrollWidth, document.body.scrollWidth) - w, vw)
}

for (const [portal, routes] of [['运维前台', OPS_ROUTES], ['管理后台', ADMIN_ROUTES]] as const) {
  test.describe(`${portal} · 三档宽度下不溢出`, () => {
    for (const w of WIDTHS) {
      test(`${w}px`, async ({ page }) => {
        await stubAll(page)
        await page.goto('/login')
        await page.click(`button.portal-opt:has-text("${portal}")`)
        await page.fill('input[type="email"]', 'linwei@vela.io')
        await page.fill('input[type="password"]', 'vela123')
        await page.click('button.login-submit')
        await page.waitForTimeout(1500)

        await page.setViewportSize({ width: w, height: 900 })
        for (const route of routes) {
          await page.goto(`/${route}`)
          await page.waitForTimeout(300)
          expect(await overflow(page, w), `${route} 在 ${w}px 下横向溢出`).toBeLessThanOrEqual(1)
        }
      })
    }
  })
}

// 标题被挤成竖排单字时,它的高度会是行高的好几倍而宽度只有一两个字符。
// 这正是 390 下「数据库管理网关」与「默认策略」的样子。
test('窄屏下标题不被挤成竖排单字', async ({ page }) => {
  await stubAll(page)
  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto('/settings')
  await expect(page.locator('.top-title')).toBeVisible()

  const ratio = await page.locator('.top-title').evaluate((el) => {
    const r = el.getBoundingClientRect()
    return r.height / parseFloat(getComputedStyle(el).lineHeight || '20')
  })
  expect(ratio, '顶栏标题折成了多行 —— 它应当单行省略').toBeLessThan(1.6)
})

// 能力矩阵的横滚是**有意设计**(见 CapabilityMatrix.tsx 的注释)。
// 把它写成断言,是为了挡住下一个人在做自适应时「顺手把它修掉」。
test('能力矩阵在 768 下仍然横滚,不被这轮改动顺手修掉', async ({ page }) => {
  await stubAll(page)
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(
    ['prod', 'gli', 'staging', 'uat', 'dev'].map((code, i) => ({ code, displayName: code.toUpperCase(), sortOrder: i })))))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([{ id: 1, code: 'admin', name: 'Admin', layer: 'L0', icon: 'crown', members: 1 }])))
  await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope(
    { id: 1, code: 'admin', name: 'Admin', layer: 'L0', icon: 'crown', menus: {}, matrix: {}, members: [], memberIds: [], tags: [] })))

  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  await page.setViewportSize({ width: 768, height: 900 })
  await page.goto('/permissions')
  await expect(page.locator('.cap-scroll')).toBeVisible()

  const scrolls = await page.locator('.cap-scroll').evaluate((el) => el.scrollWidth > el.clientWidth + 4)
  expect(scrolls, '能力矩阵不再横滚 —— 它是有意设计,不该被自适应改动收掉').toBe(true)
})
```

- [ ] **Step 2b: 补一条「轨道数 = 单元格数」的 DOM 断言**

在同一个 spec 里追加。这是规格点名的唯一新缺陷类别的真正守卫 —— 纯函数层够不到它，
它只在调用方把**未经 `visibleCols` 过滤**的列表喂给 `gridTemplate` 时发生，
表现为整张表错位一列，而类型检查与构建都看不见。

```ts
// 轨道数与单元格数脱节 = 整张表错位一列。这件事只有布局算完之后才看得出来,
// 所以它在这里,不在单测里 —— 单测那层拿不到「实际渲染了几个单元格」。
test('每张表的 grid 轨道数都等于它每行的单元格数', async ({ page }) => {
  await stubAll(page)
  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  for (const w of WIDTHS) {
    await page.setViewportSize({ width: w, height: 900 })
    for (const route of ADMIN_ROUTES) {
      await page.goto(`/${route}`)
      await page.waitForTimeout(300)
      const bad = await page.evaluate(() => {
        const out: string[] = []
        for (const head of document.querySelectorAll('.c-thead')) {
          // computed value 是解析后的像素轨道列表,minmax() 已经被算成一个值,
          // 所以数它比数模板字符串里的空格可靠。
          const tracks = getComputedStyle(head).gridTemplateColumns.split(' ').length
          if (head.children.length !== tracks) {
            out.push(`表头 ${tracks} 轨道 vs ${head.children.length} 单元格`)
          }
          const row = head.parentElement?.querySelector('.c-trow')
          if (row && row.children.length !== tracks) {
            out.push(`数据行 ${row.children.length} 单元格 vs ${tracks} 轨道`)
          }
        }
        return out
      })
      expect(bad, `${route} 在 ${w}px 下轨道与单元格对不上`).toEqual([])
    }
  }
})
```

- [ ] **Step 3: 跑 e2e**

Run: `cd frontend && npm run test:e2e`
Expected: 16 条原有 + 新增全部通过

- [ ] **Step 4: 跑全量检查**

Run: `cd frontend && npm run type-check && npm run test:unit && npm run test:e2e && npm run build`
Expected: 四项全绿

- [ ] **Step 5: 提交**

```bash
git add frontend/e2e/responsive.spec.ts frontend/e2e/fixtures.ts
git commit -m "test(e2e): pin the responsive guarantees the browser is the only witness to"
```

---

## 计划自查记录

**规格覆盖：** 规格四节逐条对应 —— 断点口径 → Task 1/5；表格隐列 → Task 2/3/4；外壳与页头 → Task 6/7；终端检查器 → Task 8；测试 → Task 1/2 的单测与 Task 9 的 e2e。

**与规格的两处偏离（实现时按本计划为准）：**

1. 规格说「`useBreakpoint()` 只做订阅」，但没说判档逻辑放哪。本计划抽成 `breakpointOf(width)` 纯函数单独测 —— `matchMedia` 在 Node 里不存在，不抽出来这段逻辑就没有单测口。
2. 规格把「9 张表各自有一列 priority 1」列为单测。**移到 Task 9 的 e2e**：该断言需要 import 各页的 `.tsx` 模块，会把 React 与 JSX 运行时拖进 Node runner。e2e 里量「窄屏下没有零列的表」更直接，也更贴近这条约束真正要防的事故。

