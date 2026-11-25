import { test, expect, type Page } from '@playwright/test'
import { envelope, ME } from './helpers'

// SRD 授权登录(公司 SSO)的登录页。钉住前端这一侧的几条约定:
//   · 浏览器里已有 SRD 登录态(CS-Token)就静默登进去,不用点
//   · 没有登录态(40101)不是错误:只露出「使用 SRD 授权登录」按钮,不报红字
//   · 点按钮且没有登录态 / 登录态被 SRD 拒绝:跳 {SRD}/auth/login?redirect=<编码后的网关登录页>
//   · 刚从 SRD 授权回来却仍没有登录态:说出原因(Cookie 没带过来),而不是让人来回跳
//   · 网关里没开通账号(40302):弹窗拦下,提示无权限访问;有号没角色:提示"等管理员分配"
//   · 主动登出后回到登录页不自动登录;启用 SSO 后本地表单默认收起

const LOGIN_URL = 'https://srd.example.com/auth/login'
const fail = (code: number, msg: string) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code, msg, data: null }) })

async function stubSso(page: Page, opts: { enabled?: boolean; sso?: 'ok' | 'none' | 'rejected' | 'norole' | 'noaccount' | 'mfa' } = {}) {
  const calls = { sso: 0, bodies: [] as any[] }
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({
    enabled: opts.enabled ?? true, loginUrl: LOGIN_URL,
  })))
  await page.route('**/api/v1/auth/sso', (r) => {
    calls.sso++
    const body = r.request().postDataJSON() ?? {}
    calls.bodies.push(body)
    // 已绑定 TOTP 的管理员(M8):不带码 → 42800;带错码 → 42800 + 原因;带 123456 → 放行
    if (opts.sso === 'mfa' && body.mfaCode !== '123456') {
      return r.fulfill(fail(42800, body.mfaCode ? 'MFA 验证码错误' : '请输入 MFA 验证码'))
    }
    if (opts.sso === 'none') return r.fulfill(fail(40101, '未检测到 SSO 登录态'))
    if (opts.sso === 'rejected') return r.fulfill(fail(40100, 'SRD 登录态无效或已过期,请重新授权登录'))
    if (opts.sso === 'noaccount') return r.fulfill(fail(40302, '你还没有开通网关账号,无权限访问。请联系管理员开通后再登录'))
    const user = opts.sso === 'norole' ? { ...ME, roleIds: [], roleCodes: [], menus: {} } : ME
    return r.fulfill(envelope({ session: '1.sso-jwt', expiresAt: '2099-01-01 00:00:00', user }))
  })
  // 登录成功后总览要拉的东西:给空数据,别让它去打真后端
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(opts.sso === 'norole' ? { ...ME, menus: {} } : ME)))
  await page.route('**/api/v1/**', (r) => (r.request().url().includes('/auth/sso') ? r.fallback() : r.fulfill(envelope([]))))
  // SRD 登录页:落地即可,记下它收到的 redirect
  await page.route(`${LOGIN_URL}*`, (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<h1>SRD</h1>' }))
  return calls
}

const expectRedirectToSRD = async (page: Page) => {
  await expect(page).toHaveURL(/srd\.example\.com\/auth\/login\?redirect=/)
  const back = new URL(page.url()).searchParams.get('redirect') || ''
  expect(back).toMatch(/^http:\/\/[^/]+\/#\/login$/) // 回到网关登录页
}

test.describe('SRD 授权登录', () => {
  test('已绑定 TOTP 的管理员:SSO 后亮出输码框,带码再登一次', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'mfa' })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-mfa-code')).toBeVisible()
    await expect(page.getByTestId('login-error')).toHaveCount(0) // 第一次只是要码,不报红字
    await page.getByTestId('sso-mfa-code').fill('000000')
    await page.getByTestId('sso-mfa-submit').click()
    await expect(page.getByTestId('login-error')).toContainText('MFA 验证码错误')
    await page.getByTestId('sso-mfa-code').fill('123456')
    await page.getByTestId('sso-mfa-submit').click()
    await expect(page).toHaveURL(/#\/dashboard/)
    expect(calls.bodies.at(-1)).toEqual({ mfaCode: '123456' })
  })

  test('有 SRD 登录态:进登录页就自动登进去', async ({ page }) => {
    await stubSso(page, { sso: 'ok' })
    await page.goto('/#/login')
    await expect(page).toHaveURL(/#\/dashboard/)
    expect(await page.evaluate(() => localStorage.getItem('vela_session'))).toBe('1.sso-jwt')
  })

  test('没有登录态:只露出按钮,不报错;本地表单默认收起', async ({ page }) => {
    await stubSso(page, { sso: 'none' })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toHaveText(/使用 SRD 授权登录/)
    await expect(page.getByTestId('login-error')).toHaveCount(0)
    await expect(page.locator('input[type="email"]')).toHaveCount(0)
    await page.getByTestId('sso-admin-toggle').click()
    await expect(page.locator('input[type="email"]')).toBeVisible()
  })

  test('点按钮且没有登录态:跳 SRD 授权登录,redirect 指回网关登录页', async ({ page }) => {
    await stubSso(page, { sso: 'none' })
    await page.goto('/#/login')
    await page.getByTestId('sso-login').click()
    await expectRedirectToSRD(page)
  })

  test('点按钮但登录态已被 SRD 拒绝(过期):同样去 SRD 重新授权', async ({ page }) => {
    await stubSso(page, { sso: 'rejected' })
    await page.goto('/#/login')
    await page.getByTestId('sso-login').click()
    await expectRedirectToSRD(page)
  })

  test('刚从 SRD 回来却没有登录态:说出 Cookie 没带过来的原因', async ({ page }) => {
    await stubSso(page, { sso: 'none' })
    await page.addInitScript(() => sessionStorage.setItem('vela_srd_redirected_at', String(Date.now())))
    await page.goto('/#/login')
    await expect(page.getByTestId('login-error')).toContainText('CS-Token')
  })

  test('主动登出后:点按钮去授权时撤掉"别自动登录"', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'none' })
    await page.addInitScript(() => { if (!sessionStorage.getItem('seen')) { sessionStorage.setItem('vela_sso_skip', '1'); sessionStorage.setItem('seen', '1') } })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toBeVisible()
    await page.waitForTimeout(300)
    expect(calls.sso).toBe(0) // 登出后不自动登录
    await page.getByTestId('sso-login').click()
    await expectRedirectToSRD(page)
    await page.goBack()
    expect(await page.evaluate(() => sessionStorage.getItem('vela_sso_skip'))).toBeNull()
  })

  test('网关里没开通账号:弹窗拦下,提示无权限访问,不进系统', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'noaccount' })
    await page.goto('/#/login')
    const dlg = page.getByTestId('sso-no-account')
    await expect(dlg).toBeVisible()
    await expect(dlg).toContainText('无权限访问')
    await expect(dlg).toContainText('开通')
    await expect(page).toHaveURL(/#\/login/)
    expect(await page.evaluate(() => localStorage.getItem('vela_session'))).toBeNull()
    await page.getByTestId('sso-no-account-ok').click()
    await expect(dlg).toHaveCount(0)
    // 手动再点一次 SSO:还是拦下,不会跳 SRD 来回绕
    await page.getByTestId('sso-login').click()
    await expect(dlg).toBeVisible()
    await expect(page).toHaveURL(/#\/login/)
    expect(calls.sso).toBe(2)
  })

  test('有账号但没分配角色:登录成功,但提示等管理员分配角色', async ({ page }) => {
    await stubSso(page, { sso: 'norole' })
    await page.goto('/#/login')
    await expect(page.getByTestId('login-error')).toContainText('分配')
    await expect(page).toHaveURL(/#\/login/)
  })

  test('未启用 SSO:登录页与原来一样', async ({ page }) => {
    await stubSso(page, { enabled: false })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toHaveCount(0)
    await expect(page.locator('input[type="email"]')).toBeVisible()
  })
})
