import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 表结构对比页。钉住:
//   · 右边只列同一引擎族的实例,其它环境在前、左边自己排最后(同实例两个库也能比);库名默认与左边同名
//   · 对比结果三组展示,展开一张不同的表看左右对照
//   · 读失败说清哪一边;两边一致明确说一致
//   · 导出 Markdown / CSV
//   · 库名是可筛选的下拉,也能直接用输入的名字;读不到库列表退回输入框

const CONNS = [
  { id: 1, name: 'orders-prod', env: 'prod', engine: 'MySQL 8.0', family: 'mysql', database: 'orders' },
  { id: 2, name: 'orders-uat', env: 'uat', engine: 'MySQL 8.0', family: 'mysql', database: 'orders_uat' },
  { id: 3, name: 'hr-oracle', env: 'prod', engine: 'Oracle 19c', family: 'oracle', database: 'HR' },
  { id: 4, name: 'dw-prod', env: 'prod', engine: 'PostgreSQL 16', family: 'postgres', database: 'dw' },
  { id: 5, name: 'dw-uat', env: 'uat', engine: 'PostgreSQL 16', family: 'postgres', database: 'dw' },
]
const C = (name: string, dataType: string, ordinal: number) => ({ name, dataType, nullable: true, hasDefault: false, default: '', comment: '', ordinal })
const SIDE = (id: number, name: string, env: string) => ({ connectionId: id, instance: name, env, engine: 'MySQL 8.0', database: 'orders', readAt: '2026-09-29T10:00:00Z', tables: 3 })
const RESULT = {
  left: SIDE(1, 'orders-prod', 'prod'), right: SIDE(2, 'orders-uat', 'uat'),
  diff: {
    summary: { left: 3, right: 3, same: 1, onlyLeft: 1, onlyRight: 1, changed: 1 },
    same: ['t_user'], onlyLeft: ['t_legacy'], onlyRight: ['t_new'],
    changed: [{
      table: 't_order',
      columns: [
        { column: 'note', status: 'changed', changes: [{ attr: 'type', left: 'varchar(100)', right: 'varchar(200)' }] },
        { column: 'remark', status: 'onlyRight', right: 'text NULL' },
      ],
      indexes: [{ index: 'idx_status', status: 'onlyRight', right: '(status)' }],
      leftDef: { name: 't_order', kind: 'table', comment: '', primaryKey: ['id'], columns: [C('id', 'bigint', 1), C('note', 'varchar(100)', 2)], indexes: [] },
      rightDef: { name: 't_order', kind: 'table', comment: '', primaryKey: ['id'], columns: [C('id', 'bigint', 1), C('note', 'varchar(200)', 2), C('remark', 'text', 3)],
        indexes: [{ name: 'idx_status', columns: ['status'], unique: false }] },
    }],
  },
}

// 各实例上的库(库名下拉);hr-oracle 读不到库列表
const DBS: Record<string, any> = {
  1: { databases: ['orders', 'orders_archive', 'orders_uat'] },
  2: { databases: ['orders', 'orders_hist'] },
  3: { databases: [], error: 'ORA-12541: TNS:no listener' },
  4: { databases: ['dw', 'postgres'] },
  5: { databases: ['dw', 'postgres'] },
}

async function open(page: Page, reply: (body: any) => any = () => envelope(RESULT)) {
  const bodies: any[] = []
  await stubCommon(page)
  // PG 库里的 schema:按实例给(dw-prod 有 app,dw-uat 有 app_uat)
  await page.route('**/api/v1/schema-diff/connections/*/schemas*', (r) => {
    const id = new URL(r.request().url()).pathname.split('/').slice(-2)[0]
    return r.fulfill(envelope({ schemas: id === '4' ? ['app', 'public'] : ['app_uat', 'public'] }))
  })
  await page.route('**/api/v1/schema-diff/connections/*/databases', (r) =>
    r.fulfill(envelope(DBS[r.request().url().split('/').slice(-2)[0]])))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...(ME as any).menus, schemadiff: true } })))
  await page.route('**/api/v1/schema-diff/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/schema-diff', (r) => {
    const b = r.request().postDataJSON()
    bodies.push(b)
    return r.fulfill(reply(b))
  })
  await page.goto('/#/schema-diff')
  await expect(page.getByTestId('sd-left')).toBeVisible()
  return bodies
}

test('右边只列同族实例,库名默认同左边;对比结果三组展示,展开看对照', async ({ page }) => {
  const bodies = await open(page)
  await page.getByTestId('sd-right').locator('.control').click()
  // 不含 Oracle;其它环境在前,左边自己排最后(同一实例里的两个库也要能比)
  await expect(page.getByTestId('sd-right').locator('.opt')).toHaveText(['orders-uat · UAT', 'orders-prod · PROD'])
  await page.getByTestId('sd-right').locator('.opt').first().click()
  await expect(page.getByTestId('sd-right-db').locator('.val')).toHaveText('orders') // 比的是同一个库:默认同名,可改
  await page.getByTestId('sd-run').click()

  await expect(page.getByTestId('sd-summary')).toContainText('orders-prod')
  await expect(page.getByTestId('sd-only-left')).toContainText('t_legacy')
  await expect(page.getByTestId('sd-only-right')).toContainText('t_new')
  await page.getByTestId('sd-changed-table').click()
  // 左右完整对照:不同的格子高亮,只在一边的整行标色,索引按服务端差异标色
  const rows = page.getByTestId('sd-col-row')
  await expect(rows).toHaveCount(3)
  await expect(rows.nth(1).locator('td.hl')).toHaveText(['varchar(100)', 'varchar(200)'])
  await expect(rows.nth(2)).toHaveClass(/only-right/)
  await expect(rows.nth(2)).toContainText('没有这一列')
  await expect(page.getByTestId('sd-index').filter({ hasText: 'idx_status' })).toHaveClass(/only/)
  expect(bodies[0]).toMatchObject({ left: { connectionId: 1, database: 'orders' }, right: { connectionId: 2, database: 'orders' } })
})

test('读失败:说清是哪一边,不出半份报告;按原因代码显示读者的语言', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await open(page, () => envelope({ left: SIDE(1, 'orders-prod', 'prod'), right: SIDE(2, 'orders-uat', 'uat'),
    error: '右边(orders-uat/orders)读取表结构失败:dial tcp: i/o timeout', errorSide: 'right',
    errorRef: { code: 'sdReadFailed', args: { instance: 'orders-uat', db: 'orders-uat/orders', err: 'dial tcp: i/o timeout' } } }))
  await page.getByTestId('sd-run').click()
  await expect(page.getByTestId('sd-error')).toContainText('Right')
  await expect(page.getByTestId('sd-error')).toContainText('dial tcp: i/o timeout') // 驱动原文不翻译
  await expect(page.getByTestId('sd-error')).not.toContainText('读取')
  await expect(page.getByTestId('sd-summary')).toHaveCount(0)
})

test('同一实例的两个库也能比', async ({ page }) => {
  const bodies = await open(page)
  await page.getByTestId('sd-right').locator('.control').click()
  await page.getByTestId('sd-right').locator('.opt', { hasText: 'orders-prod' }).click()
  await pickDb(page, 'sd-right-db', 'uat', 'orders_uat')
  await page.getByTestId('sd-run').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0]).toMatchObject({ left: { connectionId: 1, database: 'orders' }, right: { connectionId: 1, database: 'orders_uat' } })
})

async function pickDb(page: Page, id: string, kw: string, pick: string) {
  await page.getByTestId(id).locator('.control').click()
  await page.getByTestId(id).locator('.sbox input').fill(kw)
  await page.getByTestId(id).locator('.opt', { hasText: pick }).first().click()
}

test('库名下拉:列出实例上的库,输入即筛选', async ({ page }) => {
  await open(page)
  const db = page.getByTestId('sd-left-db')
  await db.locator('.control').click()
  await expect(db.locator('.opt')).toHaveText(['orders', 'orders_archive', 'orders_uat'])
  await db.locator('.sbox input').fill('arch')
  await expect(db.locator('.opt:not(.custom)')).toHaveText(['orders_archive'])
  await db.locator('.sbox input').press('Enter') // 回车取第一个匹配
  await expect(db.locator('.val')).toHaveText('orders_archive')
})

test('库名下拉:列表里没有的库,可以直接用输入的名字', async ({ page }) => {
  const bodies = await open(page)
  const db = page.getByTestId('sd-right-db')
  await db.locator('.control').click()
  await db.locator('.sbox input').fill('orders_new')
  await expect(db.getByTestId('vsel-custom')).toContainText('orders_new')
  await db.locator('.sbox input').press('Enter') // 没有匹配项:回车就用输入值
  await expect(db.locator('.val')).toHaveText('orders_new')
  await page.getByTestId('sd-run').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0]).toMatchObject({ right: { database: 'orders_new' } })
})

test('读不到库列表:退回输入框,照样能对比', async ({ page }) => {
  await open(page)
  await page.getByTestId('sd-left').locator('.control').click()
  await page.getByTestId('sd-left').locator('.opt', { hasText: 'hr-oracle' }).click()
  const db = page.getByTestId('sd-left-db')
  await expect(db).toHaveJSProperty('tagName', 'INPUT')
  await expect(db).toHaveValue('HR')
  await expect(db).toHaveAttribute('title', /TNS/)
})

test('对比前被拒(无权访问一边):按原因代码显示读者的语言', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await open(page, () => ({ contentType: 'application/json', body: JSON.stringify({ code: 40001, msg: '右边的实例不存在或无权访问',
    data: { reasonRef: { code: 'sdNoConn', args: { side: 'right' } } } }) }))
  await page.getByTestId('sd-run').click()
  await expect(page.getByTestId('sd-error')).toContainText('Right')
  await expect(page.getByTestId('sd-error')).toContainText('no access')
  await expect(page.getByTestId('sd-error')).not.toContainText('权限')
})

test('一边是空库:照常出报告,另提示确认库名', async ({ page }) => {
  await open(page, () => envelope({ ...RESULT, emptySide: 'right',
    diff: { summary: { left: 1, right: 0, same: 0, onlyLeft: 1, onlyRight: 0, changed: 0 }, same: [], onlyLeft: ['t_user'], onlyRight: [], changed: [] } }))
  await page.getByTestId('sd-run').click()
  await expect(page.getByTestId('sd-empty')).toContainText('右边没有读到任何表')
  await expect(page.getByTestId('sd-only-left')).toContainText('t_user')
  await expect(page.getByTestId('sd-all-same')).toHaveCount(0)
})

test('PG:可以指定 schema,请求里带上', async ({ page }) => {
  const bodies = await open(page)
  await expect(page.getByTestId('sd-left-schema')).toHaveCount(0) // 左边是 MySQL,没有 schema 这一层
  await page.getByTestId('sd-left').locator('.control').click()
  await page.getByTestId('sd-left').locator('.opt', { hasText: 'dw-prod' }).click()
  // schema 也是下拉:第一项「全部 schema」= 留空,其余是这个库里的 schema,输入即筛选
  const ls = page.getByTestId('sd-left-schema')
  await ls.locator('.control').click()
  await expect(ls.locator('.opt')).toHaveText(['(全部 schema)', 'app', 'public'])
  await ls.locator('.opt', { hasText: /^\s*app\s*$/ }).click()
  await pickDb(page, 'sd-right-schema', 'uat', 'app_uat')
  await page.getByTestId('sd-run').click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0]).toMatchObject({ left: { connectionId: 4, schema: 'app' }, right: { connectionId: 5, schema: 'app_uat' } })

  // 换回「全部 schema」就是留空
  await ls.locator('.control').click()
  await ls.locator('.opt', { hasText: '全部 schema' }).click()
  await expect(ls.locator('.val')).toHaveText('schema(留空 = 库内全部 schema)')
  await page.getByTestId('sd-run').click()
  await expect.poll(() => bodies.length).toBe(2)
  expect(bodies[1].left.schema || '').toBe('')
})

test('两边一致:明确说一致', async ({ page }) => {
  await open(page, () => envelope({ ...RESULT, diff: { summary: { left: 1, right: 1, same: 1, onlyLeft: 0, onlyRight: 0, changed: 0 }, same: ['t_user'], onlyLeft: [], onlyRight: [], changed: [] } }))
  await page.getByTestId('sd-run').click()
  await expect(page.getByTestId('sd-all-same')).toContainText('一致')
})

test('导出 Markdown 与 CSV', async ({ page }) => {
  await open(page)
  await page.getByTestId('sd-run').click()
  await expect(page.getByTestId('sd-summary')).toBeVisible()
  const [md] = await Promise.all([page.waitForEvent('download'), page.getByTestId('sd-export-md').click()])
  expect(md.suggestedFilename()).toMatch(/\.md$/)
  const [csv] = await Promise.all([page.waitForEvent('download'), page.getByTestId('sd-export-csv').click()])
  expect(csv.suggestedFilename()).toMatch(/\.csv$/)
})
