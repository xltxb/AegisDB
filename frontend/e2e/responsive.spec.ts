import { test, expect, type Page } from '@playwright/test'
import { ADMIN_ROUTES, OPS_ROUTES, envelope, seedSession, stubShell } from './fixtures'

// 这一口盯的是排版算完之后才成立的事。改之前全站没有页面级横向溢出,
// 问题是**挤烂**:768 下数据源页的地址列折成六行、表头断成两截;390 下外壳
// 自身溢出、两处标签竖成一列单字。类型检查、构建、单测都看不见这些。

const WIDTHS = [1440, 1080, 768]

async function stubAll(page: Page) {
  await seedSession(page)
  // 兜底**先注册**:Playwright 后注册者优先,所以随后的 stubShell 才盖得住它。
  // 反过来写的话兜底会吃掉 /auth/me,整个会话拿不到身份。
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page)
}

/** 页面级横向溢出。>1px 才算,躲开亚像素舍入。 */
async function overflow(page: Page, vw: number) {
  return page.evaluate((w) => Math.max(
    document.documentElement.scrollWidth, document.body.scrollWidth) - w, vw)
}

for (const [portal, routes] of [['运维前台', OPS_ROUTES], ['管理后台', ADMIN_ROUTES]] as const) {
  test.describe(`${portal} · 三档宽度下不溢出`, () => {
    for (const w of WIDTHS) {
      test(`${w}px`, async ({ page }) => {
        await stubAll(page)
        await page.goto('/login')
        await page.click(`button.portal-opt:has-text("${portal}")`)
        await page.fill('input[type="email"]', 'linwei@vela.io')
        await page.fill('input[type="password"]', 'vela123')
        await page.click('button.login-submit')
        await page.waitForTimeout(1500)

        await page.setViewportSize({ width: w, height: 900 })
        for (const route of routes) {
          await page.goto(`/${route}`)
          await page.waitForTimeout(300)
          expect(await overflow(page, w), `${route} 在 ${w}px 下横向溢出`).toBeLessThanOrEqual(1)
        }
      })
    }
  })
}

// 标题被挤成竖排单字时,它的高度会是行高的好几倍而宽度只有一两个字符。
// 这正是 390 下「数据库管理网关」与「默认策略」的样子。
test('窄屏下标题不被挤成竖排单字', async ({ page }) => {
  await stubAll(page)
  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto('/settings')
  await expect(page.locator('.top-title')).toBeVisible()

  const ratio = await page.locator('.top-title').evaluate((el) => {
    const r = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    // .top-title 没有显式设置 line-height,Chromium 在 computed style 里把它保留成
    // 字面量 "normal" 而不是解析出的像素值 —— parseFloat('normal') 是 NaN,原先的
    // `|| '20'` 兜底救不了它(空串才会触发,"normal" 是非空字符串)。用字号 * 1.2
    // (CSS 对 normal 行高的惯例换算)顶上,让比值仍然反映"是不是被撑成了好几行"。
    const lineHeight = parseFloat(cs.lineHeight)
    return r.height / (Number.isFinite(lineHeight) ? lineHeight : parseFloat(cs.fontSize) * 1.2)
  })
  expect(ratio, '顶栏标题折成了多行 —— 它应当单行省略').toBeLessThan(1.6)
})

// 能力矩阵的横滚是**有意设计**(见 CapabilityMatrix.tsx 的注释)。
// 把它写成断言,是为了挡住下一个人在做自适应时「顺手把它修掉」。
test('能力矩阵在 768 下仍然横滚,不被这轮改动顺手修掉', async ({ page }) => {
  await stubAll(page)
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(
    ['prod', 'gli', 'staging', 'uat', 'dev'].map((code, i) => ({ code, displayName: code.toUpperCase(), sortOrder: i })))))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([{ id: 1, code: 'admin', name: 'Admin', layer: 'L0', icon: 'crown', members: 1 }])))
  await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope(
    { id: 1, code: 'admin', name: 'Admin', layer: 'L0', icon: 'crown', menus: {}, matrix: {}, members: [], memberIds: [], tags: [] })))

  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  await page.setViewportSize({ width: 768, height: 900 })
  await page.goto('/permissions')
  await expect(page.locator('.cap-scroll')).toBeVisible()

  const scrolls = await page.locator('.cap-scroll').evaluate((el) => el.scrollWidth > el.clientWidth + 4)
  expect(scrolls, '能力矩阵不再横滚 —— 它是有意设计,不该被自适应改动收掉').toBe(true)
})

// 轨道数与单元格数脱节 = 整张表错位一列。这件事只有布局算完之后才看得出来,
// 所以它在这里,不在单测里 —— 单测那层拿不到「实际渲染了几个单元格」。
test('每张表的 grid 轨道数都等于它每行的单元格数', async ({ page }) => {
  await stubAll(page)
  await page.goto('/login')
  await page.click('button.portal-opt:has-text("管理后台")')
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForTimeout(1500)

  for (const w of WIDTHS) {
    await page.setViewportSize({ width: w, height: 900 })
    for (const route of ADMIN_ROUTES) {
      await page.goto(`/${route}`)
      await page.waitForTimeout(300)
      const bad = await page.evaluate(() => {
        const out: string[] = []
        for (const head of document.querySelectorAll('.c-thead')) {
          // computed value 是解析后的像素轨道列表,minmax() 已经被算成一个值,
          // 所以数它比数模板字符串里的空格可靠。
          const tracks = getComputedStyle(head).gridTemplateColumns.split(' ').length
          if (head.children.length !== tracks) {
            out.push(`表头 ${tracks} 轨道 vs ${head.children.length} 单元格`)
          }
          const row = head.parentElement?.querySelector('.c-trow')
          if (row && row.children.length !== tracks) {
            out.push(`数据行 ${row.children.length} 单元格 vs ${tracks} 轨道`)
          }
        }
        return out
      })
      expect(bad, `${route} 在 ${w}px 下轨道与单元格对不上`).toEqual([])
    }
  }
})
