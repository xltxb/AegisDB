import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 会话失效时回到登录页。
//
// 后端的鉴权失败不是 HTTP 401,而是 **HTTP 200 + code 40100**(resp.Abort 一律 200)。
// 拦截器原先只认 401,于是 token 过期 / 被吊销之后页面停在"僵尸态":每个请求弹一条
// "Token 失效",人却还留在原页面上,什么也点不动。

test('任一接口回 40100:清掉会话并回到登录页', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/online-users', (r) => r.fulfill(envelope({ users: 0, terminals: 0, sessions: [] })))
  await page.route('**/api/v1/users', (r) => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40100, msg: '会话已失效，请重新登录', data: null }),
  }))
  await page.goto('/#/permissions')

  await expect(page).toHaveURL(/#\/login/)
  expect(await page.evaluate(() => localStorage.getItem('aegisdb_session'))).toBeNull()
})
