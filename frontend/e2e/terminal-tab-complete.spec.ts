import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Web 命令行的 Tab 补全,在真实浏览器里敲键盘:xterm 把 Tab 交给行编辑器,候选来自
// 实例的库表结构与取列接口。

const CONN = {
  id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'mysql.internal', port: 3306, database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}

async function openTerminal(page: Page) {
  const columnReqs: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [{ name: 'orders' }, { name: 'order_items' }, { name: 'customers' }] }] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/columns'), (r) => {
    const u = new URL(r.request().url())
    columnReqs.push(`${u.searchParams.get('database')}/${u.searchParams.get('table')}`)
    return r.fulfill(envelope({ columns: u.searchParams.get('table') === 'orders' ? ['id', 'customer_id', 'amount'] : [] }))
  })
  await gotoTerminal(page)
  await page.locator('.inst.active', { hasText: CONN.name }).waitFor()
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  // 默认库加载完,提示符变成「实例/库 ❯」再开始敲
  await expect.poll(async () => { await page.keyboard.press('Enter'); return (await rows(page)).at(-1) }).toContain('/shop')
  return columnReqs
}

const rows = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).filter(Boolean) : []
})
const lastRow = async (page: Page) => (await rows(page)).at(-1) || ''

test('表名:唯一候选直接补全', async ({ page }) => {
  await openTerminal(page)
  await page.keyboard.type('SELECT * FROM cus')
  await page.keyboard.press('Tab')
  await expect.poll(() => lastRow(page)).toContain('SELECT * FROM customers')
})

test('多个候选:补到公共前缀,再按一次 Tab 列出来', async ({ page }) => {
  await openTerminal(page)
  await page.keyboard.type('SELECT * FROM ord')
  await page.keyboard.press('Tab')
  await expect.poll(() => lastRow(page)).toMatch(/FROM order$/)
  await page.keyboard.press('Tab')
  await expect.poll(async () => (await rows(page)).join('\n')).toMatch(/order_items\s+orders/)
  await expect.poll(() => lastRow(page)).toMatch(/FROM order$/) // 输入行原样留在下面
})

test('列:表名. 之后补这张表的列,取列带着会话所在的库', async ({ page }) => {
  const reqs = await openTerminal(page)
  await page.keyboard.type('SELECT orders.am')
  await page.keyboard.press('Tab')
  await expect.poll(() => lastRow(page)).toContain('SELECT orders.amount')
  expect(reqs).toEqual(['shop/orders'])
})

test('关键字:跟着人敲的大小写', async ({ page }) => {
  await openTerminal(page)
  await page.keyboard.type('sel')
  await page.keyboard.press('Tab')
  await expect.poll(() => lastRow(page)).toMatch(/❯ select$/)
  await page.keyboard.type('1') // 关键字补全后带一个空格
  await expect.poll(() => lastRow(page)).toMatch(/❯ select 1$/)
})
