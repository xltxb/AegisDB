import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 终端的库列表是打开会话时拉的。会话开着的时候 DBA 新建了一个库,`use` 它不该报
// "不在当前实例的库列表中" —— SHOW DATABASES 里明明有。拒绝之前先重拉一次库列表。

const CONN = {
  id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'mysql.internal', port: 3306, database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}

const rows = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).filter(Boolean) : []
})

test('会话开着时新建的库:use 之前重拉库列表,能切过去', async ({ page }) => {
  let created = false // 打开会话之后才"建库"
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }, ...(created ? [{ name: 'newdb', tables: [] }] : [])] })))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  await expect.poll(async () => { await page.keyboard.press('Enter'); return (await rows(page)).at(-1) }).toContain('/shop')

  created = true
  await page.keyboard.type('use newdb;')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await rows(page)).join('\n')).not.toContain('未知数据库')
  await expect.poll(async () => { await page.keyboard.press('Enter'); return (await rows(page)).at(-1) }).toContain('/newdb')
})
