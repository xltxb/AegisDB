import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

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



// Open the export page with a seeded session and every API it touches stubbed, so
// the test needs no Go backend.
async function openExportPage(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/connections/*/schema*', (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'appdb', tables: [] }, { name: 'c66_dws_report', tables: [] }] })),
  )
  await page.route('**/api/v1/export/jobs', (r) => r.fulfill(envelope([])))
  await page.goto('/#/export')
  await expect(page.locator('.vsel .control')).toBeVisible()
}

// VSelect's root class is `.vsel`; the `.pick` wrapper this spec was written
// against went away in the b754cc2 layout refactor. The export page has exactly
// one VSelect, so the bare class is unambiguous here.
const picker = (page: Page) => page.locator('.vsel')
const control = (page: Page) => page.locator('.vsel .control')
const searchBox = (page: Page) => page.locator('.vsel .sbox input')
const options = (page: Page) => page.locator('.vsel .opt')

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
    await expect(page.locator('.vsel .nores')).toBeVisible()
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

  test('the picker matches the native select stacked beneath it', async ({ page }) => {
    await openExportPage(page)
    // The two controls used to share a row; since the b754cc2 layout refactor the
    // instance picker sits in its own .field directly above the database select.
    // What still matters is that the custom control reads as the same kind of
    // thing as the native one: same height, same left edge, picker first. Both
    // rects are read in ONE evaluate: taken as two separate boundingBox() calls
    // an async relayout between them shows up as a phantom few-pixel drift.
    await expect(page.locator('select.sel option')).not.toHaveCount(0) // databases settled
    const { a, b } = await page.evaluate(() => {
      const r = (s: string) => {
        const { x, y, height } = document.querySelector(s)!.getBoundingClientRect()
        return { x, y, height }
      }
      return { a: r('.vsel .control'), b: r('select.sel') }
    })
    expect(Math.abs(a.height - b.height)).toBeLessThanOrEqual(1)
    expect(Math.abs(a.x - b.x)).toBeLessThanOrEqual(1)
    expect(a.y, 'the picker should sit above the database select').toBeLessThan(b.y)
  })
})

// Every other VSelect in the app passes no `searchable`, and must keep behaving as
// a plain list — the shared control is used in ~10 places that were not touched.
test('a non-searchable VSelect still has no search box', async ({ page }) => {
  // Go straight to the settings page: this test only needs a VSelect that was
  // created without `searchable`, and the settings page has several. Loading the
  // export page first was a second full navigation for nothing, and under the
  // parallel full-suite run that extra work is what made this test flake.
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {} })))
  await page.goto('/#/settings')
  // Wait for the page skeleton before asking for a control inside it.
  await expect(page.locator('.tabs .tab').first()).toBeVisible({ timeout: 15000 })
  const sel = page.locator('.vsel .control').first()
  await expect(sel).toBeVisible({ timeout: 15000 })
  await sel.click()
  await expect(page.locator('.vsel .menu')).toBeVisible()
  await expect(page.locator('.vsel .sbox')).toHaveCount(0)
})
