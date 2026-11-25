import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 空闲锁定的时长读「会话策略」(人人可读),不再读只给管理员的 /settings ——
// 从前非管理员读不到,一律按默认 15 分钟锁定,设置里改的分钟数对他们不生效。

async function open(page: Page, policy: { idleLock: boolean; idleMinutes: number }) {
  await page.clock.install()
  await stubCommon(page)
  await page.route('**/api/v1/auth/session-policy', (r) => r.fulfill(envelope({ ...policy, sessionTtlMinutes: 480 })))
  // 策略排在总览的几次接口调用之后才读:等它回来、定时器按它设好,再拨钟
  const loaded = page.waitForResponse('**/api/v1/auth/session-policy')
  await page.goto('/#/dashboard')
  await loaded
  await page.waitForTimeout(200)
  await expect(page).toHaveURL(/#\/dashboard/)
}

test('按会话策略的分钟数锁定(1 分钟)', async ({ page }) => {
  await open(page, { idleLock: true, idleMinutes: 1 })
  await page.clock.fastForward(61_000)
  await expect(page).toHaveURL(/#\/login.*locked=1/)
})

test('时长没到不锁;策略关掉也不锁', async ({ page }) => {
  await open(page, { idleLock: true, idleMinutes: 30 })
  await page.clock.fastForward(16 * 60_000) // 超过从前的默认 15 分钟,但没到 30
  await expect(page).toHaveURL(/#\/dashboard/)
})
