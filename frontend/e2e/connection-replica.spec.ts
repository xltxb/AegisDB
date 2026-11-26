import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 数据源的从库:配了之后业务方的数据查询都由从库提供。钉住:
//   · 编辑数据源时能填从库,保存发出的请求带着这组字段
//   · 配了从库的数据源在列表上有标记
//   · 数据查询结果注明由谁提供 —— 从库,或者从库连不上、回落了主库

const MYSQL = {
  id: 1, name: 'orders-prod', engine: 'MySQL 8.0', env: 'prod', host: '10.0.0.1', port: 3306,
  policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'orders', username: 'app',
  status: 'online', replicaHost: '', replicaPort: 0, replicaUsername: '',
}

async function openConnections(page: Page, conns: any[]) {
  const puts: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(conns)))
  await page.route(/\/api\/v1\/connections\/\d+$/, (r) => {
    if (r.request().method() !== 'PUT') return r.fallback()
    const body = r.request().postDataJSON()
    puts.push(body)
    return r.fulfill(envelope({ ...conns[0], ...body }))
  })
  await page.route(/\/api\/v1\/connections\/\d+\/test$/, (r) => r.fulfill(envelope({ ok: true })))
  await page.goto('/#/connections')
  await page.locator('.opscell').first().waitFor({ state: 'visible', timeout: 15000 })
  return puts
}

test('编辑数据源:填从库,保存的请求带着从库地址与账号', async ({ page }) => {
  const puts = await openConnections(page, [MYSQL])
  await page.locator('button[title="编辑"]').first().click()
  await expect(page.getByTestId('conn-replica')).toBeVisible()
  await page.getByTestId('conn-replica-host').fill('10.0.0.9:3307')
  await page.getByTestId('conn-replica').locator('input').nth(1).fill('ro_user')
  await page.getByTestId('conn-replica').locator('input').nth(2).fill('secret')
  await page.locator('.ce-foot button').last().click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toMatchObject({ replicaHost: '10.0.0.9:3307', replicaUsername: 'ro_user', replicaPassword: 'secret' })
})

test('配了从库的数据源在列表上有标记', async ({ page }) => {
  await openConnections(page, [{ ...MYSQL, replicaHost: '10.0.0.9', replicaPort: 3307 }])
  const tag = page.getByTestId('replica-1')
  await expect(tag).toContainText('从库')
  await expect(tag).toHaveAttribute('title', /10\.0\.0\.9:3307/)
})

// ---------------------------------------------------------------- 数据查询页

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const GRANT = {
  id: 1, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: 'orders_db',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2026-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
}

async function runQuery(page: Page, extra: Record<string, unknown>) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [GRANT], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.route('**/api/v1/bizquery/exec', (r) => r.fulfill(envelope({
    columns: ['id'], rows: [['1']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 5, timedOut: false, ...extra,
  })))
  await page.goto('/#/query')
  await page.getByTestId('q-sql').fill('SELECT 1')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-stat')).toBeVisible()
}

test('数据查询:由从库提供时注明', async ({ page }) => {
  await runQuery(page, { served: 'replica' })
  await expect(page.getByTestId('q-served')).toHaveText('从库')
})

test('数据查询:从库连不上回落主库时说清楚,原因在提示里', async ({ page }) => {
  await runQuery(page, { served: 'primaryFallback', replicaError: 'dial tcp 10.0.0.9:3307: connect: connection refused' })
  await expect(page.getByTestId('q-served')).toContainText('主库')
  await expect(page.getByTestId('q-served')).toHaveAttribute('title', /connection refused/)
})

test('数据查询:数据源没配从库时不多说一句', async ({ page }) => {
  await runQuery(page, {})
  await expect(page.getByTestId('q-served')).toHaveCount(0)
})

// 改了地址就要当场重新输入口令(服务端规则,2026-10 安全审计 M4)。保存之前就在密码框下面说,
// 而不是点了保存才看到一句报错;输入之后提示消失。
test('从库地址改了且沿用主库账号:提前提示重新输入主库口令,输入后提示消失', async ({ page }) => {
  await openConnections(page, [MYSQL])
  await page.locator('button[title="编辑"]').first().click()
  await expect(page.getByTestId('conn-need-main-pw-replica')).toHaveCount(0) // 没改地址:不提示
  await page.getByTestId('conn-replica-host').fill('10.0.0.9:3307')
  await expect(page.getByTestId('conn-need-main-pw-replica')).toContainText('请在上面重新输入主库口令')
  await expect(page.getByTestId('conn-edit-password')).toHaveClass(/invalid/)
  await page.getByTestId('conn-edit-password').fill('secret')
  await expect(page.getByTestId('conn-need-main-pw-replica')).toHaveCount(0)
  // 给从库填自己的账号也行
  await page.getByTestId('conn-edit-password').fill('')
  await expect(page.getByTestId('conn-need-main-pw-replica')).toBeVisible()
  await page.getByTestId('conn-replica').locator('input').nth(1).fill('ro_user')
  await expect(page.getByTestId('conn-need-main-pw-replica')).toHaveCount(0)
})

test('主库地址改了:提前提示重新输入口令;从库已有账号、地址改了:提示重新输入从库口令', async ({ page }) => {
  await openConnections(page, [{ ...MYSQL, replicaHost: '10.0.0.9', replicaPort: 3307, replicaUsername: 'ro' }])
  await page.locator('button[title="编辑"]').first().click()
  await page.locator('.ce-card input').nth(1).fill('10.0.0.2:3306') // 地址栏(名称、地址 两个文本框里的第二个)
  await expect(page.getByTestId('conn-need-main-pw')).toContainText('请重新输入主库口令')
  await page.getByTestId('conn-replica-host').fill('10.0.0.10:3307')
  await expect(page.getByTestId('conn-need-replica-pw')).toContainText('请重新输入从库口令')
  await page.getByTestId('conn-replica').locator('input').nth(2).fill('ro-secret')
  await expect(page.getByTestId('conn-need-replica-pw')).toHaveCount(0)
})
