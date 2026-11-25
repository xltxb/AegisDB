import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台拆分(工单 05):规范审查的规则库维护在管理后台,用户后台只读 + 自查。

const RULES = [{ id: 1, code: 'no_select_star', name: '禁止 SELECT *', kind: 'builtin', level: 'warn', enabled: true, dialect: 'mysql', spec: 'query', params: {} }]
const RO = { ...ME, id: 6, roleCode: 'ro', roleCodes: ['ro'], roleNames: ['研发只读'], menus: { terminal: true } }

async function open(page: Page, me: typeof ME, path: string) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/sql-review/rules', (r) => r.fulfill(envelope(RULES)))
  await page.route('**/api/v1/sql-review/catalog', (r) => r.fulfill(envelope({ rules: [], dialects: ['mysql'] })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.goto(path)
  await expect(page.locator('.v-head')).toBeVisible()
}

test('管理员在用户后台:规则只读,有去管理后台维护的入口;到了管理后台才能新建规则', async ({ page }) => {
  await open(page, ME, '/#/sql-review')
  await expect(page.getByRole('button', { name: '新建规则' })).toHaveCount(0)
  await page.getByTestId('sr-go-admin').click()
  await expect(page).toHaveURL(/#\/admin\/sql-review/)
  await expect(page.getByRole('button', { name: '新建规则' })).toBeVisible()
  await expect(page.locator('.rail-item', { hasText: '规范' })).toHaveClass(/active/)
})

test('普通用户:用户后台只读;管理后台进不去', async ({ page }) => {
  await open(page, RO, '/#/sql-review')
  await expect(page.locator('.roflag')).toBeVisible()
  await expect(page.getByTestId('sr-go-admin')).toHaveCount(0)
  await page.goto('/#/admin/sql-review')
  await expect(page).toHaveURL(/#\/dashboard/)
})
