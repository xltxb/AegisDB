import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 管理后台独立空闲超时(工单 09,前端那一半):进入时调 /admin/enter;停在管理后台无操作满
// adminIdleMinutes 退回用户后台(不登出);人有操作时节流报心跳;后端回 40304 同样退回。

async function open(page: Page, adminIdleMinutes: number) {
  const calls: string[] = []
  await page.clock.install()
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/auth/session-policy', (r) => r.fulfill(envelope({ idleLock: true, idleMinutes: 60, sessionTtlMinutes: 480, adminIdleMinutes })))
  await page.route('**/api/v1/admin/enter', (r) => { calls.push('enter'); return r.fulfill(envelope({ ok: true })) })
  await page.route('**/api/v1/admin/heartbeat', (r) => { calls.push('beat'); return r.fulfill(envelope({ ok: true })) })
  const loaded = page.waitForResponse('**/api/v1/auth/session-policy')
  await page.goto('/#/dashboard')
  await loaded
  await page.waitForTimeout(200)
  return calls
}

test('进入管理后台时调 enter;无操作满时长退回用户后台,不登出', async ({ page }) => {
  const calls = await open(page, 1)
  await page.getByTestId('portal-switch').click()
  await expect(page).toHaveURL(/#\/admin\//)
  await expect.poll(() => calls.filter((c) => c === 'enter').length).toBe(1)
  await page.clock.fastForward(61_000)
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByText('管理后台空闲超时').first()).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('aegisdb_session'))).not.toBeNull()
})

test('在管理后台里有操作:节流报心跳', async ({ page }) => {
  const calls = await open(page, 5)
  await page.getByTestId('portal-switch').click()
  await expect(page).toHaveURL(/#\/admin\//)
  await page.mouse.click(600, 400)
  expect(calls.filter((c) => c === 'beat').length).toBe(0) // 刚进入,一分钟内不重复报
  await page.clock.fastForward(61_000)
  await page.mouse.click(600, 400)
  await expect.poll(() => calls.filter((c) => c === 'beat').length).toBe(1)
})

test('后端说管理后台已空闲超时(40304):退回用户后台', async ({ page }) => {
  await open(page, 5)
  await page.route('**/api/v1/roles', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40304, msg: '管理后台已空闲超时,请重新进入', data: null }) }))
  await page.goto('/#/admin/permissions')
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByText('管理后台已空闲超时').first()).toBeVisible()
})
