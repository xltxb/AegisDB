import { test, expect, type Page } from '@playwright/test'

// The export page's instance picker is a searchable VSelect. Its open/close logic
// leans on focusout + relatedTarget rather than blur, because blur does not bubble
// and would close the menu the moment the search box takes focus. That is a real
// browser behaviour — jsdom-style unit tests cannot see it — so it is checked here.

const ENVS = ['prod', 'gli', 'staging', 'dev'] as const

// Enough instances that scanning the list by eye is the wrong tool — which is the
// reason the picker got a search box.
const CONNS = [
  { id: 1, name: 'tongcha', env: 'prod' },
  { id: 2, name: 'tongcha-replica', env: 'prod' },
  { id: 3, name: 'orders', env: 'prod' },
  { id: 4, name: 'orders', env: 'gli' },
  { id: 5, name: 'billing', env: 'staging' },
  { id: 6, name: 'billing', env: 'dev' },
  { id: 7, name: 'polardb-analytics', env: 'prod' },
].map((c) => ({
  ...c,
  engine: 'mysql',
  host: '10.0.0.1',
  port: 3306,
  policy: 'strict',
  defaultRole: 'ro',
  layer: 'core',
  tags: '',
  database: 'appdb',
  status: 'online',
}))

const ME = {
  id: 1,
  name: 'Lin Wei',
  email: 'linwei@vela.io',
  initials: 'LW',
  roleId: 1,
  roleCode: 'dba',
  roleName: 'DBA',
  layer: 'core',
  roleIds: [1],
  roleNames: ['DBA'],
  roleCodes: ['dba'],
  canApprove: true,
  mfaEnabled: false,
  menus: { terminal: true, export: true, approve: true, db: true, rules: true, perms: true, audit: true, settings: true },
  capabilities: {},
}

const envelope = (data: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, msg: 'ok', data }) })

// Open the export page with a seeded session and every API it touches stubbed, so
// the test needs no Go backend.
async function openExportPage(page: Page) {
  await page.addInitScript(() => localStorage.setItem('vela_token', 'e2e-token'))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/connections/*/schema*', (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'appdb', tables: [] }, { name: 'c66_dws_report', tables: [] }] })),
  )
  await page.route('**/api/v1/export/jobs', (r) => r.fulfill(envelope([])))
  await page.goto('/#/export')
  await expect(page.locator('.pick .control')).toBeVisible()
}

const picker = (page: Page) => page.locator('.pick')
const control = (page: Page) => page.locator('.pick .control')
const searchBox = (page: Page) => page.locator('.pick .sbox input')
const options = (page: Page) => page.locator('.pick .opt')

test.describe('export · searchable instance picker', () => {
  test('opens with a focused search box that survives the click that opened it', async ({ page }) => {
    await openExportPage(page)
    await control(page).click()
    // The regression this guards: with @blur the menu closed as soon as the box
    // took focus, so the search field flashed and vanished.
    await expect(searchBox(page)).toBeFocused()
    await expect(options(page)).toHaveCount(CONNS.length)
  })

  test('filters by keyword on both the name and the environment', async ({ page }) => {
    await openExportPage(page)
    await control(page).click()

    await searchBox(page).fill('tongcha')
    await expect(options(page)).toHaveText(['prod-tongcha', 'prod-tongcha-replica'])

    // The label is `env-name`, so an operator can narrow by tier just as easily.
    await searchBox(page).fill('staging')
    await expect(options(page)).toHaveText(['staging-billing'])

    await searchBox(page).fill('nothing-matches-this')
    await expect(options(page)).toHaveCount(0)
    await expect(page.locator('.pick .nores')).toBeVisible()
  })

  test('picking a filtered result selects it and reloads that instance databases', async ({ page }) => {
    await openExportPage(page)
    await control(page).click()
    await searchBox(page).fill('polardb')
    await options(page).first().click()

    await expect(control(page)).toHaveText('prod-polardb-analytics')
    await expect(searchBox(page)).toBeHidden() // menu closed on pick
    // Selecting an instance re-introspects its databases into the sibling select.
    await expect(page.locator('select.sel option')).toContainText(['c66_dws_report'])
  })

  test('Enter takes the first match, Escape abandons the search', async ({ page }) => {
    await openExportPage(page)

    await control(page).click()
    await searchBox(page).fill('billing')
    await searchBox(page).press('Enter')
    await expect(control(page)).toHaveText('staging-billing')

    // Escape closes without changing the selection, and the keyword is dropped so
    // the next open starts from the full list.
    await control(page).click()
    await searchBox(page).fill('dev')
    await searchBox(page).press('Escape')
    await expect(searchBox(page)).toBeHidden()
    await expect(control(page)).toHaveText('staging-billing')

    await control(page).click()
    await expect(searchBox(page)).toHaveValue('')
    await expect(options(page)).toHaveCount(CONNS.length)
  })

  test('clicking outside closes the menu without selecting anything', async ({ page }) => {
    await openExportPage(page)
    const before = await control(page).textContent()

    await control(page).click()
    await expect(searchBox(page)).toBeVisible()
    // Click the card header — it sits ABOVE the picker, so the open menu (which
    // covers everything below it, as a dropdown should) cannot intercept it.
    await page.locator('.shead').first().click()
    await expect(searchBox(page)).toBeHidden()
    await expect(control(page)).toHaveText(before!.trim())
  })

  test('the picker lines up with the native select beside it', async ({ page }) => {
    await openExportPage(page)
    // Same row, so a mismatched height or offset is immediately visible. Both
    // rects are read in ONE evaluate: taken as two separate boundingBox() calls
    // an async relayout between them shows up as a phantom few-pixel drift.
    await expect(page.locator('select.sel option')).not.toHaveCount(0) // databases settled
    const { a, b } = await page.evaluate(() => {
      const r = (s: string) => {
        const { y, height } = document.querySelector(s)!.getBoundingClientRect()
        return { y, height }
      }
      return { a: r('.pick .control'), b: r('select.sel') }
    })
    expect(Math.abs(a.height - b.height)).toBeLessThanOrEqual(1)
    expect(Math.abs(a.y - b.y)).toBeLessThanOrEqual(1)
  })
})

// Every other VSelect in the app passes no `searchable`, and must keep behaving as
// a plain list — the shared control is used in ~10 places that were not touched.
test('a non-searchable VSelect still has no search box', async ({ page }) => {
  await openExportPage(page)
  await page.goto('/#/settings')
  const sel = page.locator('.vsel .control').first()
  await expect(sel).toBeVisible()
  await sel.click()
  await expect(page.locator('.vsel .menu')).toBeVisible()
  await expect(page.locator('.vsel .sbox')).toHaveCount(0)
})
