import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 设置 · 会话与安全里的 SSO(OIDC)配置:字段齐全,Client Secret 不回显、空着即不改,
// 回调地址可一键填入本站地址(它要原样登记到身份服务)。
test('SSO 设置:OIDC 字段、回调地址建议值、保存时 Secret 空着不改', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: true, loginUrl: 'api/v1/auth/oidc/start', forcedOff: false })))
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') { saved.push(r.request().postDataJSON()); return r.fulfill(envelope({ ok: true })) }
    return r.fulfill(envelope({
      settings: {
        'sso.enabled': 'true', 'sso.issuer': '"https://idp.example.com"', 'sso.clientId': '"aegisdb"',
        'sso.clientSecret': '""', 'sso.redirectUrl': '""',
      },
      secretsSet: { 'sso.clientSecret': true },
    }))
  })
  await page.goto('/#/admin/settings?tab=security')
  await expect(page.getByTestId('sso-issuer')).toHaveValue('https://idp.example.com')
  await expect(page.getByTestId('sso-client-id')).toHaveValue('aegisdb')
  await expect(page.getByTestId('sso-client-secret')).toHaveValue('')
  await expect(page.getByTestId('sso-scopes')).toHaveValue('openid email profile')
  await expect(page.getByTestId('sso-email-claim')).toHaveValue('email')

  await page.getByTestId('sso-redirect-fill').click()
  const origin = new URL(page.url()).origin
  await expect(page.getByTestId('sso-redirect')).toHaveValue(`${origin}/api/v1/auth/oidc/callback`)
  await expect(page.getByTestId('sso-redirect-fill')).toHaveCount(0) // 已是建议值:按钮收起

  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  const all = Object.assign({}, ...saved)
  expect(all).toMatchObject({
    'sso.enabled': true, 'sso.issuer': 'https://idp.example.com', 'sso.clientId': 'aegisdb',
    'sso.clientSecret': '', 'sso.redirectUrl': `${origin}/api/v1/auth/oidc/callback`,
    'sso.scopes': 'openid email profile', 'sso.emailClaim': 'email',
  })
  expect(all).not.toHaveProperty('sso.baseUrl')
  expect(all).not.toHaveProperty('sso.cookieName')
})
