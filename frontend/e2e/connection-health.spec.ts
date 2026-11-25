import { test, expect, type Page } from '@playwright/test'

// 数据源页的「异常」到底指什么。
//
// 这组用例是被一次线上现象换来的:
//
//   「状态 → 仅看异常」什么都没有,而实际上有两台新加的库根本连不上,它们的状态
//    显示是「在线」。
//
// 成因:status 只有 online / maint 两个值,是**人为标的维护态**,从不表示"连得上";
// 而「仅看异常」筛的就是 status。所以那个筛选器永远筛不出连不上的实例 —— 它其实是
// 「仅看维护态」,只是名字不叫这个。
//
// 三条各挡一面:
//
//   1. 连不上的实例要能被「仅看异常」筛出来;
//   2. 分组标题上的异常计数要算上它们 —— 否则标题说"全部正常",下面挂着两台连不上的;
//   3. 从没巡检过 ≠ 正常。它不算异常(那是"不知道",不是"坏了"),但也不能显示成正常。

import { envelope, stubCommon } from './helpers'

function conn(id: number, name: string, extra: Record<string, unknown>) {
  return {
    id, name, engine: 'mysql', env: 'prod', host: '10.0.0.' + id, port: 3306,
    policy: 'strict', defaultRole: 'ro', layer: 'core', tags: '', database: 'appdb',
    status: 'online', ...extra,
  }
}

// 三台:一台健康(巡检通过)、一台连不上(状态仍是在线,正是线上那两台的样子)、
// 一台从没巡检过。
const CONNS = [
  conn(1, 'healthy-one', { lastCheckOk: true, lastCheckAt: new Date(Date.now() - 3600_000).toISOString() }),
  conn(2, 'unreachable-one', { lastCheckOk: false, lastCheckAt: new Date(Date.now() - 7200_000).toISOString(), lastCheckErr: 'dial tcp 10.0.0.2:3306: i/o timeout' }),
  conn(3, 'never-checked-one', {}),
]

async function openConnections(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.goto('/#/connections')
  await page.locator('.opscell').first().waitFor({ state: 'visible', timeout: 15000 })
}

test.describe('数据源 · 什么叫异常', () => {
  test('连不上的实例在行上就看得见,而且带着"多久以前"', async ({ page }) => {
    await openConnections(page)

    const bad = page.locator('.ck.bad')
    await expect(bad).toHaveCount(1)
    await expect(bad).toContainText('连不上')
    // 一个不说年龄的结论会被当成此刻的事实。
    await expect(bad).toContainText('小时前')
    // 原因留在 title 上:i/o timeout 和 Access denied 的处置动作完全不同。
    await expect(bad).toHaveAttribute('title', /i\/o timeout/)
  })

  test('「仅看异常」筛得出连不上的实例 —— 这正是原来筛不出来的那一条', async ({ page }) => {
    await openConnections(page)

    await page.locator('select.fsel').first().selectOption('bad')

    // 自动重试的断言,不用固定等待。
    const body = page.locator('.page')
    await expect(body).toContainText('unreachable-one')
    // 健康的和没巡检过的都不该出现:前者正常,后者是"不知道"而不是"坏了"。
    await expect(body).not.toContainText('healthy-one')
    await expect(body).not.toContainText('never-checked-one')
  })

  test('分组标题上的异常计数要算上连不上的,否则标题会说"全部正常"', async ({ page }) => {
    await openConnections(page)

    // 三台在同一个环境下:2 正常 / 1 异常 —— 两个数都要对。审查时抓到过 ok 与 bad
    // 用两套定义、在线但连不上的被数两次(显示 3 好 1 坏)。
    const health = page.locator('.ghealth').first()
    await expect(health).toHaveClass(/bad/)
    await expect(health).toHaveText(/2\s*1/)
  })

  test('从没巡检过不显示成正常 —— 悬停要说清是"不知道"', async ({ page }) => {
    await openConnections(page)

    const text = await page.locator('.page').innerText()
    expect(text).toContain('never-checked-one')
    // 它没有连不上的标记(不算异常),但**药丸本身**的说明要说清它从没被探过 ——
    // 说明挂在外层 div 上会被药丸自己的 title 盖住,人悬停上去什么都看不到。
    const pill = page.locator('.st[title*="从未巡检"]')
    await expect(pill).toHaveCount(1)
  })
})
