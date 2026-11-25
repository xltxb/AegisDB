import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台拆分(工单 04):
//   · 用户后台的「记录」= 我的操作记录:人人可进,请求带 mine=1,没有导出与审计链校验
//   · 能看全量审计的人(管理员 / 审计员 / DBA 负责人)在用户后台默认看全员(不带 mine、也不带 full),
//     另有「只看我的」切换(2026-10-26 回归排查 09;此前管理员这里也只看自己的)
//   · 全量审计在管理后台 /admin/audit:不带 mine,有导出与校验

const RO = { ...ME, id: 6, name: 'Zhao Lei', roleCode: 'ro', roleName: '研发只读', roleCodes: ['ro'], roleNames: ['研发只读'],
  menus: { terminal: true } } // 没有审计菜单也进得去自己的记录

async function open(page: Page, me: typeof ME, path: string, inst: string[] = []) {
  const seen: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) => {
    inst.push(new URL(r.request().url()).search)
    return r.fulfill(envelope([]))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    seen.push(r.request().url())
    return r.fulfill(envelope({ items: [], total: 0, groups: {} }))
  })
  await page.goto(path)
  await expect.poll(() => seen.length).toBeGreaterThan(0)
  return seen
}

test('我的操作记录:人人可进,只看自己的,没有导出与校验', async ({ page }) => {
  const seen = await open(page, RO, '/#/audit')
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
  expect(seen[0]).toContain('mine=1')
  await expect(page.getByTestId('audit-export')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /校验/ })).toHaveCount(0)
  await expect(page.locator('.rail-item', { hasText: '记录' })).toHaveClass(/active/)
})

test('管理员在用户后台:默认看全员(不带 mine / full),「只看我的」切回自己的', async ({ page }) => {
  const inst: string[] = []
  const seen = await open(page, ME, '/#/audit', inst)
  await expect(page.locator('.v-h1')).toHaveText('操作记录')
  expect(seen[0]).not.toContain('mine=1')
  expect(seen[0]).not.toContain('full=1') // 用户后台不带 full:管理后台空闲不该让这页报 40304
  await expect.poll(() => inst.length).toBeGreaterThan(0)
  expect(inst[0]).toBe('')
  // 仍然没有导出与校验(那是管理后台的)
  await expect(page.getByTestId('audit-export')).toHaveCount(0)
  await page.getByTestId('audit-mine-toggle').click()
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
  await expect.poll(() => seen.some((q) => q.includes('mine=1'))).toBe(true)
  await expect.poll(() => inst.some((q) => q.includes('mine=1'))).toBe(true)
  await page.getByTestId('audit-mine-toggle').click()
  await expect(page.locator('.v-h1')).toHaveText('操作记录')
})

test('审计员在用户后台:默认看全员,有「只看我的」', async ({ page }) => {
  const AUD = { ...ME, id: 7, name: 'Sun Li', roleCode: 'audit', roleName: '审计员', roleCodes: ['audit'], roleNames: ['审计员'], menus: { audit: true } }
  const seen = await open(page, AUD, '/#/audit')
  await expect(page.locator('.v-h1')).toHaveText('操作记录')
  expect(seen[0]).not.toContain('mine=1')
  await expect(page.getByTestId('audit-mine-toggle')).toBeVisible()
})

test('普通用户的我的操作记录没有「只看我的」切换', async ({ page }) => {
  await open(page, RO, '/#/audit')
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
  await expect(page.getByTestId('audit-mine-toggle')).toHaveCount(0)
})

test('管理后台的全量审计:不带 mine,有导出与校验', async ({ page }) => {
  const seen = await open(page, ME, '/#/admin/audit')
  expect(seen[0]).not.toContain('mine=1')
  expect(seen[0]).toContain('full=1')
  await expect(page.getByTestId('audit-mine-toggle')).toHaveCount(0)
  await expect(page.getByTestId('audit-export')).toBeVisible()
  await expect(page.getByRole('button', { name: /校验/ })).toBeVisible()
})
