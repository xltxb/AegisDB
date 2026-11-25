import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台拆分 · 2026-10 审查修复(问题 1、3、4、6)。

const ADMIN = { ...ME, menus: { ...ME.menus, project: true, mylog: true } }
const PROJECTS = [{ id: 1, name: '订单业务线', owner: 'Lin Wei', description: '', databases: 2, releases: 3 }]

async function base(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ADMIN)))
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope(PROJECTS)))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {}, adminIpAllowlistForcedOff: false })))
}

test('用户后台的项目页只读:管理员看到去管理后台编辑的入口,系统设置的「项目」页签里才能新建', async ({ page }) => {
  await base(page)
  await page.goto('/#/projects')
  await expect(page.locator('.iname', { hasText: '订单业务线' })).toBeVisible()
  await expect(page.getByRole('button', { name: '新建项目' })).toHaveCount(0)
  await expect(page.locator('.item .act')).toHaveCount(0)
  await page.getByRole('button', { name: '去管理后台编辑' }).click()
  await expect(page).toHaveURL(/#\/admin\/settings\?tab=projects/)
  await expect(page.getByRole('button', { name: '新建项目' })).toBeVisible()
  await expect(page.locator('.item .act')).toHaveCount(2)
})

test('刷新直接打开 #/admin:管理员落到管理总览,而不是被送回用户后台', async ({ page }) => {
  await base(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 8 })))
  await page.goto('/#/admin')
  await expect(page).toHaveURL(/#\/admin\/overview/)
})

test('进入管理后台时 IP 不在白名单:冷启动回用户后台,只提示一次', async ({ page }) => {
  await base(page)
  await page.route('**/api/v1/admin/enter', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40303, msg: '当前 IP 不在管理后台白名单内', data: null }) }))
  await page.goto('/#/admin/settings')
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByText('无法进入管理后台')).toHaveCount(1)
})

test('站内点进管理后台被 IP 白名单挡住:留在原页', async ({ page }) => {
  await base(page)
  await page.route('**/api/v1/admin/enter', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40303, msg: '当前 IP 不在管理后台白名单内', data: null }) }))
  await page.goto('/#/projects')
  await expect(page.locator('.iname', { hasText: '订单业务线' })).toBeVisible()
  await page.getByRole('button', { name: '去管理后台编辑' }).click()
  await expect(page.getByText('无法进入管理后台')).toHaveCount(1)
  await expect(page).toHaveURL(/#\/projects/)
})

// 2026-10-26 回归排查 09:管理员在用户后台的 /audit 默认看全员(不带 mine、不带 full),「只看我的」才带 mine。
test('两个后台共用审计页:从全量审计切到用户后台的记录页会重新按口径取数', async ({ page }) => {
  await base(page)
  const seen: string[] = []
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) => r.fulfill(envelope([])))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    seen.push(new URL(r.request().url()).search)
    return r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 100 }))
  })
  await page.goto('/#/admin/audit')
  await expect.poll(() => seen.some((q) => q.includes('full=1'))).toBe(true)
  const before = seen.length
  await page.evaluate(() => { location.hash = '#/audit' })
  await expect.poll(() => seen.length).toBeGreaterThan(before)
  expect(seen[before]).not.toContain('full=1')
  expect(seen[before]).not.toContain('mine=1')
  await page.getByTestId('audit-mine-toggle').click()
  await expect.poll(() => seen.some((q) => q.includes('mine=1'))).toBe(true)
})
