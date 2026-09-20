import { test, expect, type Page } from '@playwright/test'
import { ADMIN, envelope, seedSession, stubShell } from './fixtures'

// HUD 视觉层盯的是"层叠算完之后才成立"的事:一个伪元素有没有背景、一条装饰
// 会不会跟着横滚容器滚出可视区、按下时有没有位移、动效在 reduce 下有没有塌到 0。
// 类型检查、构建、Node 里的单测都看不见这些 —— 只有真浏览器算得出来。
//
// 全部集中在这一个文件里:它们测的是同一层东西(装饰层),改动也总是一起发生。

/** 打开一个已登录的后台页面。gateway/stats 覆盖掉 fixtures 的默认值 —— 总览页
 *  要同时有"普通数字"和"告警数字"两种 .dash-nums,拦截数必须非零。 */
async function open(page: Page, path: string) {
  await seedSession(page)
  // 兜底先注册:Playwright 后注册者优先,stubShell 才盖得住它。
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page, ADMIN)
  await page.route('**/api/v1/gateway/stats**', (r) => r.fulfill(
    envelope({ online: true, p50Ms: 3, p95Ms: 11, samples: 128, intercepts: 2 })))
  // 传 'about:blank' 表示"只装桩,先别跳" —— 调用方还要再加自己的路由,
  // 而 Playwright 的路由是后注册者优先,必须在 goto 之前注册完。
  if (path !== 'about:blank') await page.goto(path)
}

/** 切主题。stores/ui.ts 把主题写在 <html data-theme> 上。 */
async function setTheme(page: Page, theme: 'light' | 'dark') {
  await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
}

/** 读一个元素(或它的伪元素)的计算样式。 */
function styleOf(page: Page, sel: string, prop: string, pseudo?: string) {
  return page.evaluate(([s, p, pe]) => {
    const el = document.querySelector(s!)
    if (!el) throw new Error(`no element for ${s}`)
    return getComputedStyle(el, pe || undefined).getPropertyValue(p!)
  }, [sel, prop, pseudo ?? ''] as const)
}

test.describe('HUD 底座', () => {
  test('两套主题都解析出 HUD token,且取值不同', async ({ page }) => {
    await open(page, '/dashboard')
    const read = () => page.evaluate(() => {
      const cs = getComputedStyle(document.documentElement)
      return {
        grid: cs.getPropertyValue('--hud-grid').trim(),
        glowA: cs.getPropertyValue('--hud-glow-a').trim(),
        corner: cs.getPropertyValue('--hud-corner').trim(),
        size: cs.getPropertyValue('--hud-grid-size').trim(),
      }
    })

    await setTheme(page, 'light')
    const light = await read()
    await setTheme(page, 'dark')
    const dark = await read()

    for (const v of [light.grid, light.glowA, light.corner, light.size]) expect(v).not.toBe('')
    expect(light.size).toBe('32px')
    // 亮暗必须是两套值,不是同一套照抄 —— 亮色网格是冷灰蓝,暗色是白色低透明。
    expect(dark.grid).not.toBe(light.grid)
    expect(dark.glowA).not.toBe(light.glowA)
    expect(dark.corner).not.toBe(light.corner)
  })

  test('底座画在 .main::before 上,不吃点击', async ({ page }) => {
    await open(page, '/dashboard')
    const bg = await styleOf(page, '.main', 'background-image', '::before')
    // 两层径向辉光 + 两层网格线
    expect(bg).toContain('radial-gradient')
    expect(bg).toContain('linear-gradient')
    expect(await styleOf(page, '.main', 'pointer-events', '::before')).toBe('none')
    expect(await styleOf(page, '.main', 'position')).toBe('relative')
  })

  test('底座不随内容滚动 —— 它挂在 .main 上,不是滚动容器 .content', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.content', 'background-image')).toBe('none')
    expect(await styleOf(page, '.content', 'overflow-y')).toBe('auto')
  })

  test('登录页也有底座', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login', 'background-image', '::before')
    expect(bg).toContain('radial-gradient')
  })
})

test.describe('HUD 卡片', () => {
  test('卡片顶沿有 1px 渐变高光线', async ({ page }) => {
    await open(page, '/settings')
    expect(await styleOf(page, '.c-card', 'background-image', '::before')).toContain('gradient')
    expect(await styleOf(page, '.c-card', 'height', '::before')).toBe('1px')
    expect(await styleOf(page, '.c-card', 'pointer-events', '::before')).toBe('none')
  })

  test('卡片右上角有 L 形角标', async ({ page }) => {
    await open(page, '/settings')
    const w = await styleOf(page, '.c-card', 'border-top-width', '::after')
    const r = await styleOf(page, '.c-card', 'border-right-width', '::after')
    expect(parseFloat(w)).toBeGreaterThan(0)
    expect(parseFloat(r)).toBeGreaterThan(0)
    // L 形 —— 只有上和右,左和下必须是 0,否则画出来是个方框。
    expect(parseFloat(await styleOf(page, '.c-card', 'border-left-width', '::after'))).toBe(0)
    expect(parseFloat(await styleOf(page, '.c-card', 'border-bottom-width', '::after'))).toBe(0)
  })

  test('.c-table 的高光线用 background-image,不用伪元素', async ({ page }) => {
    await open(page, '/audit')
    await page.waitForSelector('.c-table')
    // 它是 overflow-x:auto 的横滚容器。绝对定位的后代会跟着内容滚出可视区,
    // 而 background-attachment 默认 scroll,锚在元素自己的边框盒上 —— 不随内容滚。
    expect(await styleOf(page, '.c-table', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.c-table', 'overflow-x')).toBe('auto')
    expect(await styleOf(page, '.c-table', 'background-image', '::before')).toBe('none')
  })

  test('卡头分隔线是渐变,不是一条等浓的实线', async ({ page }) => {
    await open(page, '/settings')
    await page.waitForSelector('.c-card-head')
    expect(await styleOf(page, '.c-card-head', 'background-image', '::after')).toContain('gradient')
    // 实色 border 必须让位,否则渐变线叠在实线上等于没换。
    expect(await styleOf(page, '.c-card-head', 'border-bottom-width')).toBe('0px')
  })

  test('表头去掉了实底,底线是渐变', async ({ page }) => {
    await open(page, '/audit')
    await page.waitForSelector('.c-thead')
    const cs = await page.locator('.c-thead').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { color: s.backgroundColor, img: s.backgroundImage, border: s.borderBottomWidth }
    })
    // 透明:实底把表头从卡片里切出来,而它本来就是这张卡的一部分。
    expect(cs.color).toBe('rgba(0, 0, 0, 0)')
    expect(cs.img).toContain('gradient')
    expect(cs.border).toBe('0px')
  })

  test('.perm-rcard 的选中色条没被装饰顶掉', async ({ page }) => {
    // open() 的兜底桩把 /api/v1/roles 喂成 [],角色列表是空的,.perm-rcard
    // 根本不会渲染。这里要一张真卡片来点,所以先只装桩(不跳转),把角色列表
    // 和详情单独喂上,再自己导航 —— open() 的注释里写的就是这个用法。
    await open(page, 'about:blank')
    await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([
      { id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown', count: 1 },
    ])))
    await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope({
      id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown',
      members: [], matrix: {}, menus: {}, tags: [],
    })))
    await page.goto('/permissions')
    await page.waitForSelector('.perm-rcard')
    await page.click('.perm-rcard')
    await page.waitForSelector('.perm-rcard.on')
    // theme.css:1060 的 ::before 是这张卡的选中态左色条 —— 全站唯一的伪元素
    // 冲突点。卡片装饰不能画到它身上。
    expect(await styleOf(page, '.perm-rcard.on', 'width', '::before')).toBe('3px')
  })
})
