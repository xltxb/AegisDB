import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// SSO 的部署层总闸(sso.disabled / AEGISDB_SSO_DISABLED):测试中发现 SSO 有问题、又进不去
// 设置页时,改配置重启即可关掉。钉两条:登录页回到纯口令表单;设置页说清"这里的开关暂不生效"。

async function stubSsoConfig(page: Page, forcedOff: boolean) {
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: false, loginUrl: '', forcedOff })))
}

test.describe('SSO 总闸', () => {
  test('登录页:没有 SSO 按钮,口令表单直接可用', async ({ page }) => {
    await stubSsoConfig(page, true)
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toHaveCount(0)
    await expect(page.locator('input[type="email"]')).toBeVisible()
  })

  test('设置页:总闸拉下时提示开关暂不生效', async ({ page }) => {
    await stubCommon(page)
    await stubSsoConfig(page, true)
    await page.route((u) => u.pathname.endsWith('/api/v1/settings'), (r) => {
      if (r.request().method() !== 'GET') return r.fallback()
      return r.fulfill(envelope({ settings: { 'sso.enabled': 'true', 'sso.issuer': '"https://idp.example.com"' }, secretsSet: {} }))
    })
    await page.goto('/#/settings')
    await page.locator('.tab', { hasText: '会话与安全' }).click()
    await expect(page.getByTestId('sso-forced-off')).toContainText('AEGISDB_SSO_DISABLED')
  })

  test('设置页:没拉总闸时不提示', async ({ page }) => {
    await stubCommon(page)
    await stubSsoConfig(page, false)
    await page.route((u) => u.pathname.endsWith('/api/v1/settings'), (r) => {
      if (r.request().method() !== 'GET') return r.fallback()
      return r.fulfill(envelope({ settings: {}, secretsSet: {} }))
    })
    await page.goto('/#/settings')
    await page.locator('.tab', { hasText: '会话与安全' }).click()
    await expect(page.getByTestId('sso-enabled')).toBeVisible()
    await expect(page.getByTestId('sso-forced-off')).toHaveCount(0)
  })
})
