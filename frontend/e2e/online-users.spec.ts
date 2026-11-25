import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台「在线用户」。钉三件事:
//   1. 只有管理员看得到这个分页;
//   2. 汇总与每一行:来源、浏览器、最近操作,超过 10 分钟没操作的标"空闲";
//   3. 开着的终端带实例名,正在执行的单独标出来。

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const ONLINE = {
  users: 2, terminals: 2, windowSec: 180,
  sessions: [
    { userId: 1, name: 'Lin Wei', email: 'linwei@vela.io', dept: 'DBA', initials: 'LW', ip: '10.1.2.3', browser: 'Chrome · Windows',
      loginAt: ago(90), lastSeen: ago(0), lastAction: ago(1),
      terminals: [
        { connectionId: 7, instance: 'prod-order-01', openedAt: ago(30), lastExecAt: ago(1), running: true },
        { connectionId: 8, instance: 'uat-order-01', openedAt: ago(20), running: false },
      ] },
    { userId: 2, name: 'Zhang Wei', email: 'zhangwei@vela.io', dept: '研发', initials: 'ZW', ip: '10.9.9.9', browser: 'Edge · macOS',
      loginAt: ago(300), lastSeen: ago(0), lastAction: ago(45), terminals: [] },
  ],
}

async function open(page: Page, me = ME) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/online-users', (r) => r.fulfill(envelope(ONLINE)))
  await page.goto('/#/admin/overview') // 在线用户移到了管理总览(工单 06)
}

test('管理员:在线用户分页列出会话、最近操作与开着的终端', async ({ page }) => {
  await open(page)
  await expect(page.getByTestId('online-summary')).toHaveText('在线 2 人 · 2 个会话 · 2 个终端')
  const rows = page.getByTestId('online-row')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText('10.1.2.3')
  await expect(rows.nth(0)).toContainText('Chrome · Windows')
  await expect(rows.nth(0).getByTestId('online-state')).toHaveText('活跃')
  await expect(rows.nth(1).getByTestId('online-state')).toHaveText('空闲') // 45 分钟没操作
  const terms = rows.nth(0).getByTestId('online-term')
  await expect(terms).toHaveText(['prod-order-01', 'uat-order-01'])
  await expect(terms.nth(0)).toHaveClass(/run/)
  await expect(terms.nth(1)).not.toHaveClass(/run/)

  // 筛选
  await page.getByPlaceholder('按姓名 / 邮箱 / 部门 / IP / 实例筛选').fill('uat-order')
  await expect(rows).toHaveCount(1)
})

// 权限页移进了管理后台(只有平台管理员):非管理员连页面都进不去,在线用户自然也看不到。
test('非管理员进不了管理总览,也就看不到在线用户', async ({ page }) => {
  await open(page, { ...ME, roleCode: 'dba', roleCodes: ['dba'], roleNames: ['DBA'] })
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByTestId('online-summary')).toHaveCount(0)
})
