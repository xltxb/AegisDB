import { test, expect, type Page } from '@playwright/test'

// The instance tree groups environment → database type → instance. Several
// production environments can sit on one prod tier, each holding more than one
// engine, and one instance may reference an environment that has since been
// deleted — instances outlive the groups they were filed under.
//
// Checked in a browser because what is being verified is what an operator can
// actually see and reach: whether the second level renders, whether searching
// opens the levels above a hit, and whether an instance in a deleted environment
// is still listed at all. A missing instance here is one nobody can fix.

const TIERS = [
  { code: 'prod', displayName: '生产环境 · PROD', sortOrder: 0, requireMfa: true, dangerBanner: true, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' },
  { code: 'dev', displayName: '测试 · DEV', sortOrder: 1, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L4', defaultRole: 'developer' },
]

// prod carries TWO environments — the shape that was impossible before.
const ENVIRONMENTS = [
  { code: 'prod-hk', displayName: '香港生产', tierCode: 'prod', sortOrder: 0 },
  { code: 'prod-sh', displayName: '上海生产', tierCode: 'prod', sortOrder: 1 },
  { code: 'dev', displayName: '测试 · DEV', tierCode: 'dev', sortOrder: 2 },
]

const CONNS = [
  // prod-hk holds two engines, which is the case the type level exists for.
  { id: 1, name: 'tongcha', env: 'prod-hk', engine: 'mysql' },
  { id: 2, name: 'hk-billing', env: 'prod-hk', engine: 'mysql' },
  { id: 3, name: 'hk-report', env: 'prod-hk', engine: 'postgres' },
  { id: 4, name: 'sh-orders', env: 'prod-sh', engine: 'polardb' },
  { id: 5, name: 'sandbox', env: 'dev', engine: 'mysql' },
  // Its environment is gone: not in ENVIRONMENTS, resolvable to no tier.
  { id: 6, name: 'legacy-orders', env: 'retired-cluster', engine: 'oracle' },
].map((c) => ({
  ...c, host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online',
}))

const ME = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, mfaEnabled: false,
  menus: { terminal: true, export: true, approve: true, db: true, rules: true, envtier: true, perms: true, audit: true, settings: true },
  capabilities: {},
}

const envelope = (data: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, msg: 'ok', data }) })

async function stubCommon(page: Page, tiers: unknown = TIERS, envs: unknown = ENVIRONMENTS) {
  await page.addInitScript(() => localStorage.setItem('vela_token', 'e2e-token'))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(tiers)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(envs)))
  await page.route('**/api/v1/connections/*/schema*', (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [] })))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/gateway/stats', (r) => r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))
}

async function openTerminal(page: Page) {
  await stubCommon(page)
  await page.goto('/#/terminal')
  await expect(page.locator('.tree')).toBeVisible()
}

const envRows = (page: Page) => page.locator('.tree .env')
const typeRows = (page: Page) => page.locator('.tree .envsub')
const insts = (page: Page) => page.locator('.tree .inst')
const search = (page: Page) => page.locator('.tree .searchbox input')

test.describe('instance tree · environment over type', () => {
  test('environments are the top level, database types sit beneath them', async ({ page }) => {
    await openTerminal(page)

    // Three environments plus the catch-all for the dangling instance.
    await expect(envRows(page)).toHaveCount(4)
    await expect(envRows(page).nth(0)).toContainText('香港生产')
    await expect(envRows(page).nth(1)).toContainText('上海生产')

    // prod-hk is expanded by default (its tier is first) and splits by engine.
    await expect(typeRows(page)).toContainText(['MySQL', 'PostgreSQL'])
    await expect(insts(page).filter({ hasText: 'tongcha' })).toBeVisible()
    await expect(insts(page).filter({ hasText: 'hk-report' })).toBeVisible()
  })

  // The tier lost its own row, so the colour is the only thing left saying which
  // of these clusters is production. It has to be right.
  test('an environment row carries its tier colour and names the tier on hover', async ({ page }) => {
    await openTerminal(page)

    await expect(envRows(page).nth(0).locator('.d.danger')).toBeVisible() // prod tier
    await expect(envRows(page).nth(1).locator('.d.danger')).toBeVisible() // the second production env, equally
    await expect(envRows(page).nth(2).locator('.d.success')).toBeVisible() // dev
    await expect(envRows(page).nth(0)).toHaveAttribute('title', /PROD/)
  })

  // Environments on the first tier open by default; the rest stay shut. Adding a
  // second production cluster must not push the first behind a chevron.
  test('every production environment starts expanded, the rest collapsed', async ({ page }) => {
    await openTerminal(page)

    await expect(insts(page).filter({ hasText: 'tongcha' })).toBeVisible()   // prod-hk
    await expect(insts(page).filter({ hasText: 'sh-orders' })).toBeVisible() // prod-sh
    await expect(insts(page).filter({ hasText: 'sandbox' })).toHaveCount(0)  // dev, collapsed
  })

  test('searching an environment or a type opens the levels above the hit', async ({ page }) => {
    await openTerminal(page)

    // "prod-sh" matches no instance NAME — only the Shanghai environment.
    await search(page).fill('prod-sh')
    await expect(insts(page).filter({ hasText: 'sh-orders' })).toBeVisible()
    await expect(insts(page).filter({ hasText: 'tongcha' })).toHaveCount(0)

    // …and the engine is a search key too: "postgre" finds the reporting replica
    // without knowing its name.
    await search(page).fill('postgre')
    await expect(insts(page).filter({ hasText: 'hk-report' })).toBeVisible()
    await expect(insts(page).filter({ hasText: 'tongcha' })).toHaveCount(0)
  })

  test('an instance whose environment was deleted is still listed', async ({ page }) => {
    await openTerminal(page)

    // It cannot be filed under any environment, so it gets its own group —
    // visible and selectable, rather than silently dropped from the tree while
    // remaining a live, reachable database.
    const unresolved = envRows(page).nth(3)
    await expect(unresolved).toContainText('retired-cluster')
    // Neutral, not a colour that would claim a control level for it.
    await expect(unresolved.locator('.d.muted')).toBeVisible()
    await expect(unresolved).toHaveAttribute('title', /未知环境|Unknown/)
    await expect(insts(page).filter({ hasText: 'legacy-orders' })).toBeVisible()
  })

  test('the tree survives a failed tier load instead of blanking', async ({ page }) => {
    await stubCommon(page)
    await page.route('**/api/v1/env-tiers', (r) => r.fulfill({ status: 500, body: 'boom' }))
    await page.route('**/api/v1/environments', (r) => r.fulfill({ status: 500, body: 'boom' }))
    await page.goto('/#/terminal')

    // With no environments, every instance is unresolved — but every instance is
    // still reachable. Losing the grouping is recoverable; losing the instances
    // is not.
    await expect(page.locator('.tree')).toBeVisible()
    await expect(insts(page)).toHaveCount(CONNS.length)
  })
})

test.describe('connections page · environment over type', () => {
  test('the table groups by environment then by database type', async ({ page }) => {
    await stubCommon(page)
    await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
    await page.goto('/#/connections')

    const groups = page.locator('.grouprow')
    await expect(groups.nth(0)).toContainText('香港生产')
    await expect(groups.nth(1)).toContainText('上海生产')

    // Type sub-headers, in engine-catalogue order rather than alphabetical:
    // MySQL before PostgreSQL, and PolarDB in its catalogue position.
    const types = page.locator('.typerow')
    await expect(types.nth(0)).toContainText('MySQL')
    await expect(types.nth(1)).toContainText('PostgreSQL')

    // The dangling instance keeps a row here too — this page is where an operator
    // would go to move it somewhere real.
    await expect(page.locator('.grouprow', { hasText: 'retired-cluster' })).toBeVisible()
    await expect(page.locator('.trow', { hasText: 'legacy-orders' })).toBeVisible()
  })
})
