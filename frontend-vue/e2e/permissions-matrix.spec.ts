import { test, expect, type Page } from '@playwright/test'

// The capability matrix is a grid: one row per capability, one column per control
// tier, and the cell where they meet is the level. Its whole job is to let
// someone read a capability ACROSS the tiers — "SELECT is allowed on dev, needs
// approval on prod" — which only works while the tiers sit side by side.
//
// It broke by losing its columns. The column template was moved onto a wrapper
// element that is a plain block, where `grid-template-columns` means nothing and
// no error is raised; the rows, which are the grids, were left with no template
// and collapsed to a single implicit column. Every cell then stacked vertically,
// the rows grew five times too tall, and two thirds of the table was blank.
//
// Nothing in the type checker, the build or a unit test can see that: the markup
// and the data were correct throughout, and only the geometry was wrong. So this
// is checked in a browser, by asking the one question that distinguishes a grid
// from a stack — are the tier headers on the same line as each other, and do the
// cells of one capability sit beside each other rather than under.

const TIERS = [
  { code: 'prod', displayName: '生产环境 · PROD', sortOrder: 0, requireMfa: true, dangerBanner: true, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' },
  { code: 'gli', displayName: '法务 · GLI', sortOrder: 1, requireMfa: true, dangerBanner: false, countsInPending: true, scanBaseline: true, connLayer: 'L2', defaultRole: 'dba_l2' },
  { code: 'staging', displayName: '预发布 · STAGING', sortOrder: 2, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L3', defaultRole: 'developer' },
  { code: 'uat', displayName: '演练 · UAT', sortOrder: 3, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'developer' },
  { code: 'dev', displayName: '开发 · DEV', sortOrder: 4, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L4', defaultRole: 'developer' },
]

const ENVIRONMENTS = TIERS.map((t, i) => ({ code: t.code, displayName: t.displayName, tierCode: t.code, sortOrder: i }))

const ROLES = [
  { id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown', members: 1 },
  { id: 2, code: 'dba_owner', name: 'DBA 负责人', layer: 'L1 · 终审', icon: 'shield', members: 2 },
]

// Levels differ across tiers on purpose: a matrix that renders the same symbol
// everywhere would still look plausible with the columns mixed up.
const ROLE_DETAIL = {
  ...ROLES[0],
  menus: { terminal: true, export: true, approve: true, db: true, rules: true, envtier: true, perms: true, audit: true, settings: true },
  matrix: {
    select: { prod: 'allow', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
    write: { prod: 'approve', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
    ddl: { prod: 'deny', gli: 'approve', staging: 'approve', uat: 'approve', dev: 'allow' },
    grant: { prod: 'allow', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
    conn: { prod: 'allow', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
    approve: { prod: 'allow', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
    explain: { prod: 'allow', gli: 'allow', staging: 'allow', uat: 'allow', dev: 'allow' },
  },
  memberIds: [1],
  tags: [],
}

const ME = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, mfaEnabled: false,
  menus: { terminal: true, export: true, approve: true, db: true, rules: true, envtier: true, perms: true, audit: true, settings: true },
  capabilities: {},
}

const envelope = (data: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, msg: 'ok', data }) })

async function openPermissions(page: Page) {
  await page.addInitScript(() => localStorage.setItem('vela_token', 'e2e-token'))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(TIERS)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(ENVIRONMENTS)))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope(ROLES)))
  await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope(ROLE_DETAIL)))
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/gateway/stats', (r) => r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))

  await page.goto('/#/permissions')
  await expect(page.locator('.mth')).toBeVisible()
}

const headerCells = (page: Page) => page.locator('.mth .ctr')

test.describe('capability matrix · role view', () => {
  test('there is one column per tier, and they are side by side', async ({ page }) => {
    await openPermissions(page)

    await expect(headerCells(page)).toHaveCount(TIERS.length)
    await expect(headerCells(page)).toHaveText(TIERS.map((t) => t.code))

    const boxes = await headerCells(page).evaluateAll((els) =>
      els.map((e) => e.getBoundingClientRect()).map((r) => ({ x: r.x, y: r.y })))

    // Same line: a stacked layout puts each header on its own row, which is
    // precisely how this looked when it was broken.
    const ys = boxes.map((b) => Math.round(b.y))
    expect(new Set(ys).size, `tier headers are on ${new Set(ys).size} different rows — the columns collapsed`).toBe(1)

    // …and in tier order, left to right.
    const xs = boxes.map((b) => b.x)
    expect(xs, 'tier headers are not in increasing x order').toEqual([...xs].sort((a, b) => a - b))
    expect(new Set(xs.map(Math.round)).size, 'tier headers overlap instead of occupying separate columns').toBe(TIERS.length)
  })

  test('one capability row shows its tiers beside each other, not stacked', async ({ page }) => {
    await openPermissions(page)

    const row = page.locator('.mtr').first()
    const cells = row.locator('.cellwrap')
    await expect(cells).toHaveCount(TIERS.length)

    const boxes = await cells.evaluateAll((els) =>
      els.map((e) => e.getBoundingClientRect()).map((r) => ({ x: r.x, y: r.y })))
    expect(new Set(boxes.map((b) => Math.round(b.y))).size, 'the cells of one capability stacked vertically').toBe(1)

    // A stacked row is several cells tall. Keep it to roughly one cell so the
    // regression cannot come back as "renders, but every row is five times too
    // tall" — which is what the operator actually saw.
    const h = await row.evaluate((e) => e.getBoundingClientRect().height)
    expect(h, `capability row is ${Math.round(h)}px tall — it is stacking`).toBeLessThan(80)
  })

  test('a cell sits under its own tier column', async ({ page }) => {
    await openPermissions(page)

    // The header and the cell beneath it must share a column, or the matrix is
    // legible but says the wrong thing about which tier a level applies to.
    const headers = await headerCells(page).evaluateAll((els) =>
      els.map((e) => e.getBoundingClientRect()).map((r) => r.x + r.width / 2))
    const cells = await page.locator('.mtr').first().locator('.cellwrap')
      .evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => r.x + r.width / 2))

    expect(cells).toHaveLength(headers.length)
    headers.forEach((hx, i) => {
      expect(Math.abs(cells[i] - hx), `cell ${i} is not under the ${TIERS[i].code} header`).toBeLessThan(2)
    })
  })
})
