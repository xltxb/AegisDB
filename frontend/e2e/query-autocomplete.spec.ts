import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-26 用户反馈数据查询页两处:
//   1. 授权的库很多时,「库」下拉菜单底部被下半屏的结果区盖住,滑到底也看不到最后几个;
//   2. 编辑器没有自动补全。
// 补全只在弹框开着时接管 ↑ ↓ Tab Enter Esc;关着时 Tab 缩进、Esc→Tab、Ctrl+Enter 执行照旧。

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = (id: number, db: string) => ({
  id, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: db,
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2027-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
})

async function open(page: Page, grants = [G(1, 'orders_db')]) {
  const execs: any[] = []
  const colCalls: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants, limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: ['orders', 'order_items', 'customers'], truncated: false, quote: '`' })))
  await page.route('**/api/v1/bizquery/columns**', (r) => {
    const u = new URL(r.request().url())
    colCalls.push(u.searchParams.get('table') || '')
    return r.fulfill(envelope({ columns: u.searchParams.get('table') === 'orders' ? ['id', 'amount', 'created_at', 'brand'] : [] }))
  })
  await page.route('**/api/v1/bizquery/exec', (r) => {
    execs.push(r.request().postDataJSON())
    return r.fulfill(envelope({ columns: ['id'], rows: [['1']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 5, timedOut: false }))
  })
  await page.goto('/#/query')
  await page.getByTestId('q-sql').waitFor()
  return { execs, colCalls }
}

test('库很多时下拉菜单完整显示在视口里,滑到底能选中最后一个', async ({ page }) => {
  const grants = Array.from({ length: 30 }, (_, i) => G(i + 1, `db_${String(i + 1).padStart(2, '0')}`))
  await open(page, grants)
  await page.getByTestId('q-db').locator('.control').click()
  const menu = page.getByTestId('q-db').locator('.menu')
  await expect(menu).toBeVisible()
  const vh = page.viewportSize()!.height
  const box = (await menu.boundingBox())!
  expect(box.y + box.height).toBeLessThanOrEqual(vh + 1) // 菜单整体在视口里
  const last = menu.locator('.opt', { hasText: 'db_30' })
  await last.scrollIntoViewIfNeeded()
  await expect(last).toBeInViewport()
  await last.click()
  await expect(page.getByTestId('q-db').locator('.val')).toContainText('db_30')
})

test('补全:关键字、FROM 后的表、别名后的列;Enter / Tab 接受', async ({ page }) => {
  const { colCalls } = await open(page)
  const ed = page.getByTestId('q-sql')
  const ac = page.getByTestId('sql-ac')
  await ed.click()
  await page.keyboard.type('sel')
  await expect(ac).toBeVisible()
  await expect(ac).toContainText('select')
  await page.keyboard.press('Enter')
  await expect(ed).toHaveValue('select')
  await expect(ac).toHaveCount(0)

  await page.keyboard.type(' o.id FROM ord')
  await expect(ac).toContainText('orders')
  await expect(ac).toContainText('order_items')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowUp')
  await page.keyboard.press('Tab')
  await expect(ed).toHaveValue('select o.id FROM order_items')

  await ed.fill('SELECT o.')
  await ed.press('End')
  await page.keyboard.type('a') // 触发
  await page.keyboard.press('Backspace')
  await ed.fill('SELECT * FROM orders o WHERE o.')
  await ed.press('End')
  await page.keyboard.type('am')
  await expect(ac).toContainText('amount')
  await page.keyboard.press('Enter')
  await expect(ed).toHaveValue('SELECT * FROM orders o WHERE o.amount')
  expect(colCalls).toContain('orders')
})

test('敲完的词与唯一候选相同时不弹:回车照常换行;Esc 关掉弹框', async ({ page }) => {
  await open(page)
  const ed = page.getByTestId('q-sql')
  const ac = page.getByTestId('sql-ac')
  await ed.click()
  await page.keyboard.type('SELECT')
  await page.waitForTimeout(300)
  await expect(ac).toHaveCount(0)
  await page.keyboard.press('Enter')
  await expect(ed).toHaveValue('SELECT\n')

  await page.keyboard.type('FROM cus')
  await expect(ac).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(ac).toHaveCount(0)
  await expect(ed).toHaveValue('SELECT\nFROM cus')
  // Ctrl+Space 手动唤出
  await page.keyboard.press('Control+Space')
  await expect(ac).toContainText('customers')
  await ac.locator('.se-ac-item', { hasText: 'customers' }).click()
  await expect(ed).toHaveValue('SELECT\nFROM customers')
})

// 2026-10-27:敲名字中间 / 后面的一段也能补;敲完的已是完整关键字(AND 含在别的名字里)照旧不弹
test('补全:包含匹配(敲 items 补出 order_items、敲 _at 补出 created_at);完整关键字不弹', async ({ page }) => {
  await open(page)
  const ed = page.getByTestId('q-sql')
  const ac = page.getByTestId('sql-ac')
  await ed.click()
  await page.keyboard.type('SELECT * FROM items')
  await expect(ac).toContainText('order_items')
  await page.keyboard.press('Enter')
  await expect(ed).toHaveValue('SELECT * FROM order_items')

  await ed.fill('SELECT * FROM orders WHERE ')
  await ed.press('End')
  await page.keyboard.type('_at')
  await expect(ac).toContainText('created_at')
  await page.keyboard.press('Tab')
  await expect(ed).toHaveValue('SELECT * FROM orders WHERE created_at')

  // AND 含在列 brand 里,但敲的已是完整关键字:不追加包含匹配,不弹,回车照常换行
  await page.keyboard.type(' > 1 AND')
  await page.waitForTimeout(300)
  await expect(ac).toHaveCount(0)
  await page.keyboard.press('Enter')
  await expect(ed).toHaveValue('SELECT * FROM orders WHERE created_at > 1 AND\n')
})

test('回归:弹框关着时 Tab 缩进、Esc→Tab 移走焦点、Ctrl+Enter 执行照旧', async ({ page }) => {
  const { execs } = await open(page)
  const ed = page.getByTestId('q-sql')
  await ed.fill('SELECT 1')
  await ed.press('End')
  await ed.press('Tab')
  await expect(ed).toHaveValue('SELECT 1  ')
  await ed.press('Control+Enter')
  await expect.poll(() => execs.length).toBe(1)
  await ed.press('Escape')
  await ed.press('Tab')
  await expect(ed).not.toBeFocused()
})
