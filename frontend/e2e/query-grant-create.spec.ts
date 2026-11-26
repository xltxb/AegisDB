import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 权限 · 查询授权 ·「新建授权」(issue #173):管理员直接给已有账号开通一个库。钉住:
//   · 请求体就是表单上填的(邮箱 / 实例 / 库 / 天数 / 行数 / 超时 / 导出 / 备注);
//   · 服务端拒绝时原样显示原因,不关框;成功后关框并重新拉列表。

const LIMITS = { defaultMaxRows: 1000, maxRowsCap: 10000, defaultTimeoutSec: 30, timeoutCapSec: 300, maxGrantDays: 90, maxConcurrentPerUser: 2, maxConcurrentPerSource: 5, exportEnabled: true }
const G = (id: number) => ({
  id, email: `u${id}@corp.io`, userName: `用户${id}`, dept: '财务部', sourceId: 12, source: 'orders-replica', database: 'orders_db',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-11-27T09:00:00+08:00', endsAt: '2026-12-27T09:00:00+08:00', status: 'active',
  externalId: `console-${id}`, client: '', approver: 'Lin Wei', reason: '',
})

test('管理员新建授权:请求体是表单上填的,成功后重新拉列表', async ({ page }) => {
  const posts: any[] = []
  let lists = 0
  await stubCommon(page)
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  await page.route(/\/api\/v1\/query-grants(\?.*)?$/, (r) => {
    if (r.request().method() === 'POST') {
      posts.push(r.request().postDataJSON())
      if (posts.length === 1) {
        return r.fulfill({ status: 200, contentType: 'application/json',
          body: JSON.stringify({ code: 40001, msg: '网关里没有账号 nobody@corp.io,请先在【权限 · 用户】新建或邀请', data: null }) })
      }
      return r.fulfill(envelope(G(2)))
    }
    lists++
    return r.fulfill(envelope({ items: lists > 1 ? [G(1), G(2)] : [G(1)], limits: LIMITS }))
  })
  await page.route('**/api/v1/bizquery/apply/sources', (r) => r.fulfill(envelope([{ id: 12, name: 'orders-replica', engine: 'mysql', env: 'prod', status: 'online' }])))
  await page.route('**/api/v1/bizquery/apply/sources/12/databases', (r) => r.fulfill(envelope(['orders_db', 'finance_db'])))
  await page.goto('/#/permissions')
  await page.getByTestId('tab-grants').click()
  await expect(page.getByTestId('qg-row')).toHaveCount(1)

  await page.getByTestId('qg-new').click()
  const modal = page.getByTestId('qg-new-modal')
  await expect(modal).toBeVisible()
  await page.getByTestId('qg-new-email').fill('nobody@corp.io')
  await page.getByTestId('qg-new-source').selectOption('12')
  await page.getByTestId('qg-new-db').selectOption('finance_db')
  await page.getByTestId('qg-new-days').fill('30')
  await page.getByTestId('qg-new-rows').fill('500')
  await page.getByTestId('qg-new-timeout').fill('60')
  await page.getByTestId('qg-new-export').check()
  await page.getByTestId('qg-new-reason').fill('审计抽查')
  await page.getByTestId('qg-new-submit').click()
  await expect(page.getByTestId('qg-new-err')).toContainText('网关里没有账号 nobody@corp.io')
  await expect(modal).toBeVisible()

  await page.getByTestId('qg-new-email').fill('u2@corp.io')
  await page.getByTestId('qg-new-submit').click()
  await expect(modal).toHaveCount(0)
  expect(posts[1]).toEqual({
    email: 'u2@corp.io', sourceId: 12, database: 'finance_db', days: 30, maxRows: 500, timeoutSec: 60, allowExport: true, reason: '审计抽查',
  })
  await expect(page.getByTestId('qg-row')).toHaveCount(2)
})
