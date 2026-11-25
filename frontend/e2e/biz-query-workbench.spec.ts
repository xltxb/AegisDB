import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 数据查询页的工作台布局:上下分屏、工具条动作、结果 / 消息 / 历史三个子页签、表格分页。
// 业务逻辑(选库、执行、被拒、草稿)由 biz-query-page.spec.ts 钉住,这里只管新增的交互。

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = (id: number, db: string) => ({
  id, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: db,
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2026-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
})
const fail = (msg: string) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 40300, msg, data: null }) })

async function open(page: Page, exec: (b: any) => any) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [G(1, 'orders_db')], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.route('**/api/v1/bizquery/exec', (r) => r.fulfill(exec(r.request().postDataJSON())))
  await page.goto('/#/query')
}

const rowsOf = (n: number) => Array.from({ length: n }, (_, i) => [String(i + 1), `name-${i + 1}`, '2026-10-21 09:00:00', ''])

test('结果:状态栏说清成功 / 行数 / 耗时;列类型按值推断;超过一页才出分页', async ({ page }) => {
  await open(page, () => envelope({ columns: ['id', 'name', 'created_at', 'memo'], rows: rowsOf(245), truncated: false, maxRows: 1000, timeoutSec: 30, ms: 86, timedOut: false }))
  await page.getByTestId('q-sql').fill('SELECT * FROM customers')
  await page.getByTestId('q-run').click()
  const stat = page.getByTestId('q-stat')
  await expect(stat).toContainText('查询成功')
  await expect(stat).toContainText('返回 245 行')
  await expect(stat).toContainText('86 ms')
  await expect(page.locator('.qg-table thead th')).toHaveCount(5) // # + 4 列
  await expect(page.locator('.qg-table tbody tr')).toHaveCount(100) // 默认每页 100
  await expect(page.locator('.qg-table td.null').first()).toContainText('∅') // 空串不冒充 NULL
  const pager = page.getByTestId('q-pager')
  await expect(pager).toContainText('1 / 3')
  await pager.getByRole('button', { name: '最后一页' }).click()
  await expect(pager).toContainText('3 / 3')
  await expect(page.locator('.qg-table tbody tr')).toHaveCount(45)
  await expect(page.locator('.qg-table tbody tr').first().locator('td').first()).toHaveText('201')
})

test('格式化与清空;保存进常用查询,从历史载入到新页签', async ({ page }) => {
  await open(page, () => envelope({ columns: ['n'], rows: [['1']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 3, timedOut: false }))
  const sql = page.getByTestId('q-sql')
  await sql.fill('select id from t where a = 1 order by id')
  await page.getByTestId('q-format').click()
  await expect(sql).toHaveValue('select id\nfrom t\nwhere a = 1\norder by id')

  await page.getByTestId('q-save').click()
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-stat')).toBeVisible()

  await page.getByTestId('q-sub-history').click()
  await expect(page.getByTestId('q-saved-item')).toHaveCount(1)
  await expect(page.getByTestId('q-history-item')).toHaveCount(1)
  await expect(page.getByTestId('q-history-item')).toContainText('1 行')

  // 当前页签有内容:载入进新页签,不覆盖
  await page.getByTestId('q-saved-item').getByRole('button', { name: '载入' }).click()
  await expect(page.getByTestId('q-tab')).toHaveCount(2)
  await expect(sql).toHaveValue('select id\nfrom t\nwhere a = 1\norder by id')

  await page.getByTestId('q-clear').click()
  await expect(sql).toHaveValue('')
})

test('执行日志:成功与被拒都记下来,带库与 SQL', async ({ page }) => {
  let n = 0
  await open(page, () => (++n === 1
    ? envelope({ columns: ['n'], rows: [['1'], ['2']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 7, timedOut: false })
    : fail('数据查询只允许 SELECT 查询,不能执行 DELETE')))
  const sql = page.getByTestId('q-sql')
  await sql.fill('SELECT 1')
  await sql.press('Control+Enter')
  await expect(page.getByTestId('q-stat')).toBeVisible()
  await sql.fill('DELETE FROM t')
  await sql.press('Control+Enter')
  await expect(page.getByTestId('q-denied')).toContainText('DELETE')

  await page.getByTestId('q-sub-log').click()
  const log = page.getByTestId('q-log').locator('.qlog')
  await expect(log).toHaveCount(2)
  await expect(log.first()).toHaveClass(/err/)
  await expect(log.first()).toContainText('DELETE FROM t')
  await expect(log.nth(1)).toContainText('返回 2 行')
  await expect(log.nth(1)).toContainText('orders_db')
})

test('分割条:拖动改变上半部分高度,双击还原', async ({ page }) => {
  await open(page, () => envelope({ columns: [], rows: [], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 1, timedOut: false }))
  const top = page.locator('.qtop')
  const before = (await top.boundingBox())!.height
  const bar = (await page.locator('.qsplit').boundingBox())!
  await page.mouse.move(bar.x + bar.width / 2, bar.y + bar.height / 2)
  await page.mouse.down()
  await page.mouse.move(bar.x + bar.width / 2, bar.y + 120, { steps: 5 })
  await page.mouse.up()
  const after = (await top.boundingBox())!.height
  expect(after).toBeGreaterThan(before + 60)
  await page.locator('.qsplit').dblclick()
  await expect.poll(async () => Math.round((await top.boundingBox())!.height)).toBe(Math.round(before))
})
