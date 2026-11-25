import { test, expect } from '@playwright/test'
import { envelope, stubCommon, autoConfirm } from './helpers'

// 【权限 · 用户】里的 SSO 绑定状态。绑定发生在第一次经 SRD 授权登录、邮箱对上的时候(服务端),
// 这里只展示,并让管理员解绑;SRD 没有用户名单接口,所以不再有"与 SSO 核对绑定"按钮。

const USERS = [
  { id: 1, name: 'Lin Wei', email: 'linwei@aegisdb.io', initials: 'LW', dept: 'DBA', roles: ['Admin'], roleIds: [1], primaryRoleId: 1, status: 'active', kind: 'human', mfaEnabled: false, lastActive: '—', ssoBound: true, ssoUserName: 'linwei' },
  { id: 2, name: 'Chen Hao', email: 'chenhao@aegisdb.io', initials: 'CH', dept: 'Dev', roles: ['RO'], roleIds: [4], primaryRoleId: 4, status: 'active', kind: 'human', mfaEnabled: false, lastActive: '—', ssoBound: false },
]

test.describe('SSO 绑定状态', () => {
  test.beforeEach(async ({ page }) => {
    await stubCommon(page)
    await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: true, loginUrl: 'https://srd.example.com/auth/login' })))
    await page.route('**/api/v1/users', (r) => r.fulfill(envelope(USERS)))
    await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  })

  test('每行显示绑定状态;管理员点已绑定的标签可解绑', async ({ page }) => {
    let unbound = 0
    await page.route('**/api/v1/users/1/sso-unbind', (r) => { unbound++; return r.fulfill(envelope({ ok: true })) })
    await autoConfirm(page)
    await page.goto('/#/permissions')
    await page.getByText(/用户视图|User view/).first().click()
    await expect(page.getByTestId('sso-tag-1')).toHaveClass(/on/)
    await expect(page.getByTestId('sso-tag-2')).not.toHaveClass(/on/)
    await page.getByTestId('sso-tag-2').click()           // 未绑定:点了什么都不发生
    await page.getByTestId('sso-tag-1').click()
    await expect.poll(() => unbound).toBe(1)
  })

  test('不再有名单核对按钮', async ({ page }) => {
    await page.goto('/#/permissions')
    await page.getByText(/用户视图|User view/).first().click()
    await expect(page.getByTestId('sso-tag-1')).toBeVisible()
    await expect(page.getByTestId('sso-match')).toHaveCount(0)
  })
})
