import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 管理后台拆分(工单 03):实例、规则、分层、权限、系统设置这几个菜单只能给平台管理员角色。
// 别的角色上开关灰掉、标「仅管理员」;平台管理员角色照常可勾。

const ROLES = [
  { id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown', members: 1 },
  { id: 2, code: 'l2', name: 'DBA L2', layer: 'L2 · 执行', icon: 'user-cog', members: 2 },
]
const detail = (r: typeof ROLES[number], menus: Record<string, boolean>) => ({ ...r, menus, matrix: {}, memberIds: [], members: [], tags: [] })

async function open(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope(ROLES)))
  await page.route('**/api/v1/roles/1', (r) => r.fulfill(envelope(detail(ROLES[0], { db: true, perms: true, settings: true, terminal: true }))))
  await page.route('**/api/v1/roles/2', (r) => r.fulfill(envelope(detail(ROLES[1], { terminal: true }))))
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.goto('/#/admin/permissions')
}

test('非管理员角色:管理类菜单的开关灰掉,标「仅管理员」;普通菜单照常可改', async ({ page }) => {
  await open(page)
  await page.getByText('DBA L2', { exact: true }).first().click()
  for (const k of ['db', 'rules', 'envtier', 'perms', 'settings']) {
    const card = page.getByTestId('menu-' + k)
    await expect(card).toHaveClass(/locked/)
    await expect(card).toContainText('仅管理员')
  }
  await expect(page.getByTestId('menu-terminal')).not.toHaveClass(/locked/)
})

test('平台管理员角色:管理类菜单照常可改', async ({ page }) => {
  await open(page)
  await page.getByText('平台管理员', { exact: true }).first().click()
  await expect(page.getByTestId('menu-perms')).not.toHaveClass(/locked/)
  await expect(page.getByTestId('menu-settings')).not.toContainText('仅管理员')
})
