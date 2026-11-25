import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 执行耗时每条都拆开显示(取连接 / 库端执行 / 读取),不再只在 ≥ 1 秒时才拆(2026-10 用户要求)。

const conn = { id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 3306,
  database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '' }

const rows = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).filter(Boolean) : []
})

test('快的语句也拆分耗时;没有分段数据时不打', async ({ page }) => {
  let reply: Record<string, unknown> = { intercepted: false, risk: 'low', output: '+ 1 rows', rows: 1, ms: 5, connMs: 1, queryMs: 3, fetchMs: 1, columns: ['x'], data: [['1']] }
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([conn])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => r.fulfill(envelope(reply)))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()

  await page.keyboard.type('select 1;')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await rows(page)).join('\n')).toContain('取连接 1ms · 库端执行 3ms · 读取 1ms')

  reply = { intercepted: false, risk: 'low', output: '+ 1 rows', rows: 1, ms: 4, columns: ['x'], data: [['2']] } // 没有分段
  await page.keyboard.type('select 2;')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await rows(page)).join('\n')).toContain('4ms')
  const last = (await rows(page)).filter((l) => l.includes('4ms')).join('\n')
  expect(last).not.toContain('取连接')
})
