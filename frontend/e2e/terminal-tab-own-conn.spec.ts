import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 同一台实例新开一个标签页,网关要给它新开一条数据库连接,不复用别的标签的。前端这一侧的约定:
//   · 每个标签页有自己的会话标识,随每次执行带上(sessionId)
//   · 同一个标签里多次执行,标识不变;两个标签的标识不同
//   · 关掉标签页时通知网关释放这条连接

const conn = { id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 3306,
  database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '' }

async function openTerm(page: Page) {
  const seen: string[] = []
  const closed: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([conn])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    seen.push(r.request().postDataJSON().sessionId)
    return r.fulfill(envelope({ intercepted: false, risk: 'low', output: '+ 1 rows', rows: 1, ms: 3, columns: ['x'], data: [['1']] }))
  })
  await page.route('**/api/v1/terminal/sessions/close', (r) => {
    closed.push(r.request().postDataJSON().sessionId)
    return r.fulfill(envelope({ ok: true }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  return { seen, closed }
}

async function run(page: Page, sql: string) {
  await page.locator('.xterm:visible').click()
  await page.keyboard.type(sql)
  await page.keyboard.press('Enter')
}

test('同一实例的两个标签:各带各的会话标识;关掉标签就释放它的连接', async ({ page }) => {
  const { seen, closed } = await openTerm(page)
  await run(page, 'select 1;')
  await expect.poll(() => seen.length).toBe(1)
  await run(page, 'select 2;')
  await expect.poll(() => seen.length).toBe(2)
  expect(seen[0]).toMatch(/^[0-9a-f-]{16,64}$/i)
  expect(seen[1]).toBe(seen[0]) // 同一个标签,同一个会话

  await page.locator('.tabspacer').dblclick() // 同一台实例再开一个标签
  await expect(page.locator('.tab')).toHaveCount(2)
  await page.locator('.xterm:visible').waitFor()
  await run(page, 'select 3;')
  await expect.poll(() => seen.length).toBe(3)
  expect(seen[2]).not.toBe(seen[0]) // 新标签,新会话 → 新连接

  await page.locator('.tab').nth(1).locator('.close').click()
  await expect(page.locator('.tab')).toHaveCount(1)
  await expect.poll(() => closed).toEqual([seen[2]]) // 只释放关掉的那个标签
})
