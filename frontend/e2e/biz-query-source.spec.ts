import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 业务方数据查询 · 第二期的两处界面:
//
//   1. 数据源页的「业务查询源」标记。只有打了标记的实例能被查询授权引用;标记前确认框要说清
//      "应指向从库、用只读库账号",取消前要说清"已有授权会失效"。
//   2. 开放接口凭据的 grant:read / grant:write。它们给外部审批系统下发查询授权用,**默认不勾**
//      —— 一把 CI 凭据不该顺带拿到给人开生产查询权的能力;勾上时要有提醒。

const CONNS = [
  { id: 7, name: 'orders-replica', env: 'prod', engine: 'mysql', host: '10.0.0.7', port: 3306, policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'orders', status: 'online', bizQuery: false },
  { id: 8, name: 'finance-replica', env: 'prod', engine: 'mysql', host: '10.0.0.8', port: 3306, policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'fin', status: 'online', bizQuery: true },
]

async function openConnections(page: Page) {
  const patches: { id: string; body: any }[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route(/\/api\/v1\/connections\/\d+$/, (r) => {
    if (r.request().method() !== 'PATCH') return r.fallback()
    patches.push({ id: r.request().url().split('/').pop()!, body: r.request().postDataJSON() })
    return r.fulfill(envelope({}))
  })
  await page.goto('/#/connections')
  await expect(page.getByTestId('biz-7')).toBeVisible()
  return patches
}

test('标记业务查询源:确认框说清从库与只读账号,发出 bizQuery=true', async ({ page }) => {
  const patches = await openConnections(page)
  await expect(page.getByTestId('biz-8')).toHaveClass(/on/)
  await expect(page.getByTestId('biz-7')).not.toHaveClass(/on/)
  await page.getByTestId('biz-7').click()
  await expect(page.getByTestId('confirm-message')).toContainText('从库')
  await expect(page.getByTestId('confirm-message')).toContainText('只读')
  await page.getByTestId('confirm-ok').click()
  await expect.poll(() => patches.length).toBe(1)
  expect(patches[0]).toEqual({ id: '7', body: { bizQuery: true } })
})

test('取消标记:先说清已有授权会失效,取消确认就什么都不发', async ({ page }) => {
  const patches = await openConnections(page)
  await page.getByTestId('biz-8').click()
  await expect(page.getByTestId('confirm-message')).toContainText('不再生效')
  await page.getByTestId('confirm-cancel').click()
  await expect(page.getByTestId('confirm-dialog')).toHaveCount(0)
  await page.waitForTimeout(200)
  expect(patches).toEqual([])
})

test('新建凭据:grant 范围默认不勾,勾上时出现提醒', async ({ page }) => {
  await stubCommon(page)
  for (const [path, data] of [
    ['**/api/v1/api-clients', []], ['**/api/v1/service-accounts', []], ['**/api/v1/pipelines', []],
  ] as const) {
    await page.route(path, (r) => r.fulfill(envelope(data)))
  }
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {} })))
  await page.goto('/#/settings')
  await page.locator('.tabs .tab', { hasText: '开放接口' }).click()
  await page.getByRole('button', { name: /新建凭据|New credential/ }).first().click()

  const scope = (s: string) => page.locator('.sc', { hasText: s })
  await expect(scope('release:create')).toHaveClass(/on/)
  await expect(scope('grant:read')).not.toHaveClass(/on/)
  await expect(scope('grant:write')).not.toHaveClass(/on/)
  await expect(page.getByTestId('grant-scope-warn')).toHaveCount(0)
  await scope('grant:write').click()
  await expect(page.getByTestId('grant-scope-warn')).toBeVisible()
})
