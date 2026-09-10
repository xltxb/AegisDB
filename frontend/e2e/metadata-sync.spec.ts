import { test, expect, type Page } from '@playwright/test'

// 元数据同步的两个入口:设置页的定时开关,和数据源页每行的「立即同步」。
//
// 放在浏览器里验,是因为要挡的三件事都只在真的点下去时才成立:
//
//   1. **夹取**。间隔和并发在界面上就夹到服务端接受的范围里(1-720 / 1-8)。填 5000
//      必须变成 720 —— 否则人以为自己设了 5000,而服务端悄悄按 720 跑,两边说的不是
//      同一件事。
//   2. **按钮真的发出了那个请求**。这个按钮会**登录目标库**,所以"点了没反应"和
//      "点了但打错了地址"必须分得开。
//   3. **失败要说清是哪一种失败**。连不上 / 没权限 / 未配凭据,处置方式完全不同;
//      压成一句"同步失败"就等于什么都没说。
//
// 开关本身也在这里定住:它默认必须是**关**的。打开它意味着这台网关会周期性地登录每
// 一台实例 —— 一个默认开着的这种开关,是没人做过决定就开始扫生产。

const envelope = (data: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, msg: 'ok', data }) })
const failure = (msg: string) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 1001, msg, data: null }) })

const ME = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, mfaEnabled: false,
  menus: { terminal: true, export: true, approve: true, db: true, rules: true, envtier: true, perms: true, audit: true, settings: true },
  capabilities: {},
}

const CONNS = [
  { id: 7, name: 'tongcha', env: 'prod', engine: 'mysql', host: '10.0.0.1', port: 3306, policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online' },
]

const TIERS = [{ code: 'prod', displayName: '生产环境 · PROD', sortOrder: 0, requireMfa: true, dangerBanner: true, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }]
const ENVS = [{ code: 'prod', displayName: '生产环境 · PROD', tierCode: 'prod', sortOrder: 0 }]

// 词条值是 JSON 串 —— 设置页读的时候会 JSON.parse(见 SettingsView 的 parse)。
const SETTINGS = { settings: { 'meta.sync.enabled': 'false', 'meta.sync.intervalHours': '24', 'meta.sync.concurrency': '2' }, secretsSet: {} }

async function stubCommon(page: Page) {
  await page.addInitScript(() => localStorage.setItem('vela_token', 'e2e-token'))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(TIERS)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(ENVS)))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/gateway/stats', (r) => r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))
  await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [] })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
}

// ---------------------------------------------------------------- 设置页

async function openMetaTab(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope(SETTINGS))
    return r.fulfill(envelope({}))
  })
  await page.goto('/#/settings')
  await page.locator('.tabs .tab', { hasText: '元数据同步' }).click()
  await expect(metaCard(page)).toBeVisible()
}

// 收窄到元数据那一张卡:设置页有八张 .card(每个分页一张,靠 v-show 藏,DOM 里都在),
// 直接用 .card 会撞上 strict mode。
const metaCard = (page: Page) => page.locator('.card').filter({ has: page.locator('.st', { hasText: '元数据同步' }) })
const metaRows = (page: Page) => metaCard(page).locator('.srow')

test.describe('设置 · 元数据定时同步', () => {
  test('关着的时候不摆出可调参数,三句提醒都在', async ({ page }) => {
    await openMetaTab(page)

    // 开关渲染成关 —— 桩数据给的是 false,这里验的是界面照它显示,
    // 不是验服务端的默认值(那是 service/metadata.go 的事)。
    await expect(metaRows(page).filter({ hasText: '定时同步' }).locator('.vsw.on')).toHaveCount(0)

    // 开关关着:间隔/并发两行根本不渲染 —— 一个关着的功能不该摆出可调的参数,
    // 那会让人以为它在按这个参数跑。
    await expect(metaRows(page).filter({ hasText: '同步间隔' })).toHaveCount(0)
    await expect(metaRows(page).filter({ hasText: '并发实例数' })).toHaveCount(0)

    // 三句提醒必须在。第一句是最要紧的:开完不会立刻跑。
    await expect(metaCard(page)).toContainText('开启后不会立刻开跑')
    await expect(metaCard(page)).toContainText('会被跳过')
    await expect(metaCard(page)).toContainText('只读的副本')
  })

  test('打开后才出现间隔与并发,并且填过界的值会被夹回服务端接受的范围', async ({ page }) => {
    await openMetaTab(page)

    const saved: Record<string, unknown>[] = []
    await page.route('**/api/v1/settings', (r) => {
      if (r.request().method() === 'GET') return r.fulfill(envelope(SETTINGS))
      saved.push(r.request().postDataJSON())
      return r.fulfill(envelope({}))
    })

    await metaRows(page).filter({ hasText: '定时同步' }).locator('.vsw').click()
    const interval = metaRows(page).filter({ hasText: '同步间隔' }).locator('input')
    const conc = metaRows(page).filter({ hasText: '并发实例数' }).locator('input')
    await expect(interval).toBeVisible()
    await expect(conc).toBeVisible()

    // 故意填过界:5000 小时和 99 台并发。
    await interval.fill('5000')
    await conc.fill('99')
    await page.locator('button', { hasText: '保存' }).first().click()

    await expect.poll(() => saved.length).toBeGreaterThan(0)
    const body = saved[saved.length - 1]
    expect(body['meta.sync.enabled']).toBe(true)          // 布尔,不是字符串 'true'
    expect(body['meta.sync.intervalHours']).toBe(720)     // 夹到 30 天
    expect(body['meta.sync.concurrency']).toBe(8)         // 夹到 8
  })
})

// ---------------------------------------------------------------- 数据源页

async function openConnections(page: Page) {
  await stubCommon(page)
  await page.goto('/#/connections')
  await expect(page.locator('.opscell').first()).toBeVisible()
}

const syncBtn = (page: Page) => page.locator('.opscell button[title="立即同步元数据"]').first()

test.describe('数据源 · 立即同步', () => {
  test('按钮真的打到那台实例的同步接口上,并把同步到多少张表说出来', async ({ page }) => {
    await openConnections(page)

    const hits: string[] = []
    await page.route('**/api/v1/connections/*/metadata/sync', (r) => {
      hits.push(new URL(r.request().url()).pathname)
      return r.fulfill(envelope({ tables: 42, sync: { connectionId: 7, startedAt: '', finishedAt: '', databases: 1, tables: 42, columns: 300 } }))
    })

    await syncBtn(page).click()

    // 打到的是这一行那台实例,不是别的哪一台 —— 它会真的登录过去。
    await expect.poll(() => hits).toEqual(['/api/v1/connections/7/metadata/sync'])
    await expect(page.locator('.opscell .ck.ok')).toContainText('42')
  })

  test('失败时显示的是服务端说的那句话,而不是一句"同步失败"', async ({ page }) => {
    await openConnections(page)
    await page.route('**/api/v1/connections/*/metadata/sync', (r) =>
      r.fulfill(failure('该实例未配置真实执行凭据,无法探查元数据')))

    await syncBtn(page).click()

    // 行上的标记只说成败(位置就那么点大),完整原因走提示条 —— 那三种失败的
    // 处置方式完全不同,压成一句话等于什么都没说。
    await expect(page.locator('.opscell .ck.bad')).toBeVisible()
    await expect(page.locator('body')).toContainText('未配置真实执行凭据')
  })
})
