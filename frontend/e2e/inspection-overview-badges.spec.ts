import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// AI 巡检报告 · 工单 05(.scratch/inspection-report):管理总览「今日巡检」、实例树的巡检标记。

const TODAY = '2026-10-15'
const rep = (id: number, connectionId: number, reportDate: string, level: string, topFinding = '') => ({
  id, connectionId, instanceName: 'x', reportDate, level, score: 80, criticalCount: level === 'critical' ? 2 : 0,
  warningCount: level === 'warning' ? 3 : 0, infoCount: 1, sizeBytes: 1, updatedAt: '2026-10-15T03:00:00Z', topFinding,
})
const LATEST = {
  today: TODAY,
  items: [
    { connectionId: 1, instance: 'prod-ok', env: 'prod', engine: 'MySQL 8.0', report: rep(11, 1, TODAY, 'ok') },
    { connectionId: 2, instance: 'prod-warn', env: 'prod', engine: 'MySQL 8.0', report: rep(12, 2, TODAY, 'warning', 't_user 缺少索引') },
    { connectionId: 3, instance: 'prod-bad', env: 'prod', engine: 'MySQL 8.0', report: rep(13, 3, TODAY, 'critical', 't_order 分区将用尽') },
    { connectionId: 4, instance: 'prod-never', env: 'prod', engine: 'TiDB', report: null },
  ],
}
const ADMIN = { ...ME, menus: { ...ME.menus, inspection: true } }

async function overview(page: Page, latest: unknown) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ADMIN)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: { 'conn.check.enabled': 'true' }, secretsSet: {} })))
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => r.fulfill(envelope({ items: [], total: 0, pending: 0 })))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 8, groups: {} })))
  await page.route('**/api/v1/online-users', (r) => r.fulfill(envelope({ sessions: [], users: 0, terminals: 0, windowSec: 600 })))
  await page.route('**/api/v1/inspection-reports/latest', (r) => r.fulfill(envelope(latest)))
  await page.goto('/#/admin/overview')
}

test('管理总览「今日巡检」:严重在前、带最严重的问题;未收到的折叠;点开去报告', async ({ page }) => {
  await overview(page, LATEST)
  const card = page.getByTestId('ao-insp')
  await expect(card).toContainText('今天已收到 3 / 4')
  const rows = page.getByTestId('ao-insp-row')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText('prod-bad')
  await expect(rows.nth(0)).toContainText('t_order 分区将用尽')
  await expect(rows.nth(1)).toContainText('prod-warn')
  await expect(page.getByTestId('ao-insp-missing-item')).toHaveCount(0)
  await page.getByTestId('ao-insp-missing').click()
  await expect(page.getByTestId('ao-insp-missing-item')).toHaveText(['prod-never'])
  await rows.nth(0).click()
  await expect(page).toHaveURL(/#\/inspection\?instance=3&report=13/)
})

test('管理总览「今日巡检」:全部正常、都收到时一句话', async ({ page }) => {
  await overview(page, { today: TODAY, items: [LATEST.items[0], { ...LATEST.items[0], connectionId: 9, instance: 'prod-ok-2', report: rep(19, 9, TODAY, 'ok') }] })
  await expect(page.getByTestId('ao-insp-all-ok')).toContainText('今天 2 个实例巡检全部正常')
  await expect(page.getByTestId('ao-insp-row')).toHaveCount(0)
})

const CONNS = [1, 2, 3, 4].map((id) => ({
  id, name: LATEST.items[id - 1].instance, engine: 'MySQL 8.0', env: 'prod', status: 'online', host: 'h', port: 3306, database: 'shop', defaultRole: 'dba_l2',
}))

test('终端实例树:严重 / 警告的实例名旁有巡检标记,正常的没有', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ADMIN)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/inspection-reports/latest', (r) => r.fulfill(envelope(LATEST)))
  await page.goto('/#/terminal')
  await expect(page.locator('.inst', { hasText: 'prod-bad' }).getByTestId('insp-badge')).toBeVisible()
  await expect(page.locator('.inst', { hasText: 'prod-warn' }).getByTestId('insp-badge')).toBeVisible()
  await expect(page.locator('.inst', { hasText: 'prod-ok' }).getByTestId('insp-badge')).toHaveCount(0)
  await expect(page.locator('.inst', { hasText: 'prod-never' }).getByTestId('insp-badge')).toHaveCount(0)
  await expect(page.locator('.inst', { hasText: 'prod-bad' }).getByTestId('insp-badge')).toHaveAttribute('title', /t_order 分区将用尽/)
})

test('看不了巡检报告的角色:不请求巡检数据,实例树上没有标记', async ({ page }) => {
  let asked = 0
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, id: 6, roleCode: 'ro', roleCodes: ['ro'], menus: { terminal: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/inspection-reports/latest', (r) => { asked++; return r.fulfill(envelope(LATEST)) })
  await page.goto('/#/terminal')
  await expect(page.locator('.inst', { hasText: 'prod-bad' })).toBeVisible()
  await page.waitForTimeout(300)
  await expect(page.getByTestId('insp-badge')).toHaveCount(0)
  expect(asked).toBe(0)
})

// 用户后台总览:今天有严重 / 警告时加一张「巡检异常」卡片和一块提示;全部正常、或看不了巡检的角色,什么都不加。
async function dashboard(page: Page, me: unknown, latest: unknown) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/inspection-reports/latest', (r) => r.fulfill(envelope(latest)))
  await page.goto('/#/dashboard')
}

test('用户后台总览:巡检异常卡片 + 提示,严重在前,点开去报告', async ({ page }) => {
  await dashboard(page, ADMIN, LATEST)
  await expect(page.getByTestId('dash-insp-stat')).toContainText('2')
  await expect(page.getByTestId('dash-insp-stat')).toHaveClass(/bad/)
  const panel = page.getByTestId('dash-insp')
  await expect(panel).toContainText('今日巡检发现异常')
  await expect(panel).toContainText('严重 1 个实例 · 警告 1 个实例')
  const rows = page.getByTestId('dash-insp-row')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText('prod-bad')
  await expect(rows.nth(0)).toContainText('t_order 分区将用尽')
  await expect(rows.nth(1)).toContainText('prod-warn')
  await rows.nth(1).click()
  await expect(page).toHaveURL(/#\/inspection\?instance=2&report=12/)
})

test('用户后台总览:今天全部正常时不加提示;昨天的异常不算', async ({ page }) => {
  await dashboard(page, ADMIN, { today: TODAY, items: [LATEST.items[0], { ...LATEST.items[2], report: rep(13, 3, '2026-10-14', 'critical') }] })
  await expect(page.locator('.stats')).toBeVisible()
  await page.waitForTimeout(300)
  await expect(page.getByTestId('dash-insp')).toHaveCount(0)
  await expect(page.getByTestId('dash-insp-stat')).toHaveCount(0)
})

test('用户后台总览:看不了巡检报告的角色没有提示', async ({ page }) => {
  await dashboard(page, { ...ME, id: 6, roleCode: 'ro', roleCodes: ['ro'], menus: { dashboard: true, terminal: true } }, LATEST)
  await expect(page.locator('.stats')).toBeVisible()
  await page.waitForTimeout(300)
  await expect(page.getByTestId('dash-insp')).toHaveCount(0)
})
