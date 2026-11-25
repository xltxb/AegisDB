import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理总览(管理后台首页,工单 06):实例连通状态(异常置顶)、待审批(最久的在前)、
// 最近高危操作、在线用户。

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const CONNS = [
  { id: 1, name: 'prod-ok', engine: 'MySQL 8.0', env: 'prod', status: 'online', lastCheckOk: true, lastCheckAt: ago(3) },
  { id: 2, name: 'prod-down', engine: 'Oracle 19c', env: 'prod', status: 'online', lastCheckOk: false, lastCheckAt: ago(2), lastCheckErr: 'dial tcp: i/o timeout' },
  { id: 3, name: 'prod-maint', engine: 'TiDB', env: 'prod', status: 'maint' },
  { id: 4, name: 'prod-new', engine: 'PostgreSQL 16', env: 'prod', status: 'online' },
]
const ap = (id: number) => ({ id, apNo: 'AP-' + id, initiator: 'Chen Hao', instance: 'prod-ok', database: 'shop', command: 'DROP TABLE t' + id, createdAt: ago(100 - id), status: 'pending', steps: [] })

async function open(page: Page, opts: { checkOn: boolean }) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: { 'conn.check.enabled': JSON.stringify(opts.checkOn) }, secretsSet: {} })))
  // 7 张待审批,按 id 倒序分页:最后一页是最久的
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => {
    const q = new URL(r.request().url()).searchParams
    const size = Number(q.get('pageSize')), pg = Number(q.get('page'))
    const all = [7, 6, 5, 4, 3, 2, 1].map(ap)
    return r.fulfill(envelope({ items: all.slice((pg - 1) * size, pg * size), total: 7, pending: 7 }))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope({ items: [
    { id: 9, occurredAt: ago(30), actor: 'Zhang Wei', instance: 'prod-ok', database: 'shop', command: 'TRUNCATE orders', risk: 'high', result: 'executed' },
  ], total: 1, page: 1, pageSize: 8, groups: {} })))
  await page.route('**/api/v1/online-users', (r) => r.fulfill(envelope({ sessions: [], users: 0, terminals: 0, windowSec: 600 })))
  await page.goto('/#/admin/overview')
}

// 布局(2026-10):异常实例置顶列出,正常的默认收起成一个数字,展开才逐个列 —— 从前 90 多个
// 实例逐行列到底,真正要看的几个失败的被淹没在里面。
test('实例按健康度排:只列异常(失败、维护、未巡检),正常的收起;巡检关着时给提示', async ({ page }) => {
  await open(page, { checkOn: false })
  const rows = page.getByTestId('ao-conn')
  await expect(rows).toHaveCount(3)
  await expect(rows.nth(0)).toContainText('prod-down')
  await expect(rows.nth(0)).toContainText('巡检失败')
  await expect(rows.nth(1)).toContainText('prod-maint')
  await expect(rows.nth(2)).toContainText('prod-new')
  await expect(rows.nth(2)).toContainText('未巡检')
  await expect(rows.filter({ hasText: 'prod-ok' })).toHaveCount(0)
  await page.getByTestId('ao-toggle-ok').click()
  await expect(rows).toHaveCount(4)
  await expect(rows.nth(3)).toContainText('prod-ok')
  await page.getByTestId('ao-toggle-ok').click()
  await expect(rows).toHaveCount(3)
  await expect(page.getByTestId('ao-stat-conn')).toContainText('1')
  await expect(page.getByTestId('ao-check-off')).toBeVisible()
})

test('待审批显示最久的几张(最久的在前);高危操作列出来;在线用户在这一页', async ({ page }) => {
  await open(page, { checkOn: true })
  await expect(page.getByTestId('ao-check-off')).toHaveCount(0)
  await expect(page.getByTestId('ao-stat-pending')).toContainText('7')
  const pend = page.getByTestId('ao-pending')
  await expect(pend).toHaveCount(5)
  await expect(pend.nth(0)).toContainText('AP-1')
  await expect(pend.nth(4)).toContainText('AP-5')
  await expect(page.getByTestId('ao-high')).toContainText('TRUNCATE orders')
  await expect(page.getByTestId('online-summary')).toBeVisible()
})

test('实例全部正常:一句话说清,不逐个列出', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONNS[0], { ...CONNS[0], id: 5, name: 'prod-ok-2' }])))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: { 'conn.check.enabled': 'true' }, secretsSet: {} })))
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => r.fulfill(envelope({ items: [], total: 0, pending: 0 })))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 8, groups: {} })))
  await page.route('**/api/v1/online-users', (r) => r.fulfill(envelope({ sessions: [], users: 0, terminals: 0, windowSec: 600 })))
  await page.goto('/#/admin/overview')
  await expect(page.getByTestId('ao-all-ok')).toContainText('全部 2 个实例巡检正常')
  await expect(page.getByTestId('ao-conn')).toHaveCount(0)
})
