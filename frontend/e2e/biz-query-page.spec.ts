import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 业务方的数据查询页。钉住:
//   · 只有数据查询权限的人:总览直接送到 /query,导航里只有这一项
//   · 「库」下拉只列此刻生效的授权(未到开始时间的另起一行说明);选一个库执行,请求带的是那个实例和库
//   · 「表」下拉列授权库里的表,选了就写出查询;多个页签各有各的库、SQL 与结果,刷新后草稿还在
//   · 截断、脱敏、超时、被拒 —— 服务端说的话原样说清楚
//   · 没有任何授权时告诉人去哪儿申请

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = (id: number, db: string, status = 'active') => ({
  id, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: db,
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2026-10-25T09:00:00+08:00', status,
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
})

async function open(page: Page, grants: any[], exec?: (body: any) => any, tables: Record<string, string[]> = {}, limits: Record<string, unknown> = {}) {
  const bodies: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants, limits })))
  await page.route('**/api/v1/bizquery/tables**', (r) => {
    const db = new URL(r.request().url()).searchParams.get('database') || ''
    return r.fulfill(envelope({ tables: tables[db] || [], truncated: false }))
  })
  await page.route('**/api/v1/bizquery/exec', (r) => {
    const b = r.request().postDataJSON()
    bodies.push(b)
    return r.fulfill(exec ? exec(b) : envelope({ columns: ['id'], rows: [['1']], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 5, timedOut: false }))
  })
  await page.goto('/#/dashboard')
  await expect(page).toHaveURL(/#\/query/)
  return bodies
}

// 导航里除了数据查询,还有「我的操作记录」—— 他查过什么,自己也该看得到(2026-10-14 用户确认)。
// 总览这类常驻项仍藏着。
test('只有数据查询权限:总览送到数据查询页,导航只有数据查询与我的操作记录', async ({ page }) => {
  await open(page, [G(1, 'orders_db')])
  const rail = page.locator('.rail-item')
  await expect(rail).toHaveCount(2)
  await expect(rail.nth(0)).toHaveAttribute('title', '数据查询')
  await expect(rail.nth(1)).toHaveAttribute('title', '记录')
})

// 2026-10-26 回归排查 19:迁移给 DBA L2 回填了巡检菜单。被定制成只有「查询」的 L2 不能因为多出 inspection
// 就不算"只开查询" —— 照样送到数据查询页;巡检入口仍按菜单出现在导航里。
test('只开查询的 DBA L2 多了回填的巡检菜单:照样算只开查询,巡检入口还在', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...BIZ_ME, roleCodes: ['l2'], roleCode: 'l2', menus: { query: true, inspection: true } })))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [G(1, 'orders_db')], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.goto('/#/dashboard')
  await expect(page).toHaveURL(/#\/query/)
  const rail = page.locator('.rail-item')
  await expect(rail).toHaveCount(3)
  await expect(page.locator('.rail-item[title="数据查询"]')).toHaveCount(1)
  await expect(page.locator('.rail-item[title="巡检"]')).toHaveCount(1)
})

// 回归:多开了别的菜单(比如工单)就不是只开查询,总览照常停留
test('查询之外还有别的菜单:不算只开查询,总览照常', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...BIZ_ME, roleCodes: ['l2'], roleCode: 'l2', menus: { query: true, inspection: true, approve: true } })))
  await page.goto('/#/dashboard')
  await expect(page.locator('.rail-item[title="总览"]')).toBeVisible()
  await expect(page).toHaveURL(/#\/dashboard/)
})

test('选库执行:请求带着实例与库;截断与脱敏都说出来', async ({ page }) => {
  const bodies = await open(page, [G(1, 'orders_db'), G(2, 'finance_db'), G(3, 'hr_db', 'pending')], () =>
    envelope({
      columns: ['id', 'phone'], rows: Array.from({ length: 1000 }, (_, i) => [String(i + 1), '138****0000']),
      truncated: true, maxRows: 1000, timeoutSec: 30, ms: 42, timedOut: false, maskedColumns: ['phone'],
    }))
  await expect(page.locator('.qtarget')).toContainText('orders_db') // 默认第一个生效的库
  await page.getByTestId('q-db').locator('.control').click()
  // 未到开始时间的不进下拉,另起一行说明
  await expect(page.getByTestId('q-db').locator('.opt')).toHaveText(['orders-replica / orders_db', 'orders-replica / finance_db'])
  await expect(page.locator('.qmeta')).toContainText('hr_db')
  await page.getByTestId('q-db').locator('.opt', { hasText: 'finance_db' }).click()
  await expect(page.locator('.qtarget')).toContainText('finance_db')
  await page.getByTestId('q-sql').fill('SELECT id, phone FROM customers')
  await page.getByTestId('q-sql').press('Control+Enter')
  await expect(page.getByTestId('q-truncated')).toContainText('1000')
  await expect(page.getByTestId('q-masked')).toContainText('phone')
  await expect(page.getByTestId('q-stat')).toContainText('42 ms')
  expect(bodies).toEqual([{ sourceId: 12, database: 'finance_db', sql: 'SELECT id, phone FROM customers' }])
})

test('被拒:原样显示服务端的原因', async ({ page }) => {
  await open(page, [G(1, 'orders_db')], () => ({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40300, msg: '数据查询只允许 SELECT 查询(含 WITH … SELECT、EXPLAIN),不能执行 UPDATE', data: null }) }))
  await page.getByTestId('q-sql').fill('UPDATE t SET a = 1')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-denied')).toContainText('不能执行 UPDATE')
})

// 服务端给 msg(规范中文串)和 data.reasonRef(code + 参数):界面按读者语言渲染,
// 并按 code —— 不是按中文原话 —— 决定要不要刷新左侧授权列表。
const denial = (code: string, args: Record<string, string>, msg: string) => ({ status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 40300, msg, data: { reasonRef: { code, args } } }) })

test('被拒:英文界面按 reasonRef 显示英文原因', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await open(page, [G(1, 'orders_db')], () => denial('bizNotSelect', { verb: 'UPDATE' },
    '数据查询只允许 SELECT 查询(含 WITH … SELECT、EXPLAIN),不能执行 UPDATE'))
  await page.getByTestId('q-sql').fill('UPDATE t SET a = 1')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-denied')).toContainText('UPDATE')
  await expect(page.getByTestId('q-denied')).not.toContainText('数据查询')
})

test('被拒:授权失效按 code 刷新授权列表,不看原话怎么写', async ({ page }) => {
  let loads = 0
  page.on('request', (r) => { if (r.url().includes('/api/v1/bizquery/grants')) loads++ })
  await open(page, [G(1, 'orders_db')], () => denial('bizNoGrant', { source: 'orders-replica', db: 'orders_db' }, '原话改过了'))
  await expect.poll(() => loads).toBe(1)
  await page.getByTestId('q-sql').fill('SELECT 1')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-denied')).toContainText('orders_db')
  await expect.poll(() => loads).toBe(2)
})

test('超时:显示为自动取消的警告,不是报错', async ({ page }) => {
  await open(page, [G(1, 'orders_db')], () => envelope({
    columns: [], rows: [], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 30010, timedOut: true, error: '查询超过 30 秒,已自动取消',
  }))
  await page.getByTestId('q-sql').fill('SELECT * FROM big')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-error')).toContainText('自动取消')
  await expect(page.getByTestId('q-error')).toHaveClass(/warn/)
})

test('超时:英文界面按 errorRef 显示英文', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await open(page, [G(1, 'orders_db')], () => envelope({
    columns: [], rows: [], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 30010, timedOut: true,
    error: '查询超过 30 秒,已自动取消', errorRef: { code: 'bizExecTimeout', args: { sec: '30' } },
  }))
  await page.getByTestId('q-sql').fill('SELECT * FROM big')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-error')).toContainText('30')
  await expect(page.getByTestId('q-error')).not.toContainText('自动取消')
})

// 外部申请(applyMode=external)又没配申请页地址:不给链接,只告诉人去审批系统申请。
// 网关内申请(默认)的拦截卡片见 biz-query-apply.spec.ts。
test('外部申请且没有地址:告诉人去审批系统申请', async ({ page }) => {
  await open(page, [], undefined, {}, { applyMode: 'external' })
  await expect(page.getByTestId('q-no-grants')).toContainText('审批系统')
  await expect(page.getByTestId('q-apply')).toHaveCount(0)
})

test('选表:空编辑器写出查询;已有内容时把表名插到光标处', async ({ page }) => {
  await open(page, [G(1, 'orders_db')], undefined, { orders_db: ['customers', 'orders'] })
  const pick = async (name: string) => {
    await page.getByTestId('q-table').locator('.control').click()
    await page.getByTestId('q-table').locator('.opt', { hasText: name }).click()
  }
  await pick('customers')
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT * FROM customers')
  await page.getByTestId('q-sql').fill('SELECT * FROM  WHERE 1 = 1')
  await page.getByTestId('q-sql').evaluate((el: HTMLTextAreaElement) => el.setSelectionRange(14, 14))
  await pick('orders')
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT * FROM orders WHERE 1 = 1')
})

test('多个页签:各有各的库、SQL 与结果;刷新后页签与草稿还在', async ({ page }) => {
  const bodies = await open(page, [G(1, 'orders_db'), G(2, 'finance_db')], (b) =>
    envelope({ columns: ['db'], rows: [[b.database]], truncated: false, maxRows: 1000, timeoutSec: 30, ms: 3, timedOut: false }))
  await page.getByTestId('q-sql').fill('SELECT 1')
  await page.getByTestId('q-tab-add').click()
  await expect(page.getByTestId('q-tab')).toHaveCount(2)
  await page.getByTestId('q-db').locator('.control').click()
  await page.getByTestId('q-db').locator('.opt', { hasText: 'finance_db' }).click()
  await page.getByTestId('q-sql').fill('SELECT 2')
  await page.getByTestId('q-run').click()
  await expect(page.getByTestId('q-stat')).toBeVisible()

  // 切回第一页:SQL 还是自己的,库还是 orders_db,没有第二页的结果
  await page.getByTestId('q-tab').first().click()
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT 1')
  await expect(page.locator('.qtarget')).toContainText('orders_db')
  await expect(page.getByTestId('q-stat')).toHaveCount(0)
  expect(bodies).toEqual([{ sourceId: 12, database: 'finance_db', sql: 'SELECT 2' }])

  // 刷新:页签、库和草稿都回来,结果不回来(生产数据不落浏览器)
  await page.waitForTimeout(600) // 草稿延迟 400ms 落盘
  await page.reload()
  await expect(page.getByTestId('q-tab')).toHaveCount(2)
  await page.getByTestId('q-tab').nth(1).click()
  await expect(page.getByTestId('q-sql')).toHaveValue('SELECT 2')
  await expect(page.locator('.qtarget')).toContainText('finance_db')
  await expect(page.getByTestId('q-stat')).toHaveCount(0)

  // 关掉一个;最后一个关掉是清空,不是没有页签
  await page.getByTestId('q-tab-close').nth(1).click()
  await expect(page.getByTestId('q-tab')).toHaveCount(1)
  await page.getByTestId('q-tab-close').click()
  await expect(page.getByTestId('q-tab')).toHaveCount(1)
  await expect(page.getByTestId('q-sql')).toHaveValue('')
})

// 2026-10-22:没有任何生效的授权 → 拦截卡片,「去申请权限」新开标签页到申请页(外部申请模式);
// 申请批下来后点「重新检查」就进到查询页,不用刷新。左侧导航不挡(「记录」照样能进)。
test('没有授权:拦截卡片指去申请页;获批后重新检查即可进入', async ({ page }) => {
  let grants: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) =>
    r.fulfill(envelope({ grants, limits: { applyMode: 'external', applyUrl: 'https://srd.bssrvc66.com/service-center/query/db-gateway-grant' } })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.goto('/#/query')

  const card = page.getByTestId('q-no-grants')
  await expect(card).toContainText('暂无数据查询权限')
  const apply = page.getByTestId('q-apply')
  await expect(apply).toHaveAttribute('href', 'https://srd.bssrvc66.com/service-center/query/db-gateway-grant')
  await expect(apply).toHaveAttribute('target', '_blank')
  await expect(page.getByTestId('q-sql')).toHaveCount(0) // 编辑器被拦在外面
  await expect(page.locator('.rail-item')).toHaveCount(2) // 导航还在

  grants = [G(1, 'orders_db')]
  await page.getByTestId('q-recheck').click()
  await expect(page.getByTestId('q-no-grants')).toHaveCount(0)
  await expect(page.getByTestId('q-sql')).toBeVisible()
})

// 2026-11-03 生产报障:几条不带分号、空行隔开的 SELECT,只选中其中一行按执行,发出去的却是整段
// 编辑器内容 —— 拼成一句,DWS 报 syntax error at or near "select"。有选区只执行选区;
// 没有选区执行光标所在的那条(分号或空行为界)。
const FOUR = [
  "select * from g_dim_pagcor_game_kind_df where platform_id='524'",
  '',
  "select * from g_dim_pagcor_game_kind_df where platform_id='175'",
  '',
  "select * from g_dim_pagcor_game_kind_df where platform_id='524'",
  '',
  "select * from g_dim_pagcor_game_kind_df where platform_id='524'",
].join('\n')

test('选中一行执行:只发选中的那条', async ({ page }) => {
  const bodies = await open(page, [G(1, 'orders_db')])
  const ed = page.getByTestId('q-sql')
  await ed.fill(FOUR)
  const line3 = "select * from g_dim_pagcor_game_kind_df where platform_id='175'"
  await ed.evaluate((el: HTMLTextAreaElement, s: string) => { const at = el.value.indexOf(s); el.setSelectionRange(at, at + s.length) }, line3)
  await ed.press('Control+Enter')
  await expect(page.getByTestId('q-stat')).toBeVisible()
  expect(bodies.map((b) => b.sql)).toEqual([line3])
  // 点工具栏的「执行」按钮(编辑器失焦)也一样只执行选区
  await page.getByTestId('q-run').click()
  await expect.poll(() => bodies.length).toBe(2)
  expect(bodies[1].sql).toBe(line3)
})

test('没有选区:执行光标所在的那条,空行与分号都是边界', async ({ page }) => {
  const bodies = await open(page, [G(1, 'orders_db')])
  const ed = page.getByTestId('q-sql')
  await ed.fill(FOUR)
  // 光标放在第 3 行中间
  await ed.evaluate((el: HTMLTextAreaElement) => { const at = el.value.indexOf("'175'"); el.setSelectionRange(at, at) })
  await ed.press('Control+Enter')
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].sql).toBe("select * from g_dim_pagcor_game_kind_df where platform_id='175'")
  // 有分号的多条:光标在第二条上只发第二条
  await ed.fill('SELECT 1;\nSELECT 2;\n')
  await ed.evaluate((el: HTMLTextAreaElement) => el.setSelectionRange(14, 14))
  await ed.press('Control+Enter')
  await expect.poll(() => bodies.length).toBe(2)
  expect(bodies[1].sql).toBe('SELECT 2')
})
