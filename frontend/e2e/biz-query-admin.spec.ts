import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 业务方数据查询 · 第四期的三处界面:导出按钮、查询授权总表、设置里的全局值。

const G = (id: number, over: Record<string, unknown> = {}) => ({
  id, email: `u${id}@corp.io`, userName: `用户${id}`, dept: '财务部', sourceId: 12, source: 'orders-replica', database: 'orders_db',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2026-10-25T09:00:00+08:00', status: 'active',
  externalId: `OA-${id}`, client: '审批系统', approver: '李经理', reason: '', ...over,
})
const LIMITS = { defaultMaxRows: 1000, maxRowsCap: 10000, defaultTimeoutSec: 30, timeoutCapSec: 300, maxGrantDays: 90, maxConcurrentPerUser: 2, maxConcurrentPerSource: 5, exportEnabled: true }

async function queryPage(page: Page, grant: any, exportEnabled: boolean) {
  const bodies: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { query: true } })))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [grant], limits: { ...LIMITS, exportEnabled } })))
  await page.route('**/api/v1/bizquery/export', (r) => {
    bodies.push(r.request().postDataJSON())
    return r.fulfill({ status: 200, contentType: 'text/csv; charset=utf-8', body: '﻿id\n1\n',
      headers: { 'Content-Disposition': "attachment; filename*=UTF-8''orders_db_20260925_101010.csv" } })
  })
  await page.goto('/#/query')
  await expect(page.getByTestId('q-sql')).toBeVisible()
  return bodies
}

test('导出按钮:全局开关与授权都允许才出现,点了下载服务端给的文件', async ({ page }) => {
  const bodies = await queryPage(page, G(1, { allowExport: true }), true)
  await page.getByTestId('q-sql').fill('SELECT id FROM orders')
  const dl = page.waitForEvent('download')
  await page.getByTestId('q-export').click()
  expect((await dl).suggestedFilename()).toBe('orders_db_20260925_101010.csv')
  expect(bodies).toEqual([{ sourceId: 12, database: 'orders_db', sql: 'SELECT id FROM orders' }])
})

// 2026-11-03:导出与执行交出去的是同一段 —— 有选区导选区,否则光标所在的那条(空行为界)
test('导出:编辑器里有几条时,只导出选中的 / 光标所在的那条', async ({ page }) => {
  const bodies = await queryPage(page, G(1, { allowExport: true }), true)
  const ed = page.getByTestId('q-sql')
  await ed.fill('SELECT id FROM orders\n\nSELECT id FROM refunds')
  await ed.evaluate((el: HTMLTextAreaElement) => el.setSelectionRange(0, 21))
  let dl = page.waitForEvent('download')
  await page.getByTestId('q-export').click()
  await dl
  await ed.evaluate((el: HTMLTextAreaElement) => { el.focus(); el.setSelectionRange(el.value.length, el.value.length) })
  dl = page.waitForEvent('download')
  await page.getByTestId('q-export').click()
  await dl
  expect(bodies.map((b) => b.sql)).toEqual(['SELECT id FROM orders', 'SELECT id FROM refunds'])
})

test('导出:查询本身失败时(code 0 + data.error)显示原因,不当成文件下载', async ({ page }) => {
  await queryPage(page, G(1, { allowExport: true }), true)
  await page.route('**/api/v1/bizquery/export', (r) =>
    r.fulfill(envelope({ error: '导出的查询超过 30 秒,已自动取消', timedOut: true })))
  let downloaded = false
  page.on('download', () => { downloaded = true })
  await page.getByTestId('q-sql').fill('SELECT * FROM big')
  await page.getByTestId('q-export').click()
  await expect(page.getByTestId('q-denied')).toContainText('超过 30 秒')
  expect(downloaded).toBe(false)
})

test('导出:会话失效(401)回登录页,与其他请求一致', async ({ page }) => {
  await queryPage(page, G(1, { allowExport: true }), true)
  await page.route('**/api/v1/bizquery/export', (r) => r.fulfill({ status: 401, contentType: 'application/json', body: '{"code":40100,"msg":"unauthorized"}' }))
  await page.getByTestId('q-sql').fill('SELECT id FROM orders')
  await page.getByTestId('q-export').click()
  await expect(page).toHaveURL(/#\/login/)
})

test('导出按钮:授权没批导出,或全局开关关着,都不出现', async ({ page }) => {
  await queryPage(page, G(1, { allowExport: false }), true)
  await expect(page.getByTestId('q-export')).toHaveCount(0)
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await queryPage(page, G(1, { allowExport: true }), false)
  await expect(page.getByTestId('q-export')).toHaveCount(0)
})

test('查询授权总表:按状态筛选,管理员撤销时要填原因', async ({ page }) => {
  const queries: string[] = []
  const revokes: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  await page.route(/\/api\/v1\/query-grants(\?.*)?$/, (r) => {
    queries.push(new URL(r.request().url()).search)
    return r.fulfill(envelope({ items: [G(1), G(2, { status: 'expired', allowExport: true })], limits: LIMITS }))
  })
  await page.route(/\/api\/v1\/query-grants\/\d+\/revoke$/, (r) => {
    revokes.push({ url: r.request().url(), body: r.request().postDataJSON() })
    return r.fulfill(envelope(G(1, { status: 'revoked' })))
  })
  await page.goto('/#/permissions')
  await page.getByTestId('tab-grants').click()
  await expect(page.getByTestId('qg-row')).toHaveCount(2)
  expect(queries[0]).toContain('status=active') // 默认只看生效中的
  await page.getByTestId('qg-status').selectOption('')
  await expect.poll(() => queries.length).toBe(2)
  expect(queries[1]).not.toContain('status=')
  // 已过期的不给撤销按钮
  await expect(page.getByTestId('qg-row').nth(1).getByTestId('qg-revoke')).toHaveCount(0)
  page.once('dialog', (d) => d.accept('离职'))
  await page.getByTestId('qg-row').first().getByTestId('qg-revoke').click()
  await expect.poll(() => revokes.length).toBe(1)
  expect(revokes[0].url).toMatch(/\/query-grants\/1\/revoke$/)
  expect(revokes[0].body).toEqual({ reason: '离职' })
})

test('设置 · 数据查询:保存带上全局值;服务端拒绝时说原因,不弹已保存', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  for (const p of ['**/api/v1/api-clients', '**/api/v1/service-accounts', '**/api/v1/approval-chain']) {
    await page.route(p, (r) => r.fulfill(envelope(p.endsWith('approval-chain') ? { chain: [] } : [])))
  }
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: { 'bizQuery.maxRowsCap': '10000' }, secretsSet: {} }))
    saved.push(r.request().postDataJSON())
    return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 40001, msg: '默认行数 必须在 1 ~ 10000 之间,当前 20000', data: null }) })
  })
  await page.goto('/#/settings')
  await page.locator('.tabs .tab', { hasText: '数据查询' }).click()
  await page.getByTestId('biz-default-rows').fill('20000')
  await page.getByTestId('biz-daily').fill('50')
  await page.getByRole('button', { name: /保存/ }).first().click()
  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]['bizQuery.defaultMaxRows']).toBe(20000)
  expect(saved[0]['bizQuery.maxDailyPerUser']).toBe(50)
  expect(saved[0]['bizQuery.exportEnabled']).toBe(false)
  await expect(page.getByText('默认行数 必须在')).toBeVisible()
  await expect(page.getByText('设置已保存')).toHaveCount(0)
})
