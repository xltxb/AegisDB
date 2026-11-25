import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 对象树上右键下载 Oracle 程序包。钉住:
//   · 右键一个包 → 「下载程序包 (.pck)」,请求只带这一个名字,文件名取服务端给的
//   · Ctrl / ⌘ 单击多选后右键 → 「下载所选 N 个」,names 按树上的顺序,拿到一个 zip
//   · 服务端拒绝时说出原话,不落任何文件
//   · 单击仍然是查看源码(原有行为不变),Esc 关菜单

const CONN = {
  id: 1, name: 'ora-app', engine: 'Oracle', env: 'prod', tierCode: 'prod', status: 'online',
  host: 'ora.internal', port: 1521, database: 'ORCL', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const PKGS = ['PKG_ORDERS', 'PKG_BILLING', 'PKG_USERS']
const PCK = 'CREATE OR REPLACE PACKAGE PKG_ORDERS IS\nEND PKG_ORDERS;\n/\n'

async function openTree(page: Page, reply?: 'fail') {
  const bodies: any[] = []
  const sources: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'prod', displayName: 'PROD', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'prod', displayName: 'PROD', tierCode: 'prod', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'APP', tables: [{ name: 'ORDERS' }] }] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects'), (r) =>
    r.fulfill(envelope({ functions: [], procedures: [], packages: PKGS, triggers: [] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects/invalid'), (r) =>
    r.fulfill(envelope({ items: [], total: 0 })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/object-source'), (r) => {
    sources.push(new URL(r.request().url()).searchParams.get('name') || '')
    return r.fulfill(envelope({ name: 'PKG_ORDERS', type: 'package', source: PCK }))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/packages/download'), (r) => {
    const body = r.request().postDataJSON()
    bodies.push(body)
    if (reply === 'fail') {
      return r.fulfill({ status: 200, contentType: 'application/json',
        body: JSON.stringify({ code: 40001, msg: '程序包 PKG_ORDERS: 无法读取对象源码: 对象不存在', data: null }) })
    }
    if (body.names.length === 1) {
      return r.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', body: PCK,
        headers: { 'Content-Disposition': `attachment; filename*=UTF-8''${body.names[0]}.pck` } })
    }
    return r.fulfill({ status: 200, contentType: 'application/zip', body: Buffer.from('PK\x05\x06' + '\0'.repeat(18), 'binary'),
      headers: { 'Content-Disposition': "attachment; filename*=UTF-8''APP_packages_20260924_101010.zip" } })
  })
  await gotoTerminal(page)
  await page.locator('.inst.active', { hasText: 'ora-app' }).waitFor()
  await page.locator('.db', { hasText: 'APP' }).click()
  await page.locator('.cat', { hasText: '包' }).click()
  await expect(page.getByTestId('pkg-row')).toHaveCount(3)
  return { bodies, sources }
}

const row = (page: Page, name: string) => page.getByTestId('pkg-row').filter({ hasText: name })

test.describe('Oracle 程序包 · 右键下载', () => {
  test('右键一个包:下载 .pck,只带这一个名字', async ({ page }) => {
    const { bodies } = await openTree(page)
    await row(page, 'PKG_BILLING').click({ button: 'right' })
    const menu = page.getByTestId('pkg-menu')
    await expect(menu).toBeVisible()
    await expect(page.getByTestId('pkg-download')).toHaveText(/下载程序包 \(\.pck\)/)
    const dl = page.waitForEvent('download')
    await page.getByTestId('pkg-download').click()
    expect((await dl).suggestedFilename()).toBe('PKG_BILLING.pck')
    expect(bodies).toEqual([{ scope: 'APP', database: '', names: ['PKG_BILLING'] }])
    await expect(menu).toHaveCount(0)
  })

  // macOS 上是 ⌘ 单击:系统把 Ctrl+单击当右键,弹出来的是菜单(issue #105)。
  test('Ctrl / ⌘ 多选后右键:一个 zip,names 按树上的顺序', async ({ page }) => {
    const { bodies, sources } = await openTree(page)
    await row(page, 'PKG_USERS').click({ modifiers: ['ControlOrMeta'] })
    await row(page, 'PKG_ORDERS').click({ modifiers: ['ControlOrMeta'] })
    expect(sources).toEqual([]) // 多选不打开源码
    await row(page, 'PKG_USERS').click({ button: 'right' })
    await expect(page.getByTestId('pkg-download')).toHaveText(/下载所选 2 个程序包/)
    const dl = page.waitForEvent('download')
    await page.getByTestId('pkg-download').click()
    expect((await dl).suggestedFilename()).toBe('APP_packages_20260924_101010.zip')
    expect(bodies[0].names).toEqual(['PKG_ORDERS', 'PKG_USERS'])
  })

  test('右键「全选」后下载整组', async ({ page }) => {
    const { bodies } = await openTree(page)
    await row(page, 'PKG_ORDERS').click({ button: 'right' })
    await page.getByTestId('pkg-select-all').click()
    await row(page, 'PKG_ORDERS').click({ button: 'right' })
    await expect(page.getByTestId('pkg-download')).toHaveText(/下载所选 3 个程序包/)
    const dl = page.waitForEvent('download')
    await page.getByTestId('pkg-download').click()
    await dl
    expect(bodies[0].names).toEqual(PKGS)
  })

  test('服务端拒绝:提示原话,不落文件', async ({ page }) => {
    const { bodies } = await openTree(page, 'fail')
    let downloaded = false
    page.on('download', () => { downloaded = true })
    await row(page, 'PKG_ORDERS').click({ button: 'right' })
    await page.getByTestId('pkg-download').click()
    await expect(page.getByText('对象不存在')).toBeVisible()
    expect(bodies.length).toBe(1)
    expect(downloaded).toBe(false)
  })

  test('单击仍是查看源码;Esc 关菜单', async ({ page }) => {
    const { sources } = await openTree(page)
    await row(page, 'PKG_BILLING').click({ button: 'right' })
    await expect(page.getByTestId('pkg-menu')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('pkg-menu')).toHaveCount(0)
    expect(sources).toEqual([]) // 右键不打开源码
    await row(page, 'PKG_ORDERS').click()
    await expect.poll(() => sources).toEqual(['PKG_ORDERS'])
  })
})
