import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 导出任务的终止:排队中 / 待审批 / 执行中的任务有「终止」按钮,确认后才发请求;
// 终止后显示"已终止"和谁终止的,可以把参数填回表单重新提交。已结束的任务没有这个按钮。

const CONN = { id: 1, name: 'orders', env: 'prod', engine: 'mysql', host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online' }
const JOB = (id: number, status: string, over: Record<string, unknown> = {}) => ({
  id, userId: 1, connectionId: 1, instance: 'orders', database: 'appdb', sql: 'SELECT * FROM big_table', name: 'big',
  status, includeSensitive: false, approvalId: 0, apNo: '', rows: 0, bytes: 0, parts: 0, files: '', password: '', error: '',
  createdAt: '2026-09-28T10:00:00+08:00', finishedAt: null, ...over,
})

async function open(page: Page, jobs: () => any[]) {
  const cancels: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route('**/api/v1/connections/*/schema*', (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'appdb', tables: [] }] })))
  await page.route('**/api/v1/export/config', (r) => r.fulfill(envelope({ retentionDays: 0 })))
  await page.route('**/api/v1/export/jobs', (r) => r.fulfill(envelope(jobs())))
  await page.route('**/api/v1/export/jobs/*/cancel', (r) => {
    cancels.push(new URL(r.request().url()).pathname)
    return r.fulfill(envelope(JOB(7, 'cancelled', { error: '已被 林伟 终止' })))
  })
  await page.goto('/#/export')
  return cancels
}

test('执行中的任务:确认后终止,显示已终止与终止人', async ({ page }) => {
  let status = 'running'
  const cancels = await open(page, () => [JOB(7, status, status === 'cancelled' ? { error: '已被 林伟 终止' } : {})])
  await page.getByTestId('export-cancel').click()
  await page.getByTestId('confirm-ok').click()
  await expect.poll(() => cancels).toEqual(['/api/v1/export/jobs/7/cancel'])
  status = 'cancelled'
  await page.getByTestId('export-refresh').click()
  await expect(page.locator('.jbadge')).toContainText('已终止')
  await expect(page.locator('.jerr')).toContainText('林伟')
  await expect(page.getByTestId('export-cancel')).toHaveCount(0)
  await expect(page.getByTestId('export-reuse')).toHaveCount(1)
})

test('确认框点了取消:不发终止请求', async ({ page }) => {
  const cancels = await open(page, () => [JOB(7, 'running')])
  await page.getByTestId('export-cancel').click()
  await page.getByTestId('confirm-cancel').click()
  await page.waitForTimeout(300)
  expect(cancels).toEqual([])
})

test('排队中与待审批的也能终止;已结束的没有终止按钮', async ({ page }) => {
  await open(page, () => [JOB(1, 'pending'), JOB(2, 'awaiting'), JOB(3, 'done', { parts: 1, files: 'a.zip', password: 'x' }), JOB(4, 'failed'), JOB(5, 'cancelled')])
  await expect(page.getByTestId('export-cancel')).toHaveCount(2)
})
