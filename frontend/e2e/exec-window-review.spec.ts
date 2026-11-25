import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-26 回归排查 08:拆分后用户后台的班车列表只剩自己的,回看按钮挂在列表行上,DBA 负责人 / L2
// 看不到别人班车的回看。有回看权限的人多一个「全部窗口」页签(scope=reviewable,含已过期),
// 每行可回看,只读 —— 不摆改 / 停用 / 删。「我的申请」照旧。

const ago = (min: number) => new Date(Date.now() + min * 60_000).toISOString()
const W = (id: number, by: number, name: string, over: Record<string, unknown> = {}) => ({
  id, name: '班车' + id, enabled: true, connectionId: 1, database: 'G13', kind: 'once', timezone: 'Asia/Shanghai',
  startsAt: ago(-10), endsAt: ago(60), weekdays: '', startMin: 0, endMin: 0, reason: '发版', createdBy: by, createdByName: name,
  createdAt: ago(-100), updatedAt: ago(-100), status: 'approved', apNo: 'AP-' + id, active: true, expired: false, ...over,
})
const OWNER = { ...ME, id: 2, name: 'Zhang Wei', roleCode: 'owner', roleCodes: ['owner'], roleNames: ['DBA 负责人'],
  menus: { terminal: true, approve: true, audit: true, pipeline: true, execwindow: true } }

async function open(page: Page, me: Record<string, unknown>) {
  const calls: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([{ id: 1, name: 'prod-g13', engine: 'Oracle 19c', env: 'prod', status: 'online' }])))
  await page.route((u) => u.pathname.endsWith('/api/v1/exec-windows'), (r) => {
    const url = r.request().url()
    calls.push(url)
    if (url.includes('scope=reviewable')) {
      return r.fulfill(envelope([
        W(1, 3, 'Chen Hao', { active: false, expired: true, startsAt: ago(-300), endsAt: ago(-200) }),
        W(2, (me as any).id, String((me as any).name)),
      ]))
    }
    return r.fulfill(envelope([W(2, (me as any).id, String((me as any).name))]))
  })
  await page.route('**/api/v1/exec-windows/*/statements', (r) => {
    calls.push('stmts:' + r.request().url())
    return r.fulfill(envelope({ windowId: 1, items: [{ auditId: 9, occurredAt: ago(-250), actor: 'Chen Hao', command: 'DROP TABLE t_old',
      risk: 'high', result: 'executed', byWindow: true }], byWindow: 1, truncated: false }))
  })
  await page.goto('/#/exec-windows')
  await expect.poll(() => calls.length).toBeGreaterThan(0)
  return calls
}

test('DBA 负责人:「全部窗口」列别人的(含已过期)、带申请人,可回看,只读', async ({ page }) => {
  const calls = await open(page, OWNER)
  // 默认仍是自己的申请
  expect(calls[0]).not.toContain('scope=')
  await expect(page.getByTestId('ew-tab-mine')).toHaveClass(/on/)
  await expect(page.getByTestId('ew-edit')).toHaveCount(1)
  await page.getByTestId('ew-tab-all').click()
  await expect.poll(() => calls.some((c) => c.includes('scope=reviewable'))).toBe(true)
  await expect(page.getByTestId('ew-applicant')).toHaveCount(2)
  await expect(page.getByTestId('ew-applicant').first()).toContainText('Chen Hao')
  await expect(page.locator('.wst.off')).toHaveCount(1) // 已过期的那扇
  await expect(page.getByTestId('ew-review')).toHaveCount(2)
  // 只读:连自己那扇也不摆改 / 停用 / 删
  await expect(page.getByTestId('ew-edit')).toHaveCount(0)
  await expect(page.getByTestId('ew-disable')).toHaveCount(0)
  await expect(page.getByTestId('ew-delete')).toHaveCount(0)
  await page.getByTestId('ew-review').first().click()
  await expect.poll(() => calls.some((c) => c.startsWith('stmts:') && c.includes('/exec-windows/1/statements'))).toBe(true)
  await expect(page.getByText('DROP TABLE t_old')).toBeVisible()
})

test('切回「我的申请」:按原口径取自己的,编辑等按钮回来', async ({ page }) => {
  const calls = await open(page, OWNER)
  await page.getByTestId('ew-tab-all').click()
  await expect.poll(() => calls.some((c) => c.includes('scope=reviewable'))).toBe(true)
  const n = calls.length
  await page.getByTestId('ew-tab-mine').click()
  await expect.poll(() => calls.length).toBeGreaterThan(n)
  expect(calls[n]).not.toContain('scope=')
  await expect(page.getByTestId('ew-applicant')).toHaveCount(0)
  await expect(page.getByTestId('ew-edit')).toHaveCount(1)
})

test('管理员在用户后台的「全部窗口」也是只读(别人的窗口在管理后台管)', async ({ page }) => {
  await open(page, { ...ME, menus: { ...ME.menus, execwindow: true } })
  await page.getByTestId('ew-tab-all').click()
  await expect(page.getByTestId('ew-review')).toHaveCount(2)
  await expect(page.getByTestId('ew-edit')).toHaveCount(0)
  await expect(page.getByTestId('ew-delete')).toHaveCount(0)
})
