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

/** 读一个元素(或它的伪元素)的计算样式。
 *  先等选择器挂载再读:调用方大多紧跟在 open()/goto() 后面就取值,并行 worker
 *  下页面还没渲染完就有概率撞上 —— 复现过一次间歇性的 "no element for …"。
 *  等待不改变任何断言,只是把"读之前先等它存在"这一步挪进 helper,
 *  别再让每个调用点各自处理一次。 */
async function styleOf(page: Page, sel: string, prop: string, pseudo?: string) {
  await page.waitForSelector(sel)
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

test.describe('HUD 交互态', () => {
  // open() 的兜底桩返回 envelope([]),而审计页读的是 data.items —— 取不到值,
  // 表格渲染 0 行,.c-trow 根本不存在。可点的行必须自己喂数据。
  // 字段名必须对上 types/index.ts 的 AuditRow —— 页面读的是 occurredAt /
  // instance / result / approvalNo,写成别的名字行会渲染出来但全是空格,
  // hover 测的是几何,空格量掉了就测不出位移。
  const AUDIT_ROWS = {
    items: [
      {
        id: 1, occurredAt: '2026-09-20T10:00:00Z', actor: 'linwei',
        instance: 'prod-mysql-01', database: 'shop', command: 'select 1',
        risk: 'low', result: 'executed', approvalNo: '', hash: 'a1',
      },
      {
        id: 2, occurredAt: '2026-09-20T10:01:00Z', actor: 'linwei',
        instance: 'prod-mysql-01', database: 'shop',
        command: 'update orders set a = 1 where id = 1',
        risk: 'high', result: 'executed', approvalNo: 'CR-2026-0001', hash: 'b2',
      },
    ],
    total: 2, page: 1, pageSize: 20,
  }
  async function openAudit(page: Page) {
    await open(page, 'about:blank')
    await page.route('**/api/v1/audit**', (r) => r.fulfill(envelope(AUDIT_ROWS)))
    await page.goto('/audit')
    await page.waitForSelector('.c-trow.clickable')
  }

  test('表格行 hover 时文字没有横向位移', async ({ page }) => {
    await openAudit(page)
    const cell = page.locator('.c-trow.clickable .c-td').first()
    const before = await cell.boundingBox()
    await page.locator('.c-trow.clickable').first().hover()
    const after = await cell.boundingBox()
    // 左侧竖条必须用 inset 阴影,不能用 border-left —— border 会把整行内容
    // 往右挤 2px,hover 一次行内文字跳一下。同 .rail-item.on 的做法。
    expect(after!.x).toBeCloseTo(before!.x, 1)
    expect(after!.width).toBeCloseTo(before!.width, 1)
  })

  test('表格行 hover 时出现左侧 accent 竖条', async ({ page }) => {
    await openAudit(page)
    const row = page.locator('.c-trow.clickable').first()
    await row.hover()
    const shadow = await row.evaluate((el) => getComputedStyle(el).boxShadow)
    expect(shadow).toContain('inset')
    expect(shadow).not.toBe('none')
  })

  test('可交互卡 hover 抬升,静态卡不动', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.dash-card')
    const card = page.locator('.dash-card').first()
    expect(await card.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
    await card.hover()
    // --transition-transform 的 --dur-base 是 220ms 的真实过渡,hover() 一返回就读
    // computed style 会拿到插值中间态(见 e2e/responsive.spec.ts 里同样的
    // waitForTimeout(300) 手法)。等过渡跑完,再读稳定值。
    await page.waitForTimeout(300)
    // translateY(-2px) → matrix(1, 0, 0, 1, 0, -2)
    const t = await card.evaluate((el) => getComputedStyle(el).transform)
    expect(t).not.toBe('none')
    expect(t).toContain('-2')
  })

  test('.portal-opt 选中态的辉光不被 hover 顶掉', async ({ page }) => {
    await page.goto('/login')
    const on = page.locator('.portal-opt.on').first()
    const before = await on.evaluate((el) => getComputedStyle(el).boxShadow)
    await on.hover()
    const after = await on.evaluate((el) => getComputedStyle(el).boxShadow)
    // hover 规则必须写成 :hover:not(.on),否则选中态的 --glow-sm 被投影盖掉。
    expect(after).toBe(before)
  })

  test('静态卡 hover 不抬升', async ({ page }) => {
    // 抬升只挂在 .dash-card / .perm-rcard / .portal-opt 上 —— 普通、非交互的
    // .c-card 不该动。/settings 的卡片就是纯 Card 组件,不带那三个类。
    await open(page, '/settings')
    await page.waitForSelector('.c-card')
    const card = page.locator('.c-card').first()
    await card.hover()
    // 同上面 .dash-card 的手法:等 220ms 的 transition 跑完,读的是稳定值,
    // 不是 hover() 刚返回时的插值中间态。
    await page.waitForTimeout(300)
    expect(await card.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
  })
})

test.describe('HUD 按钮与焦点', () => {
  test('主按钮是 azure→cyan 渐变,不是实色', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login-submit', 'background-image')
    expect(bg).toContain('gradient')
    // 设计系统明令禁止 azure→violet。紫色的 red 通道会明显高于 blue 通道之外
    // 还带出 red,这里只做一件事:确认渐变里没有出现紫。
    expect(bg).not.toMatch(/rgb\(\s*(1[0-9]{2}|[6-9][0-9])\s*,\s*[0-9]{1,2}\s*,\s*2[0-9]{2}/)
  })

  test('按钮按下时下沉', async ({ page }) => {
    // 用 /audit 不用 /dashboard:总览页自己一个 .c-btn 都没有,唯一可能的来源是
    // ErrorState 的重试按钮,而兜底桩不制造错误 —— 元素根本不存在。审计页头的
    // 风险筛选按钮(audit/index.tsx:212)是常驻控件,不依赖数据。
    await open(page, '/audit')
    await page.waitForSelector('.c-btn')
    const btn = page.locator('.c-btn').first()
    const box = (await btn.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    const t = await btn.evaluate((el) => getComputedStyle(el).transform)
    await page.mouse.up()
    // translateY(1px) scale(.99) → 一个非 none 的矩阵
    expect(t).not.toBe('none')
  })

  test('键盘焦点除 outline 外还有光晕', async ({ page }) => {
    await page.goto('/login')
    // 必须用真键盘走到它,不能用 .focus():Chrome 对按钮的程序化聚焦
    // **不**匹配 :focus-visible,而这条规则正是挂在 :focus-visible 上的。
    // 也不能拿输入框做靶子:theme.css:444 的 .login-card input:focus 写了
    // outline: none,特异度 (0,2,1) 压过 :focus-visible 的 (0,1,0),
    // 输入框上根本不该有 outline。
    for (let i = 0; i < 15; i++) {
      await page.keyboard.press('Tab')
      if (await page.locator('.login-submit:focus').count()) break
    }
    const cs = await page.locator('.login-submit').evaluate((el) => {
      const s = getComputedStyle(el)
      return { fv: el.matches(':focus-visible'), outline: s.outlineWidth, shadow: s.boxShadow }
    })
    expect(cs.fv).toBe(true)
    // outline 保留不动 —— 它是键盘可达性的底线,光晕只是叠加。
    expect(parseFloat(cs.outline)).toBeGreaterThan(0)
    expect(cs.shadow).not.toBe('none')
  })
})
