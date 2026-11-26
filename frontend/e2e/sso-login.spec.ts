import { test, expect, type Page } from '@playwright/test'
import { envelope, ME } from './helpers'

// 公司 SSO(OIDC)的登录页。授权码流程整个在服务端,登录页只做这几件事,这里一一钉住:
//   · 点「使用 SSO 登录」:浏览器去网关的发起地址(按页面位置解析,不当作站点根)
//   · 回调成功跳回 #/login?sso=1:自动换会话进系统,并把 ?sso=1 从地址栏去掉
//   · 回调失败跳回 #/login?sso_error=<原因>:说出原因,不自动重试
//   · 不是从回调回来的:不自动登录(登出后回到登录页不会一眨眼又进去)
//   · 回来了却没有登录结果(40101):说清楚,让人再点一次
//   · 网关里没开通账号(40302):弹窗拦下;有号没角色:提示等管理员分配
//   · 已绑定 TOTP 的管理员:亮出输码框,带码再换一次
//   · 启用 SSO 后本地表单默认收起

const START = 'api/v1/auth/oidc/start'
const fail = (code: number, msg: string) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code, msg, data: null }) })

async function stubSso(page: Page, opts: { enabled?: boolean; sso?: 'ok' | 'none' | 'norole' | 'noaccount' | 'mfa' } = {}) {
  const calls = { sso: 0, start: 0, bodies: [] as any[] }
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: opts.enabled ?? true, loginUrl: START })))
  await page.route('**/api/v1/auth/sso', (r) => {
    calls.sso++
    const body = r.request().postDataJSON() ?? {}
    calls.bodies.push(body)
    // 已绑定 TOTP 的管理员(M8):不带码 → 42800;带错码 → 42800 + 原因;带 123456 → 放行
    if (opts.sso === 'mfa' && body.mfaCode !== '123456') {
      return r.fulfill(fail(42800, body.mfaCode ? 'MFA 验证码错误' : '请输入 MFA 验证码'))
    }
    if (opts.sso === 'none') return r.fulfill(fail(40101, '未检测到 SSO 登录结果'))
    if (opts.sso === 'noaccount') return r.fulfill(fail(40302, '你还没有开通网关账号,无权限访问。请联系管理员开通后再登录'))
    const user = opts.sso === 'norole' ? { ...ME, roleIds: [], roleCodes: [], menus: {} } : ME
    return r.fulfill(envelope({ session: '1.sso-jwt', expiresAt: '2099-01-01 00:00:00', user }))
  })
  // 发起地址:真后端会 302 去身份服务,这里落地即可
  await page.route(`**/${START}`, (r) => { calls.start++; return r.fulfill({ status: 200, contentType: 'text/html', body: '<h1>IdP</h1>' }) })
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(opts.sso === 'norole' ? { ...ME, menus: {} } : ME)))
  await page.route('**/api/v1/**', (r) => {
    const u = r.request().url()
    return u.includes('/auth/sso') || u.includes('/auth/oidc') ? r.fallback() : r.fulfill(envelope([]))
  })
  return calls
}

test.describe('SSO 登录(OIDC)', () => {
  test('点按钮:去网关的发起地址', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'none' })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toHaveText(/使用 SSO 登录/)
    await page.getByTestId('sso-login').click()
    await expect(page).toHaveURL(/\/api\/v1\/auth\/oidc\/start$/)
    expect(calls.start).toBe(1)
    expect(calls.sso).toBe(0) // 点按钮不先去试换会话
  })

  test('回调成功回来:自动换会话进系统', async ({ page }) => {
    await stubSso(page, { sso: 'ok' })
    await page.goto('/#/login?sso=1')
    await expect(page).toHaveURL(/#\/dashboard/)
    expect(await page.evaluate(() => localStorage.getItem('aegisdb_session'))).toBe('1.sso-jwt')
  })

  test('不是从回调回来的:不自动登录,不报错;本地表单默认收起', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'ok' })
    await page.goto('/#/login')
    await expect(page.getByTestId('sso-login')).toBeVisible()
    await page.waitForTimeout(300)
    expect(calls.sso).toBe(0)
    await expect(page.getByTestId('login-error')).toHaveCount(0)
    await expect(page.locator('input[type="email"]')).toHaveCount(0)
    await page.getByTestId('sso-admin-toggle').click()
    await expect(page.locator('input[type="email"]')).toBeVisible()
  })

  test('回调失败:说出原因,不自动重试,地址栏去掉 sso_error', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'ok' })
    await page.goto('/#/login?sso_error=email')
    await expect(page.getByTestId('login-error')).toContainText('已验证的邮箱')
    expect(calls.sso).toBe(0)
    await expect(page).toHaveURL(/#\/login$/)
  })

  test('不认识的失败原因:按"暂时不可用"说,不把地址栏里的文字显示出来', async ({ page }) => {
    await stubSso(page, { sso: 'ok' })
    await page.goto('/#/login?sso_error=%3Cb%3Ehi%3C%2Fb%3E')
    await expect(page.getByTestId('login-error')).toContainText('暂时不可用')
    await expect(page.getByTestId('login-error')).not.toContainText('hi')
  })

  test('回来了却没有登录结果:说清楚,让人再点一次', async ({ page }) => {
    await stubSso(page, { sso: 'none' })
    await page.goto('/#/login?sso=1')
    await expect(page.getByTestId('login-error')).toContainText('没有拿到 SSO 登录结果')
    await expect(page).toHaveURL(/#\/login$/)
  })

  test('已绑定 TOTP 的管理员:亮出输码框,带码再登一次', async ({ page }) => {
    const calls = await stubSso(page, { sso: 'mfa' })
    await page.goto('/#/login?sso=1')
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

  test('网关里没开通账号:弹窗拦下,提示无权限访问,不进系统', async ({ page }) => {
    await stubSso(page, { sso: 'noaccount' })
    await page.goto('/#/login?sso=1')
    const dlg = page.getByTestId('sso-no-account')
    await expect(dlg).toBeVisible()
    await expect(dlg).toContainText('无权限访问')
    await expect(dlg).toContainText('开通')
    await expect(page).toHaveURL(/#\/login/)
    expect(await page.evaluate(() => localStorage.getItem('aegisdb_session'))).toBeNull()
    await page.getByTestId('sso-no-account-ok').click()
    await expect(dlg).toHaveCount(0)
  })

  test('有账号但没分配角色:登录成功,但提示等管理员分配角色', async ({ page }) => {
    await stubSso(page, { sso: 'norole' })
    await page.goto('/#/login?sso=1')
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
