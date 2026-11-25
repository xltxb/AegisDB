import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Web 命令行结果分批显示(不再强限制 200 行):第一批随回执来,提示已显示的范围;
// 文本视图输入 \more、表格视图点「加载更多」取下一批(后端暂存,不重新执行)。

const conn = { id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 3306,
  database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '' }
const rowsOf = (from: number, n: number) => Array.from({ length: n }, (_, i) => [String(from + i)])

const textRows = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).filter(Boolean).join('\n') : ''
})

async function openTerm(page: Page, grid: boolean) {
  const fetched: string[] = []
  if (grid) await page.addInitScript(() => localStorage.setItem('aegisdb_termgrid', '1'))
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([conn])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => r.fulfill(envelope({ intercepted: false, risk: 'low', output: '+ 2,500 rows', rows: 1000, ms: 9,
    columns: ['n'], data: rowsOf(1, 1000), truncated: true, resultId: 'r1', totalRows: 2500 })))
  await page.route((u) => u.pathname.includes('/api/v1/terminal/results/'), (r) => {
    const off = Number(new URL(r.request().url()).searchParams.get('offset'))
    fetched.push(String(off))
    const n = Math.min(1000, 2500 - off)
    return r.fulfill(envelope({ columns: ['n'], data: rowsOf(off + 1, n), offset: off, more: off + n < 2500 }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  return fetched
}

test('文本视图:提示显示范围,\more 取下一批,直到全部加载', async ({ page }) => {
  const fetched = await openTerm(page, false)
  await page.keyboard.type('select n from big;')
  await page.keyboard.press('Enter')
  await expect.poll(() => textRows(page)).toContain('已显示 1–1000 行,共 2500 行')
  await page.keyboard.type('\\more')
  await page.keyboard.press('Enter')
  await expect.poll(() => textRows(page)).toContain('已显示 1–2000 行')
  await page.keyboard.type('\\more')
  await page.keyboard.press('Enter')
  await expect.poll(() => textRows(page)).toContain('已全部加载,共 2500 行')
  expect(fetched).toEqual(['1000', '2000'])
})

test('表格视图:底部「加载更多」把下一批接到表格后面', async ({ page }) => {
  await openTerm(page, true)
  await page.keyboard.type('select n from big;')
  await page.keyboard.press('Enter')
  await expect(page.locator('.rg tbody tr')).toHaveCount(1000)
  await page.getByTestId('grid-load-more').click()
  await expect(page.locator('.rg tbody tr')).toHaveCount(2000)
  await expect(page.locator('.rg tbody tr').nth(1000).locator('td').nth(1)).toHaveText('1001')
})
