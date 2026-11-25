import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal, autoConfirm } from './helpers'

// Oracle 无效对象:批量(勾选)与分层编译在界面上的样子。钉三件事:
//
//   1. 一个都没勾 = 全部重编译,请求里不带 names;勾了几个 = 只编那几个,names 就是那几个。
//      后端早就分这两种,界面上原来只有"全部"。
//   2. 结果里要说清**这次是怎么编的**:分了几层、跑了几轮、有几个在依赖环里 —— 一个
//      "仍失败 1"不带这些,人分不清是"顺序没排好"还是"真编不过"。
//   3. 每一行带自己的层号;环里的对象标出来。

const CONN = {
  id: 1, name: 'ora-app', engine: 'Oracle', env: 'prod', tierCode: 'prod', status: 'online',
  host: 'ora.internal', port: 1521, database: 'ORCL', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const INVALID = [
  { owner: 'APP', name: 'PKG_ORDERS', kind: 'package', units: ['PACKAGE BODY'] },
  { owner: 'APP', name: 'V_ORDERS', kind: 'view', units: ['VIEW'] },
  { owner: 'APP', name: 'TRG_AUDIT', kind: 'trigger', units: ['TRIGGER'] },
  { owner: 'APP', name: 'PKG_BILLING', kind: 'package', units: ['PACKAGE', 'PACKAGE BODY'] },
]
const REPORT = {
  owner: 'APP', total: 4, fixed: 3, failed: 1, skipped: 0, passes: 2, levels: 3, cyclic: 2, ms: 812,
  items: [
    { owner: 'APP', name: 'V_ORDERS', kind: 'view', status: 'VALID', passes: 1, level: 1 },
    { owner: 'APP', name: 'PKG_ORDERS', kind: 'package', status: 'VALID', passes: 1, level: 2 },
    { owner: 'APP', name: 'TRG_AUDIT', kind: 'trigger', status: 'VALID', passes: 2, level: 3, cyclic: true },
    { owner: 'APP', name: 'PKG_BILLING', kind: 'package', status: 'INVALID', passes: 2, level: 3, cyclic: true,
      errors: [{ type: 'PACKAGE BODY', line: 12, position: 3, text: 'PLS-00201: identifier X must be declared' }] },
  ],
}

async function openTree(page: Page) {
  const bodies: any[] = []
  // 分层不要 MFA:要不然终端页一打开就是一层二次验证的遮罩,树点不到。这里测的是树。
  await stubCommon(page, {
    tiers: [{ code: 'prod', displayName: 'PROD', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'prod', displayName: 'PROD', tierCode: 'prod', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'APP', tables: [{ name: 'ORDERS' }] }] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects'), (r) =>
    r.fulfill(envelope({ functions: [], procedures: [], packages: ['PKG_ORDERS', 'PKG_BILLING'], triggers: ['TRG_AUDIT'] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects/invalid'), (r) =>
    r.fulfill(envelope({ items: INVALID, total: INVALID.length })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects/recompile'), (r) => {
    bodies.push(r.request().postDataJSON())
    return r.fulfill(envelope({ ok: false, report: REPORT }))
  })
  await autoConfirm(page)
  await gotoTerminal(page)
  // 终端页打开时自动选中第一台生产实例并展开 —— 再点一下反而是收起。
  await page.locator('.inst.active', { hasText: 'ora-app' }).waitFor()
  await page.locator('.db', { hasText: 'APP' }).click()
  await page.locator('.inv-row').waitFor()
  return bodies
}

test.describe('Oracle 无效对象 · 批量与分层编译', () => {
  test('没勾 = 全部;勾了 = 只编勾的,names 就是那几个', async ({ page }) => {
    const bodies = await openTree(page)
    await expect(page.locator('.inv-n')).toHaveText('4')
    const btn = page.getByTestId('inv-recompile')
    await expect(btn).toHaveText(/全部重编译/)

    await page.locator('.inv-obj', { hasText: 'PKG_ORDERS' }).locator('input').check()
    await page.locator('.inv-obj', { hasText: 'TRG_AUDIT' }).locator('input').check()
    await expect(btn).toHaveText(/编译选中 2/)
    await btn.click()
    await expect.poll(() => bodies.length).toBe(1)
    expect(bodies[0].names).toEqual(['PKG_ORDERS', 'TRG_AUDIT'])
    expect(bodies[0].scope).toBe('APP')

    // 关掉结果,再按一次"全部":这回不带 names。
    await page.locator('.rc-x').click()
    // 勾选在清单重取后按名字保留;这里把两个都取消,回到"全部"。
    await page.locator('.inv-obj', { hasText: 'PKG_ORDERS' }).locator('input').uncheck()
    await page.locator('.inv-obj', { hasText: 'TRG_AUDIT' }).locator('input').uncheck()
    await expect(btn).toHaveText(/全部重编译/)
    await btn.click()
    await expect.poll(() => bodies.length).toBe(2)
    expect(bodies[1].names).toBeUndefined()
  })

  test('结果说清分了几层、几轮、几个在环里;每行带层号', async ({ page }) => {
    await openTree(page)
    await page.getByTestId('inv-recompile').click()
    const modal = page.locator('.rc-modal')
    await modal.waitFor()
    await expect(modal.locator('.rc-sum')).toContainText('已修复 3')
    await expect(modal.locator('.rc-sum')).toContainText('仍失败 1')
    await expect(modal.locator('.rc-sum')).toContainText('3 层依赖')
    await expect(modal.locator('.rc-sum')).toContainText('2 轮')
    await expect(modal.locator('.rc-sum')).toContainText('2 个互相依赖')
    await expect(modal.locator('.rc-note')).toHaveCount(0)

    const rows = modal.locator('.rc-item')
    await expect(rows).toHaveCount(4)
    await expect(rows.nth(0).locator('.ri-lv')).toHaveText('L1')
    await expect(rows.nth(1).locator('.ri-lv')).toHaveText('L2')
    await expect(rows.nth(2).locator('.ri-lv')).toHaveText('L3 · 第 2 轮')
    await expect(rows.nth(2).locator('.ri-lv')).toHaveClass(/cyc/)
    await expect(rows.nth(3)).toContainText('PLS-00201')
  })

  test('依赖图没读到时明说:这次没有分层', async ({ page }) => {
    await openTree(page)
    await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects/recompile'), (r) =>
      r.fulfill(envelope({ ok: true, report: { ...REPORT, levels: 1, cyclic: 0, depsNote: 'ORA-00942: table or view does not exist' } })))
    await page.getByTestId('inv-recompile').click()
    const modal = page.locator('.rc-modal')
    await modal.waitFor()
    await expect(modal.locator('.rc-note')).toContainText('没有按依赖分层')
    await expect(modal.locator('.rc-note')).toContainText('ORA-00942')
  })
})
