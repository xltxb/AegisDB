import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 进入终端只是进页面(2026-10 用户要求):不替人挑实例、不开标签、不建连。从前默认打开 prod
// 实例的标签 —— 每个标签独占一条数据库连接,路过一下终端页就在生产库上占了一条。
test('进入终端页:没有标签、不建连,点了实例才打开', async ({ page }) => {
  const sockets: string[] = []
  // 只数网关的终端连接(/terminal/ws);Vite 开发服务器自己的热更新连接不算
  page.on('websocket', (ws) => { if (ws.url().includes('/terminal/ws')) sockets.push(ws.url()) })
  const execs: unknown[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([
    { id: 1, name: 'ORDER-PROD', env: 'prod', engine: 'MySQL 8.0', host: 'h', port: 3306, database: 'shop', defaultRole: 'dba_l2', status: 'online' },
  ])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/terminal/exec', (r) => { execs.push(r.request().postDataJSON()); return r.fulfill(envelope({ output: '' })) })

  await page.goto('/#/terminal')
  await expect(page.locator('.inst', { hasText: 'ORDER-PROD' })).toBeVisible()
  await expect(page.getByText('从左侧选择数据库实例打开标签页')).toBeVisible()
  await expect(page.locator('.xterm')).toHaveCount(0)
  await page.waitForTimeout(500)
  expect(sockets, '没选实例前不该建任何连接').toHaveLength(0)
  expect(execs).toHaveLength(0)

  await page.locator('.inst', { hasText: 'ORDER-PROD' }).click()
  await expect(page.locator('.xterm:visible')).toHaveCount(1)
  await expect(page.getByText('从左侧选择数据库实例打开标签页')).toHaveCount(0)
  await expect.poll(() => sockets.length, { message: '点了实例才建终端连接' }).toBeGreaterThan(0)
})
