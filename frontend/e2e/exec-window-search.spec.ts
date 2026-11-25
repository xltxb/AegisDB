import { test, expect } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 班车申请:实例、数据库下拉可以输入关键字筛选(实例一多,翻着找太慢)。

const CONNS = [
  { id: 1, name: 'UAT-MYSQL', env: 'uat', engine: 'MySQL 8.0' },
  { id: 2, name: 'UAT-TIDB', env: 'uat', engine: 'TiDB' },
  { id: 3, name: 'GLI-MYSQL', env: 'gli', engine: 'MySQL 8.0' },
  { id: 4, name: 'GLI-ORACLE', env: 'gli', engine: 'Oracle 23ai' },
]

test('班车申请:实例与数据库下拉输入关键字即可筛选,回车选中第一项', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...ME.menus, execwindow: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/exec-windows', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/connections/*/schema*', (r) =>
    r.fulfill(envelope({ databases: [{ name: 'order_db' }, { name: 'risk_control' }, { name: 'risk_cron' }] })))
  await page.goto('/#/exec-windows')
  await page.getByRole('button', { name: '申请窗口' }).click()

  const inst = page.getByTestId('ew-instance')
  await inst.locator('.control').click()
  await inst.locator('.sbox input').fill('gli-gli')
  await expect(inst.locator('.opt')).toHaveText(['gli-GLI-MYSQL', 'gli-GLI-ORACLE'])
  await inst.locator('.sbox input').press('Enter')
  await expect(inst.locator('.val')).toHaveText('gli-GLI-MYSQL')

  const db = page.getByTestId('ew-database')
  await db.locator('.control').click()
  await db.locator('.sbox input').fill('risk')
  await expect(db.locator('.opt')).toHaveText(['risk_control', 'risk_cron'])
  await db.locator('.opt', { hasText: 'risk_cron' }).click()
  // 可多选:默认勾上的第一个库还在,新勾的追加在后面
  await expect(db.getByTestId('vtag-chip')).toHaveText(['order_db', 'risk_cron'])
})

// 2026-10-22:一张窗口可以勾同一台实例上的多个库。提交带上全部库,列表里显示完整的库清单。
test('班车申请:勾选多个库一起提交,列表显示全部库', async ({ page }) => {
  const bodies: any[] = []
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...ME.menus, execwindow: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/exec-windows', (r) => {
    if (r.request().method() === 'POST') {
      const b = r.request().postDataJSON()
      bodies.push(b)
      const w = { id: 9, name: b.name, enabled: true, connectionId: b.connectionId, database: b.databases[0], databases: b.databases,
        kind: 'recurring', timezone: 'Asia/Shanghai', weekdays: '', startMin: 120, endMin: 240, reason: '', status: 'pending', apNo: 'AP-1', active: false, expired: false }
      saved.push(w)
      return r.fulfill(envelope(w))
    }
    return r.fulfill(envelope(saved))
  })
  await page.route('**/api/v1/connections/*/schema*', (r) =>
    r.fulfill(envelope({ databases: [{ name: 'gaussdb' }, { name: 'prod_c66_b' }, { name: 'postgres' }] })))
  await page.goto('/#/exec-windows')
  await page.getByRole('button', { name: '申请窗口' }).click()
  await page.getByPlaceholder('例如:凌晨发车').fill('dws-b')
  const db = page.getByTestId('ew-database')
  await expect(db.getByTestId('vtag-chip')).toHaveText(['gaussdb'])
  await db.locator('.control').click()
  await db.locator('.opt', { hasText: 'prod_c66_b' }).click()
  await expect(db.getByTestId('vtag-chip')).toHaveText(['gaussdb', 'prod_c66_b'])
  await page.locator('.vbtn', { hasText: '提交审批' }).click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].databases).toEqual(['gaussdb', 'prod_c66_b'])
  expect(bodies[0].database).toBe('gaussdb')
  await expect(page.getByTestId('ew-scope').first()).toContainText('gaussdb, prod_c66_b')
})

// issue #103:编辑一张旧窗口时,它的库已经不在实例的库列表里 —— 打开表单就被悄悄换成第一个库,
// 直接保存等于改了库。窗口按 (实例, 库) 精确匹配,换错了不会报错,只会开在别的库上。
const OLD = { id: 5, name: '旧班车', enabled: true, connectionId: 3, database: 'old_db', databases: ['old_db'], kind: 'recurring',
  timezone: 'Asia/Shanghai', weekdays: '', startMin: 120, endMin: 240, reason: 'r', status: 'approved', apNo: 'AP-5',
  active: false, expired: false, createdBy: 1 }

test('编辑旧窗口:不在列表里的库保留下来、说清楚,保存的还是它', async ({ page }) => {
  const bodies: any[] = []
  let probes = 0
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...ME.menus, execwindow: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/exec-windows', (r) => r.fulfill(envelope([OLD])))
  await page.route('**/api/v1/exec-windows/5', (r) => { bodies.push(r.request().postDataJSON()); return r.fulfill(envelope(OLD)) })
  await page.route('**/api/v1/connections/*/schema*', (r) => {
    probes++
    return r.fulfill(envelope({ databases: [{ name: 'a_db' }, { name: 'b_db' }] }))
  })
  await page.goto('/#/exec-windows')
  await page.getByTestId('ew-edit').click()
  const db = page.getByTestId('ew-database')
  await expect(db).toBeVisible()
  await page.waitForTimeout(300)
  await expect(db.getByTestId('vtag-chip')).toHaveText(['old_db'])
  await expect(page.locator('.dbwarn')).toContainText('old_db')
  expect(probes).toBe(1) // 打开一次只探查一次

  await page.locator('.vbtn', { hasText: '提交审批' }).click()
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].databases).toEqual(['old_db'])
})

test('连着换两次实例:先发后到的库列表不盖掉后选的那台', async ({ page }) => {
  let release = () => {}
  const gate = new Promise<void>((res) => { release = res })
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, menus: { ...ME.menus, execwindow: true } })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route('**/api/v1/exec-windows', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/connections/*/schema*', async (r) => {
    const id = /connections\/(\d+)\//.exec(r.request().url())![1]
    if (id === '3') await gate // GLI-MYSQL 慢
    return r.fulfill(envelope({ databases: [{ name: `db_of_${id}` }] }))
  })
  await page.goto('/#/exec-windows')
  await page.getByRole('button', { name: '申请窗口' }).click()
  const chips = page.getByTestId('ew-database').getByTestId('vtag-chip')
  await expect(chips).toHaveText(['db_of_1'])
  const pick = async (name: string) => {
    await page.getByTestId('ew-instance').locator('.control').click()
    await page.getByTestId('ew-instance').locator('.opt', { hasText: name }).click()
  }
  await pick('GLI-MYSQL')
  await pick('GLI-ORACLE')
  await expect(chips).toHaveText(['db_of_4'])
  const late = page.waitForResponse('**/connections/3/schema*')
  release()
  await late
  await page.waitForTimeout(200)
  await expect(chips).toHaveText(['db_of_4'])
})
