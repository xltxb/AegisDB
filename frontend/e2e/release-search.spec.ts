import { test, expect, type Page } from '@playwright/test'

// 变更发布页按单号 / 环境找单:都走后端(列表只取最新 50 张,前端过滤会漏),环境选项来自
// 接口而不是写死,和「我的 / 全部」、项目能叠加。

import { envelope, stubCommon, ME } from './helpers'

function release(id: number, env: string) {
  return {
    id, relNo: `REL-${id}`, title: `orderID=${16000 + id}-${env}`, pipelineId: 1, pipelineName: 'fat',
    connectionId: 7, instance: `${env}-mysql`, database: 'yone', env, tierCode: 'dev',
    engine: 'mysql', changeType: 'ddl', sql: 'alter table t add c int;', reason: '',
    creatorId: 1, creator: 'Lin Wei', source: 'api', status: 'success', risk: 'low', error: '',
    externalRef: `autopub:${16000 + id}:${env}:1:1`,
    createdAt: '2026-11-06T10:16:25+08:00', startedAt: null, finishedAt: null,
    stages: [{ id: id * 10, releaseId: id, stepOrder: 1, name: '规范审查', type: 'review', status: 'success',
      log: `详情日志-${id}`, config: '', onFailure: 'stop' }],
  }
}

const ALL = [release(3, 'fat'), release(2, 'uat'), release(1, 'fat')]

type Stub = { urls: URLSearchParams[]; hold?: (q: URLSearchParams) => Promise<void> | undefined }

// 假后端按真后端的约定回:q 比工单号或 REL 编号,envs 不随筛选收窄。
async function openReleases(page: Page, stub: Stub) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, canSeeAllReleases: true })))
  await page.route('**/api/v1/releases?*', async (r) => {
    const sp = new URL(r.request().url()).searchParams
    stub.urls.push(sp)
    await stub.hold?.(sp)
    const q = (sp.get('q') || '').toLowerCase()
    const env = sp.get('env') || ''
    const items = ALL.filter((x) =>
      (!env || x.env === env) && (!q || x.relNo.toLowerCase() === q || x.externalRef.includes(`:${q}:`)))
    await r.fulfill(envelope({ items, total: items.length, page: 1, pageSize: 50, envs: ['fat', 'uat'] }))
  })
  await page.route(/\/api\/v1\/releases\/\d+$/, (r) => {
    const id = Number(new URL(r.request().url()).pathname.split('/').pop())
    return r.fulfill(envelope(release(id, ALL.find((x) => x.id === id)?.env || 'fat')))
  })
  await page.goto('/#/releases')
  await page.locator('.ritem').first().waitFor({ state: 'visible', timeout: 15000 })
}

const search = (page: Page) => page.locator('.rsinput')
const envSel = (page: Page) => page.locator('select.rlenv')
const relNos = (page: Page) => page.locator('.ritem .rno')

test.describe('变更发布 · 按单号 / 环境搜索', () => {
  test('单号打完自动查,参数带给后端;清除后回到全部', async ({ page }) => {
    const stub: Stub = { urls: [] }
    await openReleases(page, stub)
    expect(stub.urls[0].has('q')).toBe(false) // 没搜的时候请求和原来一样
    expect(stub.urls[0].has('env')).toBe(false)

    await search(page).fill('16002')
    await expect(relNos(page)).toHaveText(['REL-2'])
    expect(stub.urls.at(-1)!.get('q')).toBe('16002')

    await page.locator('.rsclear').click()
    await expect(relNos(page)).toHaveCount(3)
    expect(stub.urls.at(-1)!.has('q')).toBe(false)
  })

  test('回车立刻查,不再等防抖,也不多发一次', async ({ page }) => {
    const stub: Stub = { urls: [] }
    await openReleases(page, stub)
    const before = stub.urls.length
    // 输入和回车放在同一个任务里:分两步走的话,机器一忙两步之间就可能过了防抖窗口
    await search(page).evaluate((el: HTMLInputElement) => {
      el.value = 'REL-1'
      el.dispatchEvent(new Event('input', { bubbles: true }))
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    await expect(relNos(page)).toHaveText(['REL-1'])
    await page.waitForTimeout(500) // 过了防抖窗口
    expect(stub.urls.length).toBe(before + 1)
  })

  test('环境选项来自接口,筛选和单号、项目、范围叠加', async ({ page }) => {
    const stub: Stub = { urls: [] }
    await openReleases(page, stub)
    await expect(envSel(page).locator('option')).toHaveText(['全部环境', 'fat', 'uat'])

    await envSel(page).selectOption('uat')
    await expect(relNos(page)).toHaveText(['REL-2'])
    // 选了环境,选项不收窄
    await expect(envSel(page).locator('option')).toHaveText(['全部环境', 'fat', 'uat'])

    await envSel(page).selectOption('fat')
    await search(page).fill('16003')
    await expect(relNos(page)).toHaveText(['REL-3'])
    let last = stub.urls.at(-1)!
    expect(last.get('env')).toBe('fat')
    expect(last.get('q')).toBe('16003')

    // 切范围不丢筛选
    await page.locator('.seg .si', { hasText: '我的' }).click()
    await expect.poll(() => stub.urls.at(-1)!.get('scope')).toBe('mine')
    last = stub.urls.at(-1)!
    expect(last.get('env')).toBe('fat')
    expect(last.get('q')).toBe('16003')
  })

  test('摊开的单被筛掉时换成结果第一张并取详情;没有结果时显示筛选的空态', async ({ page }) => {
    await openReleases(page, { urls: [] })
    await page.locator('.ritem', { hasText: 'REL-3' }).click()
    await expect(page.locator('.detail .dt')).toContainText('REL-3')

    await envSel(page).selectOption('uat')
    await expect(page.locator('.detail .dt')).toContainText('REL-2')
    await expect(page.locator('.ritem.on')).toContainText('REL-2')
    await page.locator('.node').first().click()
    await expect(page.locator('.sbox')).toContainText('详情日志-2') // 是详情,不是不带日志的列表行

    await search(page).fill('no-such')
    await expect(page.locator('.list .empty')).toHaveText('没有符合条件的发布单')
    await expect(page.locator('.detail.empty')).toBeVisible()
    await expect(page.locator('.list .empty')).not.toHaveText('暂无发布单')

    // 清掉筛选:回来,详情也回来
    await page.locator('.rsclear').click()
    await envSel(page).selectOption('')
    await expect(relNos(page)).toHaveCount(3)
    await expect(page.locator('.detail .dt')).toBeVisible()
  })

  test('没有筛选时的空列表仍是原来的空态', async ({ page }) => {
    await stubCommon(page)
    await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 50 })))
    await page.goto('/#/releases')
    await expect(page.locator('.list .empty')).toHaveText('暂无发布单')
    await expect(envSel(page).locator('option')).toHaveText(['全部环境'])
  })

  test('窄屏时工具条换行,不把页头说明挤成一字一行,也不横向滚动', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 800 })
    await openReleases(page, { urls: [] })
    const head = await page.locator('.v-head-l').boundingBox()
    expect(head!.width).toBeGreaterThan(200)
    expect(head!.height).toBeLessThan(80)
    const [sw, iw] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth])
    expect(sw).toBeLessThanOrEqual(iw)
  })

  test('慢的旧响应不能盖掉新结果', async ({ page }) => {
    let release_!: () => void
    const gate = new Promise<void>((res) => { release_ = res })
    const stub: Stub = { urls: [], hold: (sp) => (sp.get('q') === '16001' ? gate : undefined) }
    await openReleases(page, stub)

    await search(page).fill('16001')
    await search(page).press('Enter') // 这一个挂住
    await expect.poll(() => stub.urls.at(-1)!.get('q')).toBe('16001')
    await search(page).fill('16002')
    await search(page).press('Enter')
    await expect(relNos(page)).toHaveText(['REL-2'])

    release_()
    await page.waitForTimeout(200)
    await expect(relNos(page)).toHaveText(['REL-2'])
  })
})
