import { test, expect } from '@playwright/test'
import { envelope, failure, stubCommon, ME } from './helpers'

// 管理后台 IP 白名单(工单 08)。

test('系统设置:配置管理后台 IP 白名单,保存的是独立的两个设置项', async ({ page }) => {
  const saved: Record<string, unknown>[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: {}, secretsSet: {}, adminIpAllowlistForcedOff: false }))
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({ ok: true }))
  })
  await page.goto('/#/admin/settings?tab=security')
  await expect(page.getByTestId('admin-ip-badge')).toBeVisible()
  await expect(page.getByTestId('admin-ip-forced-off')).toHaveCount(0)
  await page.getByTestId('admin-ip-manage').click()
  await page.locator('.ipallow-overlay textarea').fill('10.0.0.0/8\n192.168.1.5')
  await page.locator('.ipallow-overlay').getByRole('button', { name: '保存' }).click()
  await expect.poll(() => saved.find((b) => 'security.adminIpAllowlist' in b)?.['security.adminIpAllowlist']).toBe('10.0.0.0/8, 192.168.1.5')
  expect(saved.find((b) => 'security.adminIpAllowlist' in b)).not.toHaveProperty('security.ipAllowlist')
})

test('部署总闸拉下时,设置页提示白名单暂不生效', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {}, adminIpAllowlistForcedOff: true })))
  await page.goto('/#/admin/settings?tab=security')
  await expect(page.getByTestId('admin-ip-forced-off')).toBeVisible()
})

test('管理接口返回「IP 不在白名单」:提示并退回用户后台', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/roles', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40303, msg: '当前 IP 不在管理后台白名单内', data: null }) }))
  await page.goto('/#/admin/permissions')
  await expect(page).toHaveURL(/#\/dashboard/)
  await expect(page.getByText('当前 IP 不在管理后台白名单内').first()).toBeVisible()
})

// 保存失败:徽标不能先变成「已启用」,弹窗留着、改动不丢,并报出原因(2026-10 审查)。
test('保存管理后台 IP 白名单失败:页面不显示已启用,弹窗不关', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: {}, secretsSet: {}, adminIpAllowlistForcedOff: false }))
    return r.fulfill(failure('数据库写入失败'))
  })
  await page.goto('/#/admin/settings?tab=security')
  await expect(page.getByTestId('admin-ip-badge')).toHaveText('未启用')
  await page.getByTestId('admin-ip-manage').click()
  await page.locator('.ipallow-overlay textarea').fill('10.0.0.0/8')
  await page.locator('.ipallow-overlay').getByRole('button', { name: '保存' }).click()
  await expect(page.getByText('数据库写入失败').first()).toBeVisible()
  await expect(page.locator('.ipallow-overlay')).toBeVisible()
  await expect(page.locator('.ipallow-overlay textarea')).toHaveValue('10.0.0.0/8')
  await expect(page.getByTestId('admin-ip-badge')).toHaveText('未启用')
})

// 一个页面同时发出几个管理接口请求、全都回 40303:提示只弹一次 —— 从前布局按请求数弹,
// 页面自己捕获错误后又弹一遍(2026-10 审查)。
test('管理总览的几个请求同时被 IP 白名单拒绝:提示只出现一次', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  const blocked = { status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40303, msg: '当前 IP 不在管理后台白名单内', data: null }) }
  await page.route((u) => /\/api\/v1\/(audit|connections|settings|approvals)(\?|$)/.test(u.pathname + (u.search ? '?' : '')), (r) => r.fulfill(blocked))
  await page.goto('/#/dashboard')
  await page.evaluate(() => { location.hash = '#/admin/overview' })
  await expect(page).toHaveURL(/#\/dashboard/)
  await page.waitForTimeout(800)
  await expect(page.getByText('当前 IP 不在管理后台白名单内')).toHaveCount(1)
})
