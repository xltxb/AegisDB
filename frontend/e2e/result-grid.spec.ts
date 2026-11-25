import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 表格视图:结果里有几百行的多行长文本(DWS pgxc_wlm_session_info 的 query / query_plan)时,
// 曾把整行撑到几千像素高,其它列的值垂直居中、落在屏幕下方很远处 —— 表头下面一片空白,
// 命令行模式却有数据(2026-10 生产反馈)。现在单元格只占一行,换行显示成 ↵,完整值在悬停提示里。

const conn = { id: 1, name: 'dws-dev', engine: 'GaussDB (DWS)', env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 1,
  database: 'postgres', policy: 'audit-only', defaultRole: 'dba_l2', tags: '' }
const longSql = 'SELECT *\n  FROM t\n WHERE x = 1\n'.repeat(120)

async function openTerm(page: Page) {
  await page.addInitScript(() => localStorage.setItem('aegisdb_termgrid', '1')) // 表格视图打开
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([conn])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => r.fulfill(envelope({ intercepted: false, risk: 'low', output: '+ 1 rows', rows: 1, ms: 5,
    columns: ['datid', 'username', 'query'], data: [['2147500', 'archery', longSql]] })))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
}

test('多行长文本不把表格行撑高,其它列的值就在表头下面', async ({ page }) => {
  await openTerm(page)
  await page.keyboard.type('select * from pgxc_wlm_session_info;')
  await page.keyboard.press('Enter')
  const row = page.locator('.rg tbody tr').first()
  await expect(row).toBeVisible()
  const rb = (await row.boundingBox())!
  expect(rb.height).toBeLessThan(60) // 单行高度,不再被几百行文本撑开

  const scroll = (await page.locator('.rg .rgscroll').boundingBox())!
  const user = row.locator('td').nth(2)
  await expect(user).toHaveText('archery')
  expect((await user.boundingBox())!.y - scroll.y).toBeLessThan(80) // 就在表头下面,看得见

  const q = row.locator('td').nth(3)
  await expect(q).toContainText('↵')
  expect(await q.getAttribute('title')).toBe(longSql) // 完整值在悬停提示里
})
