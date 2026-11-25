import { test, expect } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-14 用户确认的三条:
//   1. 总览的「执行窗口生效中」列所有人此刻开着的窗口,别人的标出申请人;
//   2. 审计:平台管理员、DBA 负责人、审计员看所有人,其他人只看自己的 —— DBA 负责人在用户后台的记录页看全员;
//   3. 只开了数据查询的业务方也有「我的操作记录」。

const OWNER = { ...ME, id: 2, name: 'Zhang Wei', roleCode: 'owner', roleCodes: ['owner'], roleNames: ['DBA 负责人'],
  menus: { terminal: true, approve: true, audit: true, pipeline: true, execwindow: true } }
const L2 = { ...OWNER, id: 3, name: 'Chen Hao', roleCode: 'l2', roleCodes: ['l2'], roleNames: ['DBA L2'] }
const EMPTY_AUDIT = { items: [], total: 0, page: 1, pageSize: 100 }

async function stubAudit(page: import('@playwright/test').Page, seen: string[]) {
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) => r.fulfill(envelope([])))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    seen.push(new URL(r.request().url()).search)
    return r.fulfill(envelope(EMPTY_AUDIT))
  })
}

test('DBA 负责人:记录页是「操作记录」,看所有人的(不带 mine)', async ({ page }) => {
  const seen: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(OWNER)))
  await stubAudit(page, seen)
  await page.goto('/#/audit')
  await expect(page.locator('.v-h1')).toHaveText('操作记录')
  await expect.poll(() => seen.length).toBeGreaterThan(0)
  expect(seen.every((q) => !q.includes('mine=1'))).toBe(true)
  // 2026-10-26 回归排查 09:能看全员的人另有「只看我的」切换
  await page.getByTestId('audit-mine-toggle').click()
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
  await expect.poll(() => seen.some((q) => q.includes('mine=1'))).toBe(true)
})

// 2026-10-26 讨论 18:DBA 负责人在用户后台恢复审计导出与链校验
test('DBA 负责人:用户后台的记录页有导出与链校验', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(OWNER)))
  await stubAudit(page, [])
  await page.goto('/#/audit')
  await expect(page.getByTestId('audit-export')).toBeVisible()
  await expect(page.locator('.gbtn', { hasText: /校验/ })).toBeVisible()
})

test('DBA L2:记录页仍是「我的操作记录」,只取自己的(mine=1)', async ({ page }) => {
  const seen: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(L2)))
  await stubAudit(page, seen)
  await page.goto('/#/audit')
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
  await expect.poll(() => seen.some((q) => q.includes('mine=1'))).toBe(true)
  // 没有导出 / 校验
  await expect(page.getByTestId('audit-export')).toHaveCount(0)
})

test('只开了数据查询的业务方:导航里有「记录」,点进去是我的操作记录', async ({ page }) => {
  const seen: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, roleCode: 'biz', roleCodes: ['biz'], menus: { query: true } })))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [], limits: {} })))
  await stubAudit(page, seen)
  await page.goto('/#/query')
  const nav = page.locator('.rail-item')
  await expect(nav).toHaveCount(2) // 数据查询 + 记录;总览这类常驻项仍藏着
  await nav.filter({ hasText: '记录' }).click()
  await expect(page).toHaveURL(/#\/audit/)
  await expect(page.locator('.v-h1')).toHaveText('我的操作记录')
})

test('总览:生效中的窗口列所有人的,别人的标出申请人', async ({ page }) => {
  const win = (id: number, createdBy: number, createdByName: string) => ({
    id, name: `窗口${id}`, enabled: true, active: true, status: 'approved', kind: 'once', connectionId: 1,
    database: 'orders_db', createdBy, createdByName, startsAt: new Date(Date.now() - 60_000).toISOString(),
    endsAt: new Date(Date.now() + 3_600_000).toISOString(), weekdays: '', startMin: 0, endMin: 0, timezone: 'Asia/Shanghai', reason: '',
  })
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(L2)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([{ id: 1, name: 'ORDER', env: 'prod', engine: 'MySQL 8.0' }])))
  await page.route('**/api/v1/exec-windows*', (r) => {
    const active = new URL(r.request().url()).searchParams.get('scope') === 'active'
    return r.fulfill(envelope(active ? [win(1, 3, 'Chen Hao'), win(2, 1, 'Lin Wei')] : []))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope(EMPTY_AUDIT)))
  await page.route('**/api/v1/approvals*', (r) => r.fulfill(envelope({ items: [], total: 0 })))
  await page.route('**/api/v1/releases*', (r) => r.fulfill(envelope({ items: [], total: 0 })))
  await page.goto('/#/dashboard')
  await expect(page.getByTestId('dash-window-owner')).toHaveText(['我申请的', 'Lin Wei'])
})

// 2026-10-19 用户确认:已批准、还没到点的班车排期也是所有人的 —— 别人的标申请人;
// 没有窗口菜单的研发只读也看得到排期(但看不到别人待审批的申请,点了也不跳转)。
test('总览:已批准未到点的排期列所有人的,研发只读也看得到', async ({ page }) => {
  const win = (id: number, createdBy: number, createdByName: string, status = 'approved') => ({
    id, name: `排期${id}`, enabled: true, active: false, expired: false, status, kind: 'once', connectionId: 1,
    database: 'orders_db', createdBy, createdByName, startsAt: new Date(Date.now() + 3_600_000).toISOString(),
    endsAt: new Date(Date.now() + 7_200_000).toISOString(), weekdays: '', startMin: 0, endMin: 0, timezone: 'Asia/Shanghai', reason: '',
  })
  const RO = { ...ME, id: 6, name: 'Zhao Lei', roleCode: 'ro', roleCodes: ['ro'], roleNames: ['研发只读'], menus: { dashboard: true, terminal: true } }
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(RO)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([{ id: 1, name: 'ORDER', env: 'prod', engine: 'MySQL 8.0' }])))
  await page.route('**/api/v1/exec-windows*', (r) => {
    const scope = new URL(r.request().url()).searchParams.get('scope')
    if (scope === 'scheduled') return r.fulfill(envelope([win(1, 3, 'Chen Hao'), win(2, 6, 'Zhao Lei')]))
    if (scope === 'active') return r.fulfill(envelope([]))
    return r.fulfill(envelope([win(9, 6, 'Zhao Lei', 'pending')])) // 自己的待审批:没有窗口菜单时不显示
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope(EMPTY_AUDIT)))
  await page.route('**/api/v1/approvals*', (r) => r.fulfill(envelope({ items: [], total: 0 })))
  await page.route('**/api/v1/releases*', (r) => r.fulfill(envelope({ items: [], total: 0 })))
  await page.goto('/#/dashboard')
  await expect(page.getByTestId('dash-win-schedule')).toBeVisible()
  await expect(page.getByTestId('dash-win-row')).toHaveCount(2)
  await expect(page.getByTestId('dash-win-owner')).toHaveText(['Chen Hao', '我申请的'])
  await page.getByTestId('dash-win-row').first().click()
  await expect(page).toHaveURL(/#\/dashboard/)
})
