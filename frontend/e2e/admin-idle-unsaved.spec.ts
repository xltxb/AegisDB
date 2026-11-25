import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-26 回归排查 10:管理后台空闲到点时
//   · 当前页有没保存的改动 → 遮罩锁住当前页(不可操作),「重新进入」(POST /admin/enter)后原地解锁,内容还在;
//   · 没有改动 → 照旧弹回总览(admin-idle.spec 里那条也钉着)。

async function open(page: Page, path: string, enterCode = 0) {
  const calls: string[] = []
  await page.clock.install()
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/auth/session-policy', (r) => r.fulfill(envelope({ idleLock: true, idleMinutes: 60, sessionTtlMinutes: 480, adminIdleMinutes: 1 })))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {}, adminIpAllowlistForcedOff: false })))
  await page.route('**/api/v1/admin/enter', (r) => {
    calls.push('enter')
    const code = calls.filter((c) => c === 'enter').length > 1 ? enterCode : 0 // 第一次是路由进门
    return r.fulfill({ status: 200, contentType: 'application/json',
      body: JSON.stringify({ code, msg: code ? '当前 IP 不在管理后台白名单内' : 'ok', data: code ? null : { ok: true } }) })
  })
  await page.route('**/api/v1/admin/heartbeat', (r) => { calls.push('beat'); return r.fulfill(envelope({ ok: true })) })
  const loaded = page.waitForResponse('**/api/v1/auth/session-policy')
  await page.goto('/#' + path)
  await loaded
  await page.waitForTimeout(200)
  return calls
}

const timeoutInput = (page: Page) => page.locator('input.lkin[type="number"]').first()

test('系统设置改到一半空闲到点:锁住当前页,重新进入后原地继续,改动还在', async ({ page }) => {
  const calls = await open(page, '/admin/settings')
  await expect(timeoutInput(page)).toBeVisible()
  await timeoutInput(page).fill('77')
  await page.clock.fastForward(61_000)
  await expect(page.getByTestId('admin-idle-lock')).toBeVisible()
  await expect(page).toHaveURL(/#\/admin\/settings/)
  // 底下的页面不可操作
  expect(await page.locator('.shell').evaluate((el) => (el as HTMLElement).inert)).toBe(true)
  const before = calls.filter((c) => c === 'enter').length
  await page.getByTestId('admin-idle-lock-resume').click()
  await expect(page.getByTestId('admin-idle-lock')).toHaveCount(0)
  expect(calls.filter((c) => c === 'enter').length).toBe(before + 1)
  await expect(page).toHaveURL(/#\/admin\/settings/)
  await expect(timeoutInput(page)).toHaveValue('77')
  // 解锁后重新计时:再空闲到点(仍有改动)又锁上
  await page.clock.fastForward(61_000)
  await expect(page.getByTestId('admin-idle-lock')).toBeVisible()
})

test('系统设置没改过:空闲到点照旧回总览', async ({ page }) => {
  await open(page, '/admin/settings')
  await expect(timeoutInput(page)).toBeVisible()
  await page.clock.fastForward(61_000)
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByTestId('admin-idle-lock')).toHaveCount(0)
})

test('改了又改回原值:不算没保存,照旧回总览', async ({ page }) => {
  await open(page, '/admin/settings')
  await expect(timeoutInput(page)).toBeVisible()
  const orig = await timeoutInput(page).inputValue()
  await timeoutInput(page).fill('77')
  await timeoutInput(page).fill(orig)
  await page.clock.fastForward(61_000)
  await expect(page).toHaveURL(/#\/dashboard/)
})

test('锁屏里「放弃修改」:回总览', async ({ page }) => {
  await open(page, '/admin/settings')
  await timeoutInput(page).fill('77')
  await page.clock.fastForward(61_000)
  await page.getByTestId('admin-idle-lock-leave').click()
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByTestId('admin-idle-lock')).toHaveCount(0)
})

test('重新进入被管理后台 IP 白名单挡住:说清原因,仍锁着,内容不丢', async ({ page }) => {
  await open(page, '/admin/settings', 40303)
  await timeoutInput(page).fill('77')
  await page.clock.fastForward(61_000)
  await page.getByTestId('admin-idle-lock-resume').click()
  await expect(page.getByTestId('admin-idle-lock-err')).toContainText('白名单')
  await expect(page.getByTestId('admin-idle-lock')).toBeVisible()
  await expect(page).toHaveURL(/#\/admin\/settings/)
})

test('有改动时后端先回 40304:同样锁屏而不是跳走', async ({ page }) => {
  await open(page, '/admin/settings')
  await timeoutInput(page).fill('77')
  // 触发一个会回 40304 的管理接口:保存设置
  await page.route('**/api/v1/settings', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40304, msg: '管理后台已空闲超时,请重新进入', data: null }) }))
  await page.getByRole('button', { name: /保存设置/ }).click()
  await expect(page.getByTestId('admin-idle-lock')).toBeVisible()
  await expect(page).toHaveURL(/#\/admin\/settings/)
  await expect(timeoutInput(page)).toHaveValue('77')
})

test('新建分层的表单填了一半:同样锁屏;关掉表单后空闲照旧回总览', async ({ page }) => {
  await open(page, '/admin/env-tiers')
  await page.getByRole('button', { name: /新建分层/ }).click()
  await page.getByPlaceholder('prod-hk', { exact: true }).fill('prod-sg')
  await page.clock.fastForward(61_000)
  await expect(page.getByTestId('admin-idle-lock')).toBeVisible()
  await page.getByTestId('admin-idle-lock-resume').click()
  await expect(page.getByPlaceholder('prod-hk', { exact: true })).toHaveValue('prod-sg')
  // 关掉表单(取消):不再算没保存
  await page.locator('.form .fhead .x').click()
  await expect(page.getByPlaceholder('prod-hk', { exact: true })).toHaveCount(0)
  await page.clock.fastForward(61_000)
  await expect(page).toHaveURL(/#\/dashboard/)
})
