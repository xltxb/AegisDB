import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// L-AUTH4(2026-10 安全审计):绑定 MFA 要先出示当前登录密码。服务端答 42801 时弹窗亮出密码框,
// 输对了才领到密钥、显示二维码;输错了说原因,不显示密钥。
const fail = (code: number, msg: string) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code, msg, data: null }) })

test('绑定 MFA:先输当前登录密码,通过后才显示密钥', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/dashboard**', (r) => r.fulfill(envelope({})))
  const bodies: any[] = []
  await page.route('**/api/v1/auth/mfa/setup', (r) => {
    const body = r.request().postDataJSON() ?? {}
    bodies.push(body)
    if (!body.password) return r.fulfill(fail(42801, '请输入当前登录密码'))
    if (body.password !== 'aegisdb123') return r.fulfill(fail(42801, '当前登录密码错误'))
    return r.fulfill(envelope({ secret: 'JBSWY3DPEHPK3PXP', otpauthUri: 'otpauth://totp/AegisDB:linwei?secret=JBSWY3DPEHPK3PXP' }))
  })
  await page.goto('/#/dashboard')
  await page.locator('.avatar').click()
  await page.locator('.um-mfa').first().click() // 第二个同类项是「修改密码」

  const pw = page.getByTestId('mfa-current-pw')
  await expect(pw).toBeVisible()
  await expect(page.locator('.kv')).toHaveCount(0) // 密钥还没出来
  await pw.fill('wrong-pass')
  await page.getByTestId('mfa-pw-next').click()
  await expect(page.locator('.mfa-overlay .err')).toContainText('当前登录密码错误')
  await expect(page.locator('.kv')).toHaveCount(0)

  await pw.fill('aegisdb123')
  await page.getByTestId('mfa-pw-next').click()
  await expect(page.locator('.kv')).toHaveText('JBSWY3DPEHPK3PXP')
  expect(bodies.at(-1)).toEqual({ password: 'aegisdb123' })
})
