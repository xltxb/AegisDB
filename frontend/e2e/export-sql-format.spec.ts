import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 导出格式:CSV 之外可以选 SQL。选 SQL 时要有目标表名 —— 单表查询自动识别(提示里写出来,
// 留空就用它),联表 / 子查询识别不出,提示必须填写;提交带上 format 与 targetTable。

const CONN = { id: 1, name: 'orders', env: 'dev', engine: 'mysql', host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online' }

async function openPage(page: Page) {
  const bodies: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route('**/api/v1/connections/*/schema*', (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'appdb', tables: [] }] })))
  await page.route('**/api/v1/export/config', (r) => r.fulfill(envelope({ enabled: true, savePath: '/data/export' })))
  await page.route('**/api/v1/export/jobs', (r) => r.fulfill(envelope([])))
  await page.route((u) => u.pathname.endsWith('/api/v1/export'), (r) => {
    bodies.push(r.request().postDataJSON())
    return r.fulfill(envelope({ id: 9, status: 'pending', format: 'sql' }))
  })
  await page.goto('/#/export')
  await page.locator('.vsel .control').click()
  await page.locator('.vsel').getByText('orders').first().click()
  await page.getByTestId('export-name').fill('rpt') // 文件名必填(2026-10-29)
  return bodies
}

test('选 SQL:单表查询自动识别目标表,提交带上 format', async ({ page }) => {
  const bodies = await openPage(page)
  await page.locator('textarea.sqlarea').fill('SELECT id, name FROM t_member WHERE id > 10')
  await page.getByTestId('export-format-sql').click()
  await expect(page.getByTestId('export-target')).toHaveAttribute('placeholder', /t_member/)
  await page.locator('.submit').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].format).toBe('sql')
  expect(bodies[0].targetTable).toBe('') // 留空 = 用识别出的表,由服务端定
})

test('联表查询识别不出目标表:提示必须填写,填了就照填的提交', async ({ page }) => {
  const bodies = await openPage(page)
  await page.locator('textarea.sqlarea').fill('SELECT a.id FROM t_a a JOIN t_b b ON a.id = b.id')
  await page.getByTestId('export-format-sql').click()
  await expect(page.getByTestId('export-target')).toHaveAttribute('placeholder', /请填写/)
  await page.getByTestId('export-target').fill('t_ab')
  await page.locator('.submit').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].targetTable).toBe('t_ab')
})

test('默认仍是 CSV,不出现目标表输入框', async ({ page }) => {
  const bodies = await openPage(page)
  await expect(page.getByTestId('export-target')).toHaveCount(0)
  await page.locator('textarea.sqlarea').fill('SELECT 1 FROM t')
  await page.locator('.submit').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].format ?? 'csv').toBe('csv')
})
