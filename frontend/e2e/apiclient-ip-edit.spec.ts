import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 系统设置 · 开放接口:已有凭据改 IP 白名单。只发 {allowIps},清空要二次确认,
// 后端拒绝时说原因、弹窗不关;启停开关仍只发 {enabled}。非管理员进不了管理后台设置页,
// 改白名单的权限由后端 AdminOnly 把关(见 apiclient_ip_edit_test.go)。

const C = (id: number, name: string, allowIps: string) => ({
  id, name, key: `ak_${id}000000000000`, userId: 10 + id, userName: `${name}-bot`, allowIps,
  scopes: 'release:read', pipelineId: 0, enabled: true, lastUsedAt: null, createdBy: 1, createdAt: '2026-10-01T00:00:00Z',
})

async function openPanel(page: Page, opts: { putReply?: (body: any) => { code: number; msg: string } } = {}) {
  const clients = [C(1, 'db_check', ''), C(2, 'query', '10.0.0.0/8,203.0.113.7')]
  const puts: { id: string; body: any }[] = []
  await stubCommon(page)
  for (const p of ['**/api/v1/service-accounts', '**/api/v1/users', '**/api/v1/roles']) {
    await page.route(p, (r) => r.fulfill(envelope([])))
  }
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {} })))
  await page.route('**/api/v1/api-clients', (r) => r.fulfill(envelope(clients)))
  await page.route(/\/api\/v1\/api-clients\/\d+$/, (r) => {
    const body = r.request().postDataJSON()
    const id = r.request().url().split('/').pop()!
    puts.push({ id, body })
    const reply = opts.putReply?.(body) ?? { code: 0, msg: 'ok' }
    if (reply.code !== 0) return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...reply, data: null }) })
    const c = clients.find((x) => String(x.id) === id)!
    Object.assign(c, body)
    return r.fulfill(envelope(c))
  })
  await page.goto('/#/admin/settings?tab=openapi')
  return puts
}

const row = (page: Page, name: string) => page.locator('.list .item', { hasText: name })

test('每行显示当前白名单,空的显示「不限来源」', async ({ page }) => {
  await openPanel(page)
  await expect(row(page, 'db_check').getByTestId('ac-ips')).toHaveText('不限来源')
  await expect(row(page, 'query').getByTestId('ac-ips')).toHaveText('10.0.0.0/8,203.0.113.7')
})

test('编辑白名单:只发 allowIps,保存后刷新列表', async ({ page }) => {
  const puts = await openPanel(page)
  await row(page, 'query').getByTestId('ac-ip-edit').click()
  const input = page.getByTestId('ac-ip-input')
  await expect(input).toHaveValue('10.0.0.0/8\n203.0.113.7')
  await input.fill('10.20.0.0/16\n198.51.100.7')
  await page.getByTestId('ac-ip-save').click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({ id: '2', body: { allowIps: '10.20.0.0/16\n198.51.100.7' } })
  await expect(page.getByTestId('ac-ip-modal')).toHaveCount(0)
  await expect(row(page, 'query').getByTestId('ac-ips')).toContainText('10.20.0.0/16')
  // 不限来源的凭据加白名单不需要确认。
  await row(page, 'db_check').getByTestId('ac-ip-edit').click()
  await page.getByTestId('ac-ip-input').fill('172.16.0.1')
  await page.getByTestId('ac-ip-save').click()
  await expect.poll(() => puts.length).toBe(2)
  expect(puts[1]).toEqual({ id: '1', body: { allowIps: '172.16.0.1' } })
  await expect(page.getByTestId('confirm-dialog')).toHaveCount(0)
})

test('清空白名单 = 放开所有来源:先二次确认,取消就不发请求', async ({ page }) => {
  const puts = await openPanel(page)
  await row(page, 'query').getByTestId('ac-ip-edit').click()
  await page.getByTestId('ac-ip-input').fill('  ')
  await expect(page.getByTestId('ac-ip-clear-warn')).toBeVisible()
  await page.getByTestId('ac-ip-save').click()
  await expect(page.getByTestId('confirm-message')).toContainText('任意来源')
  await page.getByTestId('confirm-cancel').click()
  expect(puts).toHaveLength(0)
  await expect(page.getByTestId('ac-ip-modal')).toBeVisible() // 回到编辑,不丢输入

  await page.getByTestId('ac-ip-save').click()
  await page.getByTestId('confirm-ok').click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({ id: '2', body: { allowIps: '' } })
  await expect(row(page, 'query').getByTestId('ac-ips')).toHaveText('不限来源')
})

test('后端拒绝非法输入:说原因,弹窗不关', async ({ page }) => {
  await openPanel(page, { putReply: () => ({ code: 40001, msg: 'IP 白名单第 1 项 "nope" 不是合法的 IP 或 CIDR' }) })
  await row(page, 'query').getByTestId('ac-ip-edit').click()
  await page.getByTestId('ac-ip-input').fill('nope')
  await page.getByTestId('ac-ip-save').click()
  await expect(page.getByText('不是合法的 IP 或 CIDR')).toBeVisible()
  await expect(page.getByTestId('ac-ip-modal')).toBeVisible()
  await expect(row(page, 'query').getByTestId('ac-ips')).toHaveText('10.0.0.0/8,203.0.113.7')
})

test('启停开关仍只发 enabled,不碰白名单', async ({ page }) => {
  const puts = await openPanel(page)
  await row(page, 'query').locator('.vsw').click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({ id: '2', body: { enabled: false } })
})

test('新建凭据:release:approve 默认不勾,勾上后提示只给审批系统', async ({ page }) => {
  await openPanel(page)
  await page.getByRole('button', { name: '新建凭据' }).click()
  const chip = page.locator('.modal .scopes .sc', { hasText: 'release:approve' })
  await expect(chip).toBeVisible()
  await expect(chip).not.toHaveClass(/\bon\b/)
  await expect(page.getByTestId('approve-scope-hint')).toHaveCount(0)
  await chip.click()
  await expect(chip).toHaveClass(/\bon\b/)
  await expect(page.getByTestId('approve-scope-hint')).toContainText('只勾给审批系统的凭据')
})

test('编辑权限范围:只发 scopes,加默认外的范围先确认,取消就不发', async ({ page }) => {
  const puts = await openPanel(page)
  await row(page, 'query').getByTestId('ac-scope-edit').click()
  const modal = page.getByTestId('ac-scope-modal')
  const chip = (s: string) => modal.locator('.scopes .sc', { hasText: s })
  await expect(chip('release:read')).toHaveClass(/\bon\b/) // 带出现有范围
  await chip('release:approve').click()
  await page.getByTestId('ac-scope-save').click()
  await expect(page.getByTestId('confirm-message')).toContainText('release:approve')
  await page.getByTestId('confirm-cancel').click()
  expect(puts).toHaveLength(0)
  await expect(modal).toBeVisible()

  await page.getByTestId('ac-scope-save').click()
  await page.getByTestId('confirm-ok').click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({ id: '2', body: { scopes: ['release:read', 'release:approve'] } })
  await expect(modal).toHaveCount(0)
})

test('编辑权限范围:只减不加不用确认;全去掉不能保存;提单+审批同在一把凭据要提醒', async ({ page }) => {
  const puts = await openPanel(page)
  await row(page, 'db_check').getByTestId('ac-scope-edit').click()
  const modal = page.getByTestId('ac-scope-modal')
  const chip = (s: string) => modal.locator('.scopes .sc', { hasText: s })
  await chip('release:read').click() // 去掉唯一一项
  await expect(page.getByTestId('ac-scope-empty')).toBeVisible()
  await expect(page.getByTestId('ac-scope-save')).toBeDisabled()

  await chip('release:create').click()
  await chip('release:approve').click()
  await expect(page.getByTestId('ac-scope-sod-warn')).toBeVisible()
  await chip('release:approve').click()
  await expect(page.getByTestId('ac-scope-sod-warn')).toHaveCount(0)
  await page.getByTestId('ac-scope-save').click() // release:create 在默认集合里,不用确认
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({ id: '1', body: { scopes: ['release:create'] } })
  await expect(page.getByTestId('confirm-dialog')).toHaveCount(0)
})
