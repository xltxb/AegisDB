import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-11-12:数据查询看表的 DDL —— 「DDL」按钮、SHOW CREATE TABLE 结果画成代码块、结尾 \\G 竖排。

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = {
  id: 1, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'UAT-DWS', database: 'c66_gli',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2099-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
}
const DDL = 'CREATE TABLE public.g_ads_order (\n  id bigint NOT NULL,\n  amount numeric(10,2)\n)\nDISTRIBUTE BY HASH (id);'
const ok = (columns: string[], rows: string[][]) => envelope({ columns, rows, truncated: false, maxRows: 1000, timeoutSec: 30, ms: 12, timedOut: false })

async function open(page: Page, sent: string[]) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [G], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: ['g_ads_order', 'g_dim_user'], truncated: false })))
  await page.route('**/api/v1/bizquery/exec', (r) => {
    const sql: string = r.request().postDataJSON().sql
    sent.push(sql)
    if (/^SHOW CREATE TABLE/i.test(sql)) return r.fulfill(ok(['Table', 'Create Table'], [['g_ads_order', DDL]]))
    return r.fulfill(ok(['id', 'name', 'memo'], [['1', 'alice', ''], ['2', 'bob', 'x']]))
  })
  await page.goto('/#/query')
}

test('选了表才能点 DDL;点了发 SHOW CREATE TABLE,不动编辑器,结果画成代码块可复制', async ({ page }) => {
  const sent: string[] = []
  await open(page, sent)
  await expect(page.getByTestId('q-ddl')).toBeDisabled()
  await page.getByTestId('q-sql').fill('SELECT 1')
  await page.getByTestId('q-table').locator('.qsel').click()
  await page.getByText('g_ads_order', { exact: true }).click()
  await page.getByTestId('q-sql').fill('SELECT 1') // 选表会把表名插进编辑器,这里还原成用户自己的内容
  await page.getByTestId('q-ddl').click()
  await expect(page.getByTestId('q-ddl-view')).toContainText('DISTRIBUTE BY HASH (id);')
  await expect(page.getByTestId('q-ddl-view').locator('pre')).toContainText('amount numeric(10,2)')
  expect(sent).toEqual(['SHOW CREATE TABLE `g_ads_order`'])
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT 1')
  await expect(page.getByTestId('q-pager')).toHaveCount(0)
})

test('手写 SHOW CREATE TABLE 也画成代码块', async ({ page }) => {
  const sent: string[] = []
  await open(page, sent)
  await page.getByTestId('q-sql').fill('show create table g_ads_order')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-ddl-view')).toContainText('CREATE TABLE public.g_ads_order')
})

test('结尾 \\G:剥掉再发,结果按行竖排', async ({ page }) => {
  const sent: string[] = []
  await open(page, sent)
  await page.getByTestId('q-sql').fill('SELECT * FROM g_dim_user\\G')
  await page.getByTestId('q-run').click()
  const v = page.getByTestId('q-vertical')
  await expect(v).toContainText('1. row')
  await expect(v).toContainText('2. row')
  await expect(v.locator('.qg-kv').first()).toContainText('id:')
  expect(sent).toEqual(['SELECT * FROM g_dim_user'])
  await expect(page.locator('.qg-table')).toHaveCount(0)

  // 不带 \\G 的下一条照常是表格
  await page.getByTestId('q-sql').fill('SELECT * FROM g_dim_user')
  await page.getByTestId('q-run').click()
  await expect(page.locator('.qg-table')).toBeVisible()
  await expect(page.getByTestId('q-vertical')).toHaveCount(0)
})
