import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME, autoConfirm } from './helpers'

// 管理后台拆分(工单 07):用户后台的执行窗口只列自己的;管理后台的全局页列所有人的、带申请人,
// 可停用(停用不走审批)。

const ago = (min: number) => new Date(Date.now() + min * 60_000).toISOString()
const W = (id: number, by: number, name: string) => ({
  id, name: '班车' + id, enabled: true, connectionId: 1, database: 'G13', kind: 'once', timezone: 'Asia/Shanghai',
  startsAt: ago(-10), endsAt: ago(60), weekdays: '', startMin: 0, endMin: 0, reason: '发版', createdBy: by, createdByName: name,
  createdAt: ago(-100), updatedAt: ago(-100), status: 'approved', apNo: 'AP-' + id, active: true, expired: false,
})

async function open(page: Page, path: string) {
  const calls: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...ME.menus, execwindow: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([{ id: 1, name: 'prod-g13', engine: 'Oracle 19c', env: 'prod', status: 'online' }])))
  await page.route((u) => u.pathname.endsWith('/api/v1/exec-windows'), (r) => {
    calls.push(r.request().url())
    return r.fulfill(envelope([W(1, 3, 'Chen Hao'), W(2, 1, 'Lin Wei')]))
  })
  await page.route('**/api/v1/exec-windows/*/disable', (r) => { calls.push('disable:' + r.request().url()); return r.fulfill(envelope({})) })
  await autoConfirm(page)
  await page.goto(path)
  await expect.poll(() => calls.length).toBeGreaterThan(0)
  return calls
}

test('管理后台:列所有人的窗口(scope=all)、带申请人,可以停用别人的', async ({ page }) => {
  const calls = await open(page, '/#/admin/exec-windows')
  expect(calls[0]).toContain('scope=all')
  await expect(page.getByTestId('ew-applicant').first()).toContainText('Chen Hao')
  await expect(page.getByRole('button', { name: /申请/ })).toHaveCount(0)
  await page.getByTestId('ew-disable').first().click()
  await expect.poll(() => calls.some((c) => c.startsWith('disable:') && c.includes('/exec-windows/1/disable'))).toBe(true)
})

test('用户后台:只请求自己的窗口,不显示申请人列,可以申请', async ({ page }) => {
  const calls = await open(page, '/#/exec-windows')
  expect(calls[0]).not.toContain('scope=all')
  await expect(page.getByTestId('ew-applicant')).toHaveCount(0)
})
