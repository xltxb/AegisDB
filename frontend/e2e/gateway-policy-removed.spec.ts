import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// issue #123:网关策略(strict / approve-1 / audit-only)从不参与判定,却到处显示、还能改 ——
// 把实例设成 strict 的人会以为更安全。界面上拿掉,请求里也不再带。

const CONN = {
  id: 1, name: 'db1', engine: 'MySQL 8.0', env: 'prod', host: '10.0.0.1', port: 3306, policy: 'audit-only', layer: 'L1',
  defaultRole: 'dba_l2', status: 'online', tags: '', username: 'u', database: 'app',
}

async function openConnections(page: Page) {
  const writes: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => {
    if (r.request().method() === 'POST') { writes.push(r.request().postDataJSON()); return r.fulfill(envelope({ ...CONN, id: 2 })) }
    return r.fulfill(envelope([CONN]))
  })
  await page.route('**/api/v1/connections/1', (r) => { writes.push(r.request().postDataJSON()); return r.fulfill(envelope(CONN)) })
  await page.route('**/api/v1/connections/*/test', (r) => r.fulfill(envelope({ ok: true })))
  await page.goto('/#/admin/connections')
  await expect(page.locator('.trow').first()).toContainText('db1')
  return writes
}

test('数据源页:列表、批量条、表单里没有网关策略,新建与编辑都不带', async ({ page }) => {
  const writes = await openConnections(page)
  await expect(page.locator('.thead')).not.toContainText('网关策略')
  await expect(page.locator('.trow').first()).not.toContainText('audit-only')
  await expect(page.locator('.polsel, .polpill')).toHaveCount(0)
  await page.locator('.trow .cb').first().check()
  await expect(page.locator('.fab')).toBeVisible()
  await expect(page.locator('.fab select')).toHaveCount(0)
  // 表头与行的列数一致,没有错位
  const cols = (sel: string) => page.locator(sel).first().evaluate((e) => e.children.length)
  expect(await cols('.thead')).toBe(await cols('.trow'))
  await page.locator('.fab .ghosty').click()

  await page.getByRole('button', { name: '新建连接' }).click()
  const card = page.locator('.ce-card')
  await expect(card).not.toContainText('网关策略')
  await card.locator('.fgrid input').nth(0).fill('new-db')
  await card.locator('.fgrid input').nth(1).fill('10.0.0.2:3306')
  await card.getByRole('button', { name: '测试保存' }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]).not.toHaveProperty('policy')

  await page.reload()
  await page.locator('.trow').first().getByTitle('编辑', { exact: true }).click()
  await expect(card).toBeVisible()
  await expect(card).not.toContainText('网关策略')
  await card.getByRole('button', { name: '测试保存' }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1]).toMatchObject({ name: 'db1', env: 'prod' })
  expect(writes[1]).not.toHaveProperty('policy')
})

test('设置页:没有默认网关策略,保存也不带', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({ settings: {}, webhook: null, secrets: {} }))
  })
  await page.goto('/#/admin/settings')
  await expect(page.getByText('命令执行超时').first()).toBeVisible()
  await expect(page.getByText('默认网关策略')).toHaveCount(0)
  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  for (const b of saved) expect(b).not.toHaveProperty('gateway.defaultPolicy')
})

test('终端:对象树、状态栏、检查面板、连接提示都不再显示', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([{ ...CONN, policy: 'strict' }])))
  await page.route('**/api/v1/connections/1/schema*', (r) => r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await gotoTerminal(page, 'db1')
  await expect(page.locator('.statusbar')).toContainText('dba_l2')
  await expect(page.locator('.statusbar')).not.toContainText('strict')
  await expect(page.locator('.inst .tag')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('网关策略')
  await expect(page.locator('.xterm-rows')).toContainText('会话已连接')
})
