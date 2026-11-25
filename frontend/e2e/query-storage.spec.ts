import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-26 回归排查:历史、常用查询与页签草稿同在一个 localStorage 键里,历史每条存整段 SQL 且不去重 ——
// 配额一满 setItem 就一直失败,刷新后草稿丢失。现在:常用查询单独一个键;连着执行同一条只记一次;
// 写不下时先丢历史、保草稿。

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = { id: 1, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: 'orders_db',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2027-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账' }

async function open(page: Page, init?: () => void) {
  await stubCommon(page)
  if (init) await page.addInitScript(init)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [G], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.route('**/api/v1/bizquery/exec', (r) => r.fulfill(envelope({ columns: ['id'], rows: [['1']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 5, timedOut: false })))
  await page.goto('/#/query')
  await page.getByTestId('q-sql').waitFor()
}

const stored = (page: Page) => page.evaluate(() => JSON.parse(localStorage.getItem('aegisdb_query_tabs_9') || 'null'))

test('连着执行同一条只记一条历史;常用查询存在自己的键里', async ({ page }) => {
  await open(page)
  await page.getByTestId('q-sql').fill('SELECT 1')
  for (let i = 0; i < 3; i++) {
    await page.getByTestId('q-run').click()
    await expect(page.getByTestId('q-stat')).toContainText('查询成功')
  }
  await page.getByTestId('q-save').click()
  await expect.poll(async () => (await stored(page))?.history?.length).toBe(1)
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('aegisdb_query_saved_9') || '[]').length)).toBe(1)
})

test('老格式的常用查询照样读得出来', async ({ page }) => {
  await open(page, () => {
    if (sessionStorage.getItem('seeded')) return
    sessionStorage.setItem('seeded', '1')
    localStorage.setItem('aegisdb_query_tabs_9', JSON.stringify({ active: 1, tabs: [{ id: 1, title: 'q', grantId: 1, sql: 'SELECT draft' }],
      history: [], saved: [{ sql: 'SELECT fav_old', grantId: 1, db: 'orders_db', at: 1 }] }))
  })
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT draft')
  await expect.poll(() => page.evaluate(() => localStorage.getItem('aegisdb_query_saved_9') || '')).toContain('fav_old')
})

test('存储配额不够:先丢历史,草稿照样存下', async ({ page }) => {
  await open(page, () => {
    // 模拟配额:单个值超过 3000 字符就写失败
    const orig = Storage.prototype.setItem
    Storage.prototype.setItem = function (k: string, v: string) {
      if (k.startsWith('aegisdb_query_tabs_') && v.length > 3000) throw new DOMException('quota', 'QuotaExceededError')
      return orig.call(this, k, v)
    }
  })
  const ed = page.getByTestId('q-sql')
  for (let i = 0; i < 6; i++) {
    await ed.fill(`SELECT '${'x'.repeat(800)}' AS v${i}`)
    await page.getByTestId('q-run').click()
    await expect(page.getByTestId('q-stat')).toContainText('查询成功')
  }
  await ed.fill('SELECT draft_marker')
  await expect.poll(async () => (await stored(page))?.tabs?.[0]?.sql).toBe('SELECT draft_marker')
  expect((await stored(page)).history.length).toBeLessThan(6)
})

test('常用查询单条超长:拒收并提示,不截断、不入库;正常长度照样能收', async ({ page }) => {
  await open(page)
  const ed = page.getByTestId('q-sql')
  const long = `SELECT '${'x'.repeat(20_050)}' AS v`
  await ed.fill(long)
  await page.getByTestId('q-save').click()
  await expect(page.getByText('太长不能收藏到常用查询')).toBeVisible()
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('aegisdb_query_saved_9') || '[]').length)).toBe(0)
  // 回归:正常长度的照样收进去
  await ed.fill('SELECT 2')
  await page.getByTestId('q-save').click()
  await expect(page.getByText('已保存到常用查询')).toBeVisible()
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('aegisdb_query_saved_9') || '[]').map((h: any) => h.sql))).toEqual(['SELECT 2'])
})
