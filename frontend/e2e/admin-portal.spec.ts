import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台与用户后台拆分(.scratch/admin-user-split 工单 02):
//   · 管理后台在 /admin,只有平台管理员能进;审计员能进,但只有审计一页
//   · 两个后台各自一套导航,顶栏切换;切换不重新挂载布局(终端标签不丢)
//   · 旧地址(/settings 等)跳到新地址

const L2 = { ...ME, id: 3, name: 'Chen Hao', roleCode: 'l2', roleName: 'DBA L2', roleCodes: ['l2'], roleNames: ['DBA L2'] } // 菜单全开,但不是管理员
const AUDITOR = { ...ME, id: 8, name: 'Hu Jun', roleCode: 'audit', roleName: '审计员', roleCodes: ['audit'], roleNames: ['审计员'],
  menus: { audit: true } }

async function open(page: Page, me: typeof ME, path = '/#/dashboard') {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.goto(path)
}
const navLabels = (page: Page) => page.locator('.rail-item .rl').allTextContents()

test('平台管理员:顶栏切到管理后台,导航换成管理功能;再切回来', async ({ page }) => {
  await open(page, ME)
  await expect(page.getByTestId('admin-portal-tag')).toHaveCount(0)
  await page.getByTestId('portal-switch').click()
  await expect(page).toHaveURL(/#\/admin\/overview/) // 管理员落到管理总览(工单 06)
  await expect(page.getByTestId('admin-portal-tag')).toBeVisible()
  const admin = await navLabels(page)
  expect(admin).toEqual(expect.arrayContaining(['总览', '配置', '规则', '分层', '权限', '审计', '设置']))
  expect(admin).not.toContain('终端')
  await page.getByTestId('portal-switch').click()
  await expect(page).toHaveURL(/#\/dashboard/)
  const user = await navLabels(page)
  expect(user).toContain('终端')
  expect(user).not.toContain('配置')
  expect(user).not.toContain('设置')
})

test('旧地址跳到管理后台的新地址', async ({ page }) => {
  await open(page, ME, '/#/settings')
  await expect(page).toHaveURL(/#\/admin\/settings/)
})

test('非管理员:没有切换入口;直接访问 /admin/* 或旧地址都被送回用户后台', async ({ page }) => {
  await open(page, L2)
  await expect(page.getByTestId('portal-switch')).toHaveCount(0)
  await page.goto('/#/admin/connections')
  await expect(page).toHaveURL(/#\/dashboard/)
  await page.goto('/#/permissions')
  await expect(page).toHaveURL(/#\/dashboard/)
})

test('审计员:能进管理后台,但只有审计一页', async ({ page }) => {
  await open(page, AUDITOR)
  await page.getByTestId('portal-switch').click()
  await expect(page).toHaveURL(/#\/admin\/audit/)
  expect(await navLabels(page)).toEqual(['审计'])
  await page.goto('/#/admin/settings')
  await expect(page).toHaveURL(/#\/dashboard/)
})
