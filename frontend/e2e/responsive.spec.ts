import { test, expect, type Page } from '@playwright/test'
import { ADMIN, ADMIN_ROUTES, OPS_ROUTES, envelope, seedSession, stubShell } from './fixtures'
import { installWsFake } from './wsFake'

// 这一口盯的是排版算完之后才成立的事。改之前全站没有页面级横向溢出,
// 问题是**挤烂**:768 下数据源页的地址列折成六行、表头断成两截;390 下外壳
// 自身溢出、两处标签竖成一列单字。类型检查、构建、单测都看不见这些。

const WIDTHS = [1440, 1080, 768]

async function stubAll(page: Page) {
  await seedSession(page)
  // 终端页也在这轮里被逐个打开。不顶掉那条 socket,Vite 会把它转发给并不存在的
  // Go 后端,于是每跑一次就往输出里刷一串 ECONNREFUSED —— 与本口无关的噪声,
  // 而它盖住的正是这一口自己的失败信息。
  await installWsFake(page)
  // 兜底**先注册**:Playwright 后注册者优先,所以随后的 stubShell 才盖得住它。
  // 反过来写的话兜底会吃掉 /auth/me,整个会话拿不到身份。
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  // /auth/login 不能落到兜底头上:LoginResp 要求 `{ token, user }`,兜底给的是空
  // 数组。`authStore.login()` 不校验形状,会直接把内存里的 token 覆盖成
  // `undefined`(`data.token` 在数组上取不到值)—— 这跟 localStorage 里残留的旧
  // token 字符串不是一回事,根路由的 loader 读的是内存里那份,一旦是
  // `undefined` 就会把人弹回 /login,SPA 内部的登录跳转永远走不到 /dashboard。
  await page.route('**/api/v1/auth/login', (r) => r.fulfill(envelope({ token: 'e2e-token', expiresAt: '2999-01-01T00:00:00Z', user: ADMIN })))
  await stubShell(page)
}

/**
 * 登录并选定门户,等到真正落地在 /dashboard 才返回。
 *
 * 两个门户共用同一个落地页 —— `/dashboard` 的守卫不设 `adminOnly`,对两侧都开放
 * (见 `router/index.tsx` / `router/guards.ts` 的 `rootLoader`:只要 `firstVisibleRoute`
 * 非空就统一送 `/dashboard`,门户只决定它是否非空,不决定具体路径)。所以这里按
 * `/dashboard` 等,而不是按门户分两个目标 —— 那样反而会在两侧之间编造出一个本不
 * 存在的差异。用 `waitForURL` 而不是固定的 `waitForTimeout`,登录成功与否不再靠猜时长。
 */
async function loginAs(page: Page, portal: '运维前台' | '管理后台') {
  await page.goto('/login')
  await page.click(`button.portal-opt:has-text("${portal}")`)
  await page.fill('input[type="email"]', 'linwei@vela.io')
  await page.fill('input[type="password"]', 'vela123')
  await page.click('button.login-submit')
  await page.waitForURL(/\/dashboard$/)
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
        await loginAs(page, portal)

        await page.setViewportSize({ width: w, height: 900 })
        for (const route of routes) {
          await page.goto(`/${route}`)
          // 门户选错不报错,只会被静默送去别处(简报重点三)。不校验落地 URL 的话,
          // 送去的空白页/别的页面同样测不出溢出 —— 断言会无条件通过,变成假保证。
          await expect(page, `${route} 被静默送去了别处 —— 多半是门户选错`).toHaveURL(new RegExp(`/${route}$`))
          await page.waitForTimeout(300)
          expect(await overflow(page, w), `${route} 在 ${w}px 下横向溢出`).toBeLessThanOrEqual(1)
          // 页面级溢出断言按构造看不见内层横滚:症状表第 3 行「审计页 .page-head
          // 内层横滚 638→735 @768」就是一处内层容器自己滚、外壳并不溢出的例子。
          // 只在这一条已知会发生过的路由 × 宽度上单独钉一句,不是给每条路由都加。
          if (w === 768 && route === 'audit') {
            expect(
              await page.locator('.page-head').evaluate((el) => el.scrollWidth - el.clientWidth),
              'audit 页 .page-head 在 768px 下内层横滚',
            ).toBeLessThanOrEqual(1)
          }
        }
      })
    }
  })
}

// 标题被挤成竖排单字时,它的高度会是行高的好几倍而宽度只有一两个字符。
// 这正是 390 下「数据库管理网关」与「默认策略」的样子。
test('窄屏下标题不被挤成竖排单字', async ({ page }) => {
  await stubAll(page)
  await loginAs(page, '管理后台')

  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto('/settings')
  await expect(page.locator('.top-title')).toBeVisible()

  // 适配下限那句话的另一半:「外壳在 390 下不应自身溢出」——实测从 33 修到了 0,
  // 但之前没有断言钉住它。折行判定只看 .top-title 自己的高宽比,看不到壳体整体
  // 溢出;这两件事根因相同(.top-head 的 min-width:0),但要各自断言。
  expect(await overflow(page, 390), '壳体在 390 下自身溢出').toBeLessThanOrEqual(1)

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

  await loginAs(page, '管理后台')

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
  await loginAs(page, '管理后台')

  // 9 个后台页面里,在这套「所有接口都答空集合」的桩下,实测只有 gov / users /
  // audit 三页会渲染 `.c-thead`(它们走共享的 `<Table>`,表头不看 rows 是否为空,
  // 无条件渲染 —— 见 `components/common/Table.tsx`)。risk-rules / sql-review /
  // permissions / pipelines / settings 本身就不用这套表格组件,是页面自己的设计。
  // connections 也有一张同构的表,但它的表头包在 `!!rows.length` 里(自己的分组
  // 表,不走 <Table>),这套桩把连接列表也答成空集合,所以它同样不渲染 —— 三种
  // 情况都不算缺陷,不要求每页都量到表。但如果某个路由被门户判断、守卫或组件
  // 报错送去了空白/别的页面,`.c-thead` 同样会是 0 个:循环体不执行,`bad` 恒为
  // `[]`,断言会无条件通过 —— 那不是「这页没有表」,是「这一轮什么都没量到」。
  // 用一个跨路由 × 跨宽度的累计计数在整轮结束后兜底:量到的表总数必须大于 0,
  // 否则说明前面的假阳性正在发生。
  let tablesSeen = 0

  // tablesSeen > 0 只挡得住「整轮一张表都没量到」。变异测试证实它挡不住「共用
  // Table.tsx 的表头整体消失」:把 c-thead 这个类名改掉,connections/users/
  // env-tiers 三张手搓表各自渲染自己的 .c-thead,计数照样撑得住,测试仍然通过。
  // gov / users / audit 三条路由本身就走共用 <Table>(参见上面对 c-thead 渲染
  // 条件的说明),单独给它们各自钉一句 > 0 才挡得住这一类回归——路由增减表格
  // 不会误报,只有「这条已知路由上共用组件的表头整体消失」才触发。connections
  // 不在这份名单里:它的表头包在 `!!rows.length` 里,这套全空桩下不渲染,拿来
  // 做锚点自己就会先假阳性。
  const SHARED_TABLE_ROUTES = ['gov', 'users', 'audit']
  const sharedSeen: Record<string, number> = { gov: 0, users: 0, audit: 0 }

  for (const w of WIDTHS) {
    await page.setViewportSize({ width: w, height: 900 })
    for (const route of ADMIN_ROUTES) {
      await page.goto(`/${route}`)
      await expect(page, `${route} 被静默送去了别处 —— 多半是门户选错`).toHaveURL(new RegExp(`/${route}$`))
      await page.waitForTimeout(300)
      const { bad, count } = await page.evaluate(() => {
        const out: string[] = []
        const heads = document.querySelectorAll('.c-thead')
        for (const head of heads) {
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
        return { bad: out, count: heads.length }
      })
      tablesSeen += count
      if (route in sharedSeen) sharedSeen[route] += count
      expect(bad, `${route} 在 ${w}px 下轨道与单元格对不上`).toEqual([])
    }
  }

  expect(tablesSeen, '整轮下来一张 .c-thead 表都没量到 —— 多半是路由被送去了别处,不是页面本身没有表').toBeGreaterThan(0)

  for (const route of SHARED_TABLE_ROUTES) {
    expect(sharedSeen[route], `${route} 在共用 <Table> 组件上一次 .c-thead 都没渲染出来 —— 共用表头可能整批消失了`).toBeGreaterThan(0)
  }
})
