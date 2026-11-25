import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 2026-10-29:导出的文件名必填;列表第一行是文件名;下载过的分卷标「已下载」,按钮变「再次下载」。

const CONN = { id: 1, name: 'orders', env: 'dev', engine: 'mysql', host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online' }
const JOB = (id: number, over: Record<string, unknown> = {}) => ({
  id, userId: 1, connectionId: 1, instance: 'orders', database: 'appdb', sql: 'SELECT * FROM t', name: '10月订单明细',
  status: 'done', includeSensitive: false, approvalId: 0, apNo: '', rows: 10, bytes: 2048, parts: 1, files: '/data/export/a-1.zip',
  password: 'pw', error: '', createdAt: '2026-10-29T10:00:00+08:00', finishedAt: '2026-10-29T10:00:05+08:00', ...over,
})

async function open(page: Page, jobs: () => any[]) {
  const posts: any[] = []
  const downloads: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route('**/api/v1/connections/*/schema*', (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'appdb', tables: [] }] })))
  await page.route('**/api/v1/export/config', (r) => r.fulfill(envelope({ retentionDays: 0 })))
  await page.route('**/api/v1/export/jobs', (r) => r.fulfill(envelope(jobs())))
  await page.route('**/api/v1/export/download*', (r) => {
    downloads.push(new URL(r.request().url()).searchParams.get('file') || '')
    return r.fulfill({ status: 200, contentType: 'application/zip', body: 'zip' })
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/export'), (r) => {
    posts.push(r.request().postDataJSON())
    return r.fulfill(envelope({ id: 9, status: 'pending' }))
  })
  await page.goto('/#/export')
  return { posts, downloads }
}

test('文件名必填:不填不提交、框标红;填了随请求带上', async ({ page }) => {
  const { posts } = await open(page, () => [])
  await page.locator('.vsel .control').click()
  await page.locator('.vsel').getByText('orders').first().click()
  await page.locator('textarea.sqlarea').fill('SELECT 1 FROM t')
  await page.locator('.submit').click()
  await expect(page.getByText('请填写文件名')).toBeVisible()
  await expect(page.getByTestId('export-name')).toHaveClass(/invalid/)
  await page.waitForTimeout(200)
  expect(posts).toEqual([])

  await page.getByTestId('export-name').fill('  10月订单明细 ')
  await expect(page.getByTestId('export-name')).not.toHaveClass(/invalid/)
  await page.locator('.submit').click()
  await expect.poll(() => posts.length).toBe(1)
  expect(posts[0].name).toBe('10月订单明细')
})

test('列表:第一行是文件名(老任务显示未命名);下载过的标「已下载」,按钮变「再次下载」', async ({ page }) => {
  let downloaded = ''
  const { downloads } = await open(page, () => [
    JOB(3, { downloadedFiles: downloaded, downloadedAt: downloaded ? '2026-10-29T11:00:00+08:00' : null }),
    JOB(2, { name: '', parts: 2, files: '/data/export/b-1.zip\n/data/export/b-2.zip', downloadedFiles: '/data/export/b-1.zip', downloadedAt: '2026-10-29T09:00:00+08:00' }),
  ])
  const rows = page.locator('.jrow')
  await expect(rows.nth(0).getByTestId('job-name')).toHaveText('10月订单明细')
  await expect(rows.nth(1).getByTestId('job-name')).toHaveText('未命名')

  // 多分卷只下过一个:标 1/2,按钮仍是「下载文件」
  await expect(rows.nth(1).getByTestId('job-downloaded')).toContainText('已下载 1/2')
  await expect(rows.nth(1).getByTestId('export-download')).toContainText('下载文件')

  // 没下过:没有标记;下载之后服务端记下,刷新出标记与「再次下载」
  await expect(rows.nth(0).getByTestId('job-downloaded')).toHaveCount(0)
  downloaded = '/data/export/a-1.zip'
  await rows.nth(0).getByTestId('export-download').click()
  await expect.poll(() => downloads).toEqual(['/data/export/a-1.zip'])
  await expect(rows.nth(0).getByTestId('job-downloaded')).toContainText('已下载')
  await expect(rows.nth(0).getByTestId('export-download')).toContainText('再次下载')
})
