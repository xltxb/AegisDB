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

import { envelope, failure, stubCommon as stubShell } from './helpers'

const CONNS = [
  { id: 7, name: 'tongcha', env: 'prod', engine: 'mysql', host: '10.0.0.1', port: 3306, policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb', status: 'online' },
]

// 词条值是 JSON 串 —— 设置页读的时候会 JSON.parse(见 SettingsView 的 parse)。
const SETTINGS = { settings: { 'meta.sync.enabled': 'false', 'meta.sync.intervalHours': '24', 'meta.sync.concurrency': '2' }, secretsSet: {} }

async function stubCommon(page: Page) {
  await stubShell(page)
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

// 同步在服务端后台跑:POST ...?async=1 立刻回 running,界面再轮询 GET 同一路径。
// stubSync 把两条都接上:POST 回 running;GET 前 pending 次回 running,之后回 final。
function stubSync(page: Page, final: Record<string, unknown> | null, opts: { pending?: number; startFail?: string } = {}) {
  const hits: { method: string; url: string }[] = []
  let polls = 0
  return page.route('**/api/v1/connections/*/metadata/sync*', (r) => {
    const req = r.request()
    hits.push({ method: req.method(), url: new URL(req.url()).pathname + new URL(req.url()).search })
    if (req.method() === 'POST') {
      if (opts.startFail) return r.fulfill(failure(opts.startFail))
      return r.fulfill(envelope({ running: true, sync: null }))
    }
    polls++
    if (polls <= (opts.pending ?? 0)) return r.fulfill(envelope({ running: true, sync: null }))
    return r.fulfill(envelope({ running: false, ...final }))
  }).then(() => hits)
}

test.describe('数据源 · 立即同步', () => {
  test('按钮在那台实例上发起后台同步,轮询到跑完,并把同步到多少张表说出来', async ({ page }) => {
    await openConnections(page)
    const hits = await stubSync(page, {
      sync: { connectionId: 7, startedAt: '', finishedAt: '', databases: 1, tables: 42, columns: 300 },
    }, { pending: 1 })

    await syncBtn(page).click()

    // 跑着的时候行上说"同步中",按钮不能再点
    await expect(page.locator('.opscell .ck.run')).toContainText('同步中')
    await expect(syncBtn(page)).toBeDisabled()

    await expect(page.locator('.opscell .ck.ok')).toContainText('42')
    await expect(syncBtn(page)).toBeEnabled()
    // 打到的是这一行那台实例,不是别的哪一台 —— 它会真的登录过去。
    expect(hits[0]).toEqual({ method: 'POST', url: '/api/v1/connections/7/metadata/sync?async=1' })
    expect(hits.slice(1).every((h) => h.method === 'GET' && h.url === '/api/v1/connections/7/metadata/sync')).toBe(true)
    expect(hits.length).toBeGreaterThanOrEqual(3)
  })

  // 生产上真的发生过的那一类:整台实例同步成功了,但其中几个库没读到。
  // 它既不是成功也不是失败 —— 只有成/败两格时它会被塞进"成功",而少收一个库的表现
  // 是"那个库一张表都没有",和"那个库真的是空的"长得一模一样。
  test('收下了但不完整时,标记是告警而不是成功,原因照原样说出来', async ({ page }) => {
    await openConnections(page)
    await stubSync(page, {
      sync: {
        connectionId: 7, startedAt: '', finishedAt: '',
        databases: 3, tables: 40, columns: 900,
        err: '2 个库没能读取:billing, archive',
      },
    })

    await syncBtn(page).click()

    await expect(page.locator('.opscell .ck.warn')).toContainText('40')
    await expect(page.locator('.opscell .ck.ok')).toHaveCount(0)
    // 哪几个库没读到,是这里唯一可据以行动的信息 —— 不能被压成一句"部分失败"。
    await expect(page.locator('body')).toContainText('billing, archive')
  })

  test('发起时就被拒(未配凭据):显示的是服务端说的那句话,而不是一句"同步失败"', async ({ page }) => {
    await openConnections(page)
    await stubSync(page, null, { startFail: '该实例未配置真实执行凭据,无法探查元数据' })

    await syncBtn(page).click()

    // 行上的标记只说成败(位置就那么点大),完整原因走提示条 —— 那三种失败的
    // 处置方式完全不同,压成一句话等于什么都没说。
    await expect(page.locator('.opscell .ck.bad')).toBeVisible()
    await expect(page.locator('body')).toContainText('未配置真实执行凭据')
  })

  test('后台跑完但失败(连不上):标记失败,原因照原样说出来', async ({ page }) => {
    await openConnections(page)
    await stubSync(page, { failed: 'dial tcp 10.0.0.1:3306: connect: connection refused', sync: null })

    await syncBtn(page).click()

    await expect(page.locator('.opscell .ck.bad')).toBeVisible()
    await expect(page.locator('.opscell .ck.ok')).toHaveCount(0)
    await expect(page.locator('body')).toContainText('connection refused')
  })
})
