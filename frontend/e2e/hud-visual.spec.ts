import { test, expect, type Page } from '@playwright/test'
import { ADMIN, envelope, seedSession, stubShell } from './fixtures'
import { installWsFake } from './wsFake'

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

/** 一个元素底色(渐变的每一个色标)对它自己文字色的最小 WCAG 2.x 对比度。
 *  色标交给 canvas 画一像素再读回来 —— computed style 里 color-mix 会序列化成
 *  oklch()/oklab(),在 Node 里重算一遍色彩空间不如让 Chromium 自己画一次准。 */
async function fillContrast(page: Page, sel: string) {
  await page.waitForSelector(sel)
  return page.evaluate((s) => {
    const el = document.querySelector(s!)
    if (!el) throw new Error(`no element for ${s}`)
    const cs = getComputedStyle(el)
    const stops = cs.backgroundImage.match(/(?:rgba?|oklch|oklab|color|hsla?)\([^)]*\)/g) ?? []
    if (!stops.length) return -1
    const cv = document.createElement('canvas')
    cv.width = 1; cv.height = 1
    const ctx = cv.getContext('2d', { willReadFrequently: true })!
    const px = (c: string) => {
      ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = c; ctx.fillRect(0, 0, 1, 1)
      const d = ctx.getImageData(0, 0, 1, 1).data
      return [d[0], d[1], d[2]]
    }
    const lin = (c: number) => { const v = c / 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }
    const L = (v: number[]) => 0.2126 * lin(v[0]) + 0.7152 * lin(v[1]) + 0.0722 * lin(v[2])
    const text = L(px(cs.color))
    return Math.min(...stops.map((c) => {
      const bg = L(px(c))
      return (Math.max(bg, text) + 0.05) / (Math.min(bg, text) + 0.05)
    }))
  }, sel)
}

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

// 下面这几个 opener 放在模块作用域(而不是各自的 describe 里),是因为它们有两批
// 调用方:各档自己的断言,以及"滚动容器普查"那一条 —— 普查要走遍所有装饰容器
// 实际所在的页面,拿不到 opener 就只能缩在一个页面里扫,那正是它上一版的毛病。

// 字段名照 types/index.ts 的 Release 抄,不要凭印象写:列表读的是 relNo /
// changeType / pipelineName,写成 no / stage 之类的行会照常渲染出来但每格是空的,
// 而 hover 那条测的是几何,空格量掉了就等于没测。接口路径是 /releases,不是 /changes。
const RELEASES = {
  items: [
    {
      id: 1, relNo: 'REL-2026-0001', title: '清理过期订单备注',
      pipelineId: 1, pipelineName: '标准发布', connectionId: 1,
      instance: 'prod-mysql-01', database: 'shop', env: 'prod',
      tierCode: 'prod', engine: 'mysql', changeType: 'dml',
      status: 'pending', creator: 'linwei', createdAt: '2026-09-20T10:00:00Z',
    },
    {
      id: 2, relNo: 'REL-2026-0002', title: '补充索引',
      pipelineId: 1, pipelineName: '标准发布', connectionId: 2,
      instance: 'demo-dev', database: 'shop', env: 'dev',
      tierCode: 'dev', engine: 'mysql', changeType: 'ddl',
      status: 'pending', creator: 'linwei', createdAt: '2026-09-20T10:05:00Z',
    },
  ],
  total: 2, page: 1, pageSize: 50,
}

async function openChanges(page: Page) {
  await open(page, 'about:blank')
  // 列表和详情分开装桩:`**/api/v1/releases**` 会把 `/releases/:id`(详情)也
  // 吃掉,喂给它一个 { items, total } 形状的对象。详情区读 open.stages.find(...)
  // (waitingGate),stages 是 undefined 就整页面炸掉,连带 .chg-item 也渲染不出来
  // —— 抛的是 TypeError,不是找不到元素的断言失败,查网络请求才看得出来。
  await page.route('**/api/v1/releases?**', (r) => r.fulfill(envelope(RELEASES)))
  await page.route('**/api/v1/releases/*', (r) => {
    const id = Number(new URL(r.request().url()).pathname.split('/').pop())
    const item = RELEASES.items.find((it) => it.id === id) ?? RELEASES.items[0]
    r.fulfill(envelope({ ...item, stages: [] }))
  })
  await page.goto('/changes')
  await page.waitForSelector('.chg-item')
}

// 字段名照 types/index.ts:708 的 Pipeline 抄:id / name / description /
// tierCode / enabled / isDefault / stages(每条 stage 要 name + type,
// PipelineStage 在 types/index.ts:698)。接口是 GET /pipelines
// (api/modules/pipeline.ts 的 pipelineApi.pipelines),直接返回 Pipeline[],
// 不像 /releases 那样包一层 { items, total }。
const PIPELINES = [
  {
    id: 1, name: '标准发布', description: '审查通过后自动执行', tierCode: '',
    enabled: true, isDefault: true,
    stages: [
      { name: '审查', type: 'review', config: '{"failOn":"error"}', onFailure: 'abort' },
      { name: '执行', type: 'execute', config: '', onFailure: 'abort' },
    ],
  },
]

async function openPipelines(page: Page) {
  await open(page, 'about:blank')
  await page.route('**/api/v1/pipelines', (r) => r.fulfill(envelope(PIPELINES)))
  await page.goto('/pipelines')
  await page.waitForSelector('.pl-item')
}

// 在线表结构变更。字段名照 e2e/osc.spec.ts 的 JOBS 抄 —— 页面读 running /
// copiedRows / totalRows,写成别的名字行会渲染但进度条是空的。
// running: true 才拿得到 .osc-item.on(它表示"这条任务正在跑",不表示"被选中")。
const OSC_JOBS = [
  {
    id: 12, connectionId: 1, schema: 'app', table: 't_order',
    alter: 'ADD INDEX idx_memo (memo)', status: 'copying', shadow: 't_order_gho',
    copiedRows: 4000, totalRows: 10000, err: '', createdBy: 'Lin Wei',
    createdAt: '2026-09-18T10:00:00Z', updatedAt: '2026-09-18T10:01:00Z', finishedAt: null,
    running: true, throttle: '已启用:2 个从库,阈值 30s', throttled: true,
  },
]

async function openOsc(page: Page) {
  await open(page, 'about:blank')
  await page.route('**/api/v1/osc/status', (r) =>
    r.fulfill(envelope({ enabled: true, caveats: [] })))
  await page.route('**/api/v1/osc/jobs', (r) => r.fulfill(envelope(OSC_JOBS)))
  await page.goto('/osc')
  await page.waitForSelector('.osc-item')
}

// 不顶掉那条 socket,Vite 会把它转发给并不存在的 Go 后端,每跑一次就往输出里
// 刷一串 ECONNREFUSED —— 与本口无关的噪声,而它盖住的正是这一口自己的失败信息。
// 既有的 responsive / env-tier-tree / approval-card-long-sql 三套都是这么做的。
//
// 视口钉在 1500×900:Playwright 的 Desktop Chrome 默认是 1280×720,而 1280 正好
// 是终端取景框被收起的那一档(hud.css 的 @media (max-width: 1280px))—— 用默认
// 视口跑,下面几条关于取景框的断言全在对着一个 display: none 的节点读计算值。
async function openTerminal(page: Page) {
  await page.setViewportSize({ width: 1500, height: 900 })
  await open(page, 'about:blank')
  await installWsFake(page)
  await page.goto('/terminal')
  await page.waitForSelector('.tv-grid')
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
    // 实色 border 必须让位,否则渐变线叠在实线上等于没换 —— ::after 画在内距盒的
    // bottom: 0,正好压在实线正上方,读出来是一条 2px 的双线。
    //
    // 三个成员一个不落地验:这是一条组契约,而只盯着组里最老的那个成员,
    // 恰好拦不住"新成员入组时忘了改骨架层"—— .tv-panel-head 正是这样带着自己的
    // border-bottom 进来的,而这一条当时只问了 .c-card-head。
    expect(await styleOf(page, '.c-card-head', 'border-bottom-width')).toBe('0px')
    await open(page, '/dashboard')
    expect(await styleOf(page, '.dash-card > header', 'border-bottom-width')).toBe('0px')
    await openTerminal(page)
    expect(await styleOf(page, '.tv-panel-head', 'border-bottom-width')).toBe('0px')
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
    // theme.css 的 `.perm-rcard.on::before` 是这张卡的选中态左色条 —— 全站唯一的
    // 伪元素冲突点,卡片装饰不能画到它身上。按选择器引不按行号:theme.css 一长,
    // 行号就成了假话(本轮它就往下挪了 90 多行)。
    expect(await styleOf(page, '.perm-rcard.on', 'width', '::before')).toBe('3px')
  })
})

test.describe('HUD 交互态', () => {
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

  test('可交互卡 hover 抬升', async ({ page }) => {
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

  test('亮色下可交互卡 hover 用投影,暗色才换成辉光', async ({ page }) => {
    // 整份规格把亮暗当两个一等主题,但除了 token 那条,所有用例都落在暗色里 ——
    // 而"只在亮色成立"的规则全站只有这一条(.dash-card:hover 用 --shadow-lg,
    // 暗色覆写成 --glow-sm),在此之前它一条断言都没有,丢了也不会有人发现。
    await open(page, '/dashboard')
    await page.waitForSelector('.dash-card')
    await setTheme(page, 'light')
    const card = page.locator('.dash-card').first()
    await card.hover()
    // 同上,等 220ms 的过渡跑完再读。
    await page.waitForTimeout(300)
    const light = await card.evaluate((el) => getComputedStyle(el).boxShadow)
    // --shadow-lg(亮):冷灰蓝的投影 rgba(16, 24, 48, …),两段,外扩 28px。
    expect(light).toContain('rgba(16, 24, 48')
    expect(light).toContain('28px')
    await setTheme(page, 'dark')
    // box-shadow 也在 --transition-colors 里,换主题同样要等过渡跑完才读得到新值。
    await page.waitForTimeout(300)
    const dark = await card.evaluate((el) => getComputedStyle(el).boxShadow)
    // 设计系统:dark mode drops drop-shadows —— 暗色必须换成另一套值。
    expect(dark).not.toBe(light)
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

  test('主按钮底色的对比度不低于改版前的实色基线', async ({ page }) => {
    await page.goto('/login')
    // 改版前主按钮是实色 --accent,白字压上去实测 4.42(亮)/ 3.43(暗)。规格的
    // 非目标写着「对比度不退化」,而把 cyan 混进 --accent 会把渐变的青端拉到
    // 3.31 / 2.83 —— 主按钮是全站用得最多的控件,退化四分之一不能接受。
    // 现在 cyan 混进的是更深一档的 --azure-700:常态渐变最浅的一点就是起点
    // --accent 自己,也就是基线本身;hover 两个色标也都在基线之上。
    for (const [theme, floor] of [['light', 4.42], ['dark', 3.43]] as const) {
      await setTheme(page, theme)
      expect(await fillContrast(page, '.login-submit'), `${theme} 常态`).toBeGreaterThanOrEqual(floor)
      await page.locator('.login-submit').hover()
      expect(await fillContrast(page, '.login-submit'), `${theme} hover`).toBeGreaterThanOrEqual(floor)
      // 挪开鼠标,下一轮的常态才读得到常态。
      await page.mouse.move(0, 0)
    }
  })

  test('登录输入框聚焦时也有光晕', async ({ page }) => {
    await page.goto('/login')
    const shadow = await page.locator('.login-card input').first().evaluate((el: HTMLElement) => {
      el.focus()
      return getComputedStyle(el).boxShadow
    })
    // `box-shadow: var(--focus-ring)` 是非法值 —— 光秃秃一个颜色不是合法的 box-shadow。
    // 按 CSS 变量的语义,这种"算到计算值才发现非法"的声明照样赢下层叠、然后算成
    // none,于是它把 :focus-visible 新加的那圈光晕整条抹掉,偏偏抹在本轮的样板页上。
    expect(shadow).not.toBe('none')
    expect(shadow).toContain('4px')
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
    // 也不能拿输入框做靶子:theme.css 的 `.login-card input:focus` 写了
    // outline: none,特异度 (0,2,1) 压过 :focus-visible 的 (0,1,0),
    // 输入框上根本不该有 outline(那条规则的光晕另有一条用例看着)。
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

test.describe('HUD 活体状态', () => {
  test('健康指示灯在脉冲', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.pill-health')
    const name = await styleOf(page, '.pill-health svg', 'animation-name')
    expect(name).toBe('hud-pulse')
  })

  test('网关掉线时指示灯停跳', async ({ page }) => {
    // 这一枚是全站唯一表示"网关此刻在跑"的指示灯,掉线正是它必须停下来的时刻。
    // open() 把 gateway/stats 打成 online: true,这里再盖一层假的掉线。
    await open(page, 'about:blank')
    await page.route('**/api/v1/gateway/stats**', (r) => r.fulfill(
      envelope({ online: false, p50Ms: 0, samples: 0, intercepts: 0 })))
    await page.goto('/dashboard')
    await page.waitForSelector('.pill-health.off')
    expect(await styleOf(page, '.pill-health.off svg', 'animation-name')).toBe('none')
    // 静止态的辉光也要一起收:hud-pulse 的 filter 是 cyan 的 --glow-accent,
    // 红图标顶着一圈青光说的是两件互相矛盾的事。
    expect(await styleOf(page, '.pill-health.off svg', 'filter')).toBe('none')
  })

  test('reduce 下脉冲塌到 0', async ({ browser }) => {
    const ctx = await browser.newContext({ reducedMotion: 'reduce' })
    const page = await ctx.newPage()
    await open(page, '/dashboard')
    await page.waitForSelector('.pill-health')
    const name = await styleOf(page, '.pill-health svg', 'animation-name')
    await ctx.close()
    // e2e/reduced-motion.spec.ts 守的是同一条线:新动效必须能被 reduce 关掉。
    expect(name).toBe('none')
  })

  test('徽标有描边,不只是一块淡底', async ({ page }) => {
    // 不能用 /dashboard:总览页的每个 <Badge> 都在 rows.map() 里,兜底桩返回
    // 空数组,一个 .c-badge 都不渲染。审计表每行都有一枚(result 列),而
    // openAudit() 已经在喂数据了。
    await openAudit(page)
    await page.waitForSelector('.c-badge')
    expect(parseFloat(await styleOf(page, '.c-badge', 'border-top-width'))).toBeGreaterThan(0)
  })
})

test.describe('HUD 外壳', () => {
  test('顶栏是半透明 + 背景模糊,而且模糊不挂在 .top 自己身上', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.top', 'backdrop-filter', '::before')).toContain('blur')
    // 半透明:实色顶栏把下面的 HUD 底座切断了。
    const fill = await styleOf(page, '.top', 'background-color', '::before')
    expect(fill).not.toBe('rgba(0, 0, 0, 0)')
    // 带 backdrop-filter 的元素会成为它所有 position: fixed 后代的包含块,而通知
    // 面板那张 inset: 0 的点击遮罩就挂在顶栏里 —— 挂回 .top 上,它就从整个视口塌成
    // 顶栏那只 53px 的盒子。效果一模一样,包含块的副作用则没有。
    expect(await styleOf(page, '.top', 'backdrop-filter')).toBe('none')
  })

  test('顶栏下沿是渐变线,不是一条等浓的实线', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.top', 'background-image', '::after')).toContain('gradient')
    expect(await styleOf(page, '.top', 'height', '::after')).toBe('1px')
    // 实色 border 必须让位,否则渐变线叠在实线上等于没换(同 .c-card-head)。
    expect(await styleOf(page, '.top', 'border-bottom-width')).toBe('0px')
  })

  test('通知面板浮在页面内容之上,点击遮罩铺满视口', async ({ page }) => {
    // 铃铛面板是全站唯一挂在 .top 里的浮层,本轮有两处改动各打断它一半:
    // hud.css 的 .main > * 让 .content 和 .top 平级,.content 在 DOM 里靠后,面板被
    // 总览页的不透明卡片盖住;theme.css 给 .top 的 backdrop-filter 让 inset: 0 的
    // 遮罩塌成顶栏那只盒子,顶栏以下点空白关不掉面板。
    // 两半都只有"真把铃铛点开"才看得见 —— 在这条用例之前,整套 e2e 没有一处点过它。
    await open(page, 'about:blank')
    await page.route('**/api/v1/notifications**', (r) => r.fulfill(envelope({
      items: [{
        id: 1, type: 'approval-approved', title: 'CR-2026-0001 已通过',
        body: '', refNo: 'CR-2026-0001', read: false, createdAt: '2026-09-20T10:00:00Z',
      }],
      unread: 1,
    })))
    await page.goto('/dashboard')
    await page.waitForSelector('.dash-card')
    await page.click('.iconbtn.bell')
    await page.waitForSelector('.notif-panel')
    const r = await page.evaluate(() => {
      const panel = document.querySelector('.notif-panel')!.getBoundingClientRect()
      const hit = document.elementFromPoint(panel.x + panel.width / 2, panel.y + panel.height / 2)
      const bd = document.querySelector('.notif-backdrop')!.getBoundingClientRect()
      return {
        inPanel: !!(hit as HTMLElement | null)?.closest('.notif-panel'),
        hit: (hit as HTMLElement | null)?.className ?? null,
        bd: { x: bd.x, y: bd.y, w: bd.width, h: bd.height },
        vw: window.innerWidth, vh: window.innerHeight,
      }
    })
    // 必须用 elementFromPoint,不能用 toBeVisible:可见性判定不看绘制顺序,面板
    // 被卡片整个盖住时它照样算"可见"。
    expect(r.inPanel, `面板中心命中的是 ${r.hit}`).toBe(true)
    // 遮罩是"点面板外面就关掉"的唯一实现,必须是整个视口。
    expect(r.bd).toEqual({ x: 0, y: 0, w: r.vw, h: r.vh })
  })

  test('侧栏边缘是渐变竖线,不是一条等浓的灰线', async ({ page }) => {
    await open(page, '/dashboard')
    expect(await styleOf(page, '.rail', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.rail', 'border-right-width')).toBe('0px')
    // 不能用伪元素:.rail 是 overflow-y:auto 的滚动容器,绝对定位的后代会跟着
    // 内容滚 —— 菜单项一多,这条线就滚出可视区了。同 .c-table 那条。
    expect(await styleOf(page, '.rail', 'background-image', '::after')).toBe('none')
  })

  test('侧栏边线不参与布局,不挤窄菜单项', async ({ page }) => {
    await open(page, '/dashboard')
    const w = await page.locator('.rail').evaluate((el) => el.getBoundingClientRect().width)
    // 74px 是写死的栏宽(theme.css 的注释解释了为什么是 74)。装饰不能改它。
    expect(w).toBeCloseTo(74, 0)
  })

  test('侧栏选中项在色条之外还有外溢辉光', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.rail-item.on')
    const s = await styleOf(page, '.rail-item.on', 'box-shadow')
    // 既有的 inset 色条 + 新加的外溢辉光 = 两段阴影。数颜色函数的个数,
    // 不数逗号 —— 一段阴影自己就带好几个逗号。
    expect(s).toContain('inset')
    expect((s.match(/rgba?\(/g) ?? []).length).toBeGreaterThanOrEqual(2)
    // 外溢那一段不能也是 inset,否则辉光画在里面,外面看不见。
    expect(s.replace(/inset/, '')).not.toContain('inset')
  })
})

test.describe('HUD 指标数字', () => {
  test('普通指标数字是渐变裁字', async ({ page }) => {
    await open(page, '/dashboard')
    await page.waitForSelector('.dash-nums b')
    const cs = await page.locator('.dash-nums div:not(.warn) > b').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { clip: s.backgroundClip || s.webkitBackgroundClip, color: s.color, bg: s.backgroundImage }
    })
    expect(cs.clip).toBe('text')
    expect(cs.bg).toContain('gradient')
    // 裁字要求文字本身透明,否则实色盖在渐变上,渐变白画。
    expect(cs.color).toBe('rgba(0, 0, 0, 0)')
  })

  test('告警数字不裁字 —— 语义色不能被渐变冲淡', async ({ page }) => {
    await open(page, '/dashboard')
    // open() 把 intercepts 打成 2,GatewayCard 因此渲染出一个 .warn。
    await page.waitForSelector('.dash-nums .warn b')
    const cs = await page.locator('.dash-nums .warn > b').first().evaluate((el) => {
      const s = getComputedStyle(el)
      return { clip: s.backgroundClip || s.webkitBackgroundClip, color: s.color }
    })
    expect(cs.clip).not.toBe('text')
    expect(cs.color).not.toBe('rgba(0, 0, 0, 0)')
  })
})

test.describe('HUD 登录页', () => {
  test('登录卡有四角取景框,且对辅助技术隐藏、不吃点击', async ({ page }) => {
    await page.goto('/login')
    const el = page.locator('.login-card .hud-corners')
    await expect(el).toHaveCount(1)
    await expect(el).toHaveAttribute('aria-hidden', 'true')
    expect(await styleOf(page, '.hud-corners', 'pointer-events')).toBe('none')
    // 四个角:自身画两个(上),两个伪元素各画一个(下)。
    expect(parseFloat(await styleOf(page, '.hud-corners', 'border-left-width', '::before'))).toBeGreaterThan(0)
    expect(parseFloat(await styleOf(page, '.hud-corners', 'border-right-width', '::after'))).toBeGreaterThan(0)
  })

  test('取景框上方两角是完整的十字线(竖划+横划),不是半个角', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.hud-corners', 'background-image')
    // 每个上角要有一条竖划 + 一条横划,两角合计四层渐变 —— 只有两层说明
    // 每个角只画出了半划(比如只有竖划,没有横划,拼不成一个 L)。
    const layers = bg.split(/,(?=\s*linear-gradient)/)
    expect(layers.length).toBe(4)
    // 光数四层不够:四层全是竖划照样凑得出四层,而"少了横划"正是这条用例要盯的
    // 那个 bug。所以逐层钉方向 —— to right / to left 沿水平轴变色,画的是贴左 /
    // 贴右的竖划;to bottom 沿垂直轴变色,画的是贴顶的横划,而 Chromium 序列化时
    // 会把默认的 to bottom 省掉,于是"不带任何 to 关键字"就是横划的指纹。
    expect(layers[0]).toContain('to right')
    expect(layers[2]).toContain('to left')
    expect(layers[1]).not.toContain('to ')
    expect(layers[3]).not.toContain('to ')
    const pos = await styleOf(page, '.hud-corners', 'background-position')
    // 左上、右上都要出现 —— 缺一个就说明某个角一层都没画上。
    expect(pos).toContain('0% 0%')
    expect(pos).toContain('100% 0%')
  })

  test('登录卡顶沿的高光线来自装饰层', async ({ page }) => {
    await page.goto('/login')
    // 这条 ::before 原先在 theme.css 里跟 hud.css 的 .c-card::before 一字不差地
    // 重复了一遍。它是全新的装饰,按 hud.css 头部那条"按关注点划"的边界归装饰层,
    // 现在并进那条选择器列表 —— 合并之后必须仍然画得出来。
    expect(await styleOf(page, '.login-card', 'background-image', '::before')).toContain('gradient')
    expect(await styleOf(page, '.login-card', 'height', '::before')).toBe('1px')
  })

  test('装饰元素不改变登录卡尺寸', async ({ page }) => {
    await page.goto('/login')
    const w = await page.locator('.login-card').evaluate((el) => el.getBoundingClientRect().width)
    // 380px 是写死的卡宽。取景框是绝对定位,不能把它顶宽。
    expect(w).toBeCloseTo(380, 0)
  })

  test('登录页品牌标与侧栏 logo 用同一组渐变', async ({ page }) => {
    await page.goto('/login')
    const bg = await styleOf(page, '.login-mark', 'background-image')
    expect(bg).toContain('gradient')
    // #3b6ef6 → rgb(59, 110, 246);#2dcde6 → rgb(45, 205, 230)
    expect(bg).toContain('59, 110, 246')
    expect(bg).toContain('45, 205, 230')
  })
})

test.describe('HUD 第二阶段 · 面板档', () => {
  // 每个容器配一条能把它渲染出来的最小路径。列在一起是因为它们是同一档待遇,
  // 一条断言重复十一遍没有意义 —— 要验的是"这一档的规则挂上了"。
  const PANELS: Array<[string, string]> = [
    ['.perm-roles', '/permissions'],
    ['.set-nav', '/settings'],
    ['.set-save', '/settings'],
    ['.cat-side', '/catalog'],
    ['.cat-detail', '/catalog'],
  ]

  for (const [sel, path] of PANELS) {
    test(`${sel} 拿到顶沿高光线与右上角标`, async ({ page }) => {
      await open(page, path)
      expect(await styleOf(page, sel, 'background-image', '::before')).toContain('gradient')
      expect(await styleOf(page, sel, 'height', '::before')).toBe('1px')
      expect(await styleOf(page, sel, 'pointer-events', '::before')).toBe('none')
      expect(parseFloat(await styleOf(page, sel, 'border-top-width', '::after'))).toBeGreaterThan(0)
      expect(parseFloat(await styleOf(page, sel, 'border-right-width', '::after'))).toBeGreaterThan(0)
      // L 形,不是方框
      expect(parseFloat(await styleOf(page, sel, 'border-left-width', '::after'))).toBe(0)
    })
  }

  test('窄屏下 .set-nav / .cat-side 仍然是装饰的定位祖先', async ({ page }) => {
    await page.setViewportSize({ width: 768, height: 900 })
    await open(page, '/settings')
    // ≤768 的媒体查询把它们从 sticky 解除。解除必须落到 relative,不能落到
    // static —— static 会让 ::before 跑到更外层祖先上定位,高光线就画到别处去了。
    expect(await styleOf(page, '.set-nav', 'position')).toBe('relative')
    // .cat-side 在 /catalog 才渲染得出来,同一条媒体查询规则,两个选择器都断言,
    // 免得标题承诺了两个却只测了一个。
    await open(page, '/catalog')
    expect(await styleOf(page, '.cat-side', 'position')).toBe('relative')
  })
})

test.describe('HUD 第二阶段 · 滚动容器', () => {
  test('.ib-side 的高光线走 background-image,不走伪元素', async ({ page }) => {
    await open(page, '/inbox')
    expect(await styleOf(page, '.ib-side', 'background-image')).toContain('gradient')
    expect(await styleOf(page, '.ib-side', 'background-image', '::before')).toBe('none')
  })

  // 这条不针对某个容器,针对一类错误:本项目已经在 .c-table 和 .rail 上各犯过
  // 一次"给滚动容器挂绝对定位装饰"。将来任何人把某个装饰过的容器改成可滚,
  // 这条立刻红 —— 而"立刻红"要成立,普查必须同时满足两件事,上一版两件都不满足:
  //
  // 一是**扫到的页面要覆盖装饰容器实际所在的地方**。上一版只扫 /inbox,那一屏上
  //   唯一真正的被查对象是 .ib-side,而它上面那条断言已经直接点名验过了;把
  //   /catalog 的 .cat-detail 改成可滚,普查一条都不会红。所以这里按页面展开成
  //   一组用例 —— 每档待遇的容器都得有一页把它渲染出来。
  // 二是**认得出用 border 画的装饰**。角标是两条 border 画的(hud.css 的
  //   .c-card::after 那一组),不是 background。上一版只看 backgroundImage,
  //   于是"某个面板档容器变成可滚、角标从此跟着内容滚走"这一类 —— 也就是最像
  //   前两次事故的那一类 —— 恰好一条都拦不住。这里把判据放宽成"这个伪元素画了
  //   任何看得见的东西":背景图、非透明底色、或者任意一条非零宽度的 border。
  const DECORATED_PAGES: Array<[string, (page: Page) => Promise<void>]> = [
    ['/dashboard', (p) => open(p, '/dashboard')],   // .dash-card / .dash-card > header
    ['/settings', (p) => open(p, '/settings')],     // .c-card / .c-card-head / .set-nav / .set-save
    ['/catalog', (p) => open(p, '/catalog')],       // .cat-side / .cat-detail
    ['/permissions', (p) => open(p, '/permissions')], // .perm-roles
    ['/risk-rules', (p) => open(p, '/risk-rules')], // .rr-stat
    ['/async-jobs', (p) => open(p, '/async-jobs')], // .aj-item
    ['/inbox', (p) => open(p, '/inbox')],           // .ib-side
    ['/audit', openAudit],                          // .c-table
    ['/changes', openChanges],                      // .chg-item / .chg-detail
    ['/pipelines', openPipelines],                  // .pl-item / .pl-editor
    ['/osc', openOsc],                              // .osc-item
    ['/terminal', openTerminal],                    // .tv-tree / .tv-main / .tv-panel-head
  ]

  for (const [label, go] of DECORATED_PAGES) {
    test(`${label}:会滚的容器都不用绝对定位的伪元素承载装饰`, async ({ page }) => {
      await go(page)
      const bad = await page.evaluate(() => {
        const out: string[] = []
        const BORDERS = ['borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth'] as const
        for (const el of document.querySelectorAll<HTMLElement>('*')) {
          const cs = getComputedStyle(el)
          const scrolls = ['auto', 'scroll'].includes(cs.overflowX)
            || ['auto', 'scroll'].includes(cs.overflowY)
          if (!scrolls) continue
          for (const pe of ['::before', '::after']) {
            const p = getComputedStyle(el, pe)
            if (p.content === 'none') continue
            if (p.position !== 'absolute') continue
            // 画了东西才算装饰:光有 content 没有任何着色的伪元素(布局占位、
            // clearfix 之类)不该被算进来。
            const paints = p.backgroundImage !== 'none'
              || (p.backgroundColor !== 'rgba(0, 0, 0, 0)' && p.backgroundColor !== 'transparent')
              || BORDERS.some((k) => parseFloat(p[k]) > 0)
            if (paints) out.push(`${el.className || el.tagName}${pe}`)
          }
        }
        return out
      })
      expect(bad).toEqual([])
    })
  }
})

test.describe('HUD 第二阶段 · 列表行', () => {
  test('列表行的高光线走 ::after,不走 ::before', async ({ page }) => {
    await openChanges(page)
    // ::before 被选中态的 3px 左色条占着 —— 三处之一。
    expect(await styleOf(page, '.chg-item', 'background-image', '::after')).toContain('gradient')
    expect(await styleOf(page, '.chg-item', 'height', '::after')).toBe('1px')
  })

  test('列表行不拿角标', async ({ page }) => {
    await openChanges(page)
    // 角标是 border 画的;高光线是 background 画的。同一个 ::after 上,
    // 有 background 没有 border 才是"只要线不要角"。
    expect(parseFloat(await styleOf(page, '.chg-item', 'border-top-width', '::after'))).toBe(0)
  })

  test('列表行 hover 抬升', async ({ page }) => {
    await openChanges(page)
    const row = page.locator('.chg-item').first()
    expect(await row.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
    await row.hover()
    await page.waitForTimeout(300) // --dur-base 220ms,读的要是落定值
    const t = await row.evaluate((el) => getComputedStyle(el).transform)
    expect(t).toContain('-2')
  })

  // 这一档在 hover 上分家:四个容器都拿高光线,只有三个拿抬升。
  // .osc-item 是一个既没有 onClick 也没有 cursor: pointer 的 <article>
  // (pages/osc/index.tsx),它的 .on 表示"这条任务正在跑",不表示"被选中" ——
  // 抬升是一句"按下去会有反应"的承诺,而这里按下去什么都不会发生。
  // 两头都断言:线要在(它仍属于这一档),抬升不许有(它不可点)。
  test('.osc-item 拿高光线但不拿 hover 抬升 —— 它不可点', async ({ page }) => {
    await openOsc(page)
    expect(await styleOf(page, '.osc-item', 'background-image', '::after')).toContain('gradient')
    expect(await styleOf(page, '.osc-item', 'height', '::after')).toBe('1px')
    // 不是靠"没写 onClick"推断的,是直接问 DOM:整行不可点。
    expect(await styleOf(page, '.osc-item', 'cursor')).toBe('auto')

    const row = page.locator('.osc-item').first()
    expect(await row.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
    await row.hover()
    await page.waitForTimeout(300) // --dur-base 220ms,读的要是落定值
    expect(await row.evaluate((el) => getComputedStyle(el).transform)).toBe('none')
  })

  // .pl-item 是另一个 ::before 冲突容器。四个容器共用同一条逗号并列规则,
  // 但只在 .chg-item 上挂了几何断言,机制若在 .pl-item 上悄悄分叉不会被发现 ——
  // 这条补一次点验,不重复 hover/角标那两条(理由和 .chg-item 一样,机制是同一条
  // 规则给出的)。
  test('.pl-item 的高光线也走 ::after', async ({ page }) => {
    await openPipelines(page)
    expect(await styleOf(page, '.pl-item', 'background-image', '::after')).toContain('gradient')
    expect(parseFloat(await styleOf(page, '.pl-item', 'border-top-width', '::after'))).toBe(0)
  })

  // 三处选中态色条 —— 装饰不得覆盖它们。它们是这三个容器上唯一回答
  // "现在选的是哪一个"的东西。三个都要断言:.chg-item 和 .pl-item 正是这轮
  // 刚拿到装饰的两个,只验 .perm-rcard 等于没看它们有没有被自己刚加的规则
  // 顶掉的一半。
  test('三处选中态色条都还在', async ({ page }) => {
    // open() 的兜底桩把 /api/v1/roles 喂成 [],角色列表是空的,.perm-rcard
    // 根本不会渲染 —— 同一份 API 契约问题,只是换了个容器。这里同样得先装桩
    // 一张真角色卡,再导航,理由与 openChanges() 一致。
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
    expect(await styleOf(page, '.perm-rcard.on', 'width', '::before')).toBe('3px')

    // .chg-item:currentId 算出来就落在 items[0] 上(见 pages/changes/index.tsx),
    // 不用点,第一张单子进页面就是 .on。
    await openChanges(page)
    expect(await styleOf(page, '.chg-item.on', 'width', '::before')).toBe('3px')

    // .pl-item:同一个规矩,current 兜底落在 items[0](见 pages/pipelines/index.tsx
    // 的 `current = editing ?? clone(items[0])`),第一条模板进页面就是 .on。
    await openPipelines(page)
    expect(await styleOf(page, '.pl-item.on', 'width', '::before')).toBe('3px')
  })
})

test.describe('HUD 第二阶段 · 终端', () => {
  test('外框有四角取景框,且不吃点击', async ({ page }) => {
    await openTerminal(page)
    const el = page.locator('.tv-grid > .hud-corners')
    await expect(el).toHaveCount(1)
    await expect(el).toHaveAttribute('aria-hidden', 'true')
    expect(await styleOf(page, '.tv-grid > .hud-corners', 'pointer-events')).toBe('none')
  })

  test('取景框是绝对定位,不占终端的网格轨道', async ({ page }) => {
    await openTerminal(page)
    // .tv-grid 是 display:grid 且列模板写死三列。装饰节点若不是 absolute
    // 就会变成第四个网格项,把三栏挤位。
    expect(await styleOf(page, '.tv-grid > .hud-corners', 'position')).toBe('absolute')
  })

  test('树与终端栏各有顶沿高光线,但不各自加角标', async ({ page }) => {
    await openTerminal(page)
    // 类名是 .tv-*(终端 v2)。theme.css 里还留着一段 .term-* 是 v1 的死样式,
    // TSX 无引用 —— 拿它当靶子会一条都匹配不到。
    for (const sel of ['.tv-tree', '.tv-main']) {
      expect(await styleOf(page, sel, 'background-image', '::before')).toContain('gradient')
      // 角标只属于外框。三栏再各加一个,一屏就是四个角标。
      expect(parseFloat(await styleOf(page, sel, 'border-top-width', '::after'))).toBe(0)
    }
  })

  test('.tv-insp 不拿顶沿高光线,改由面板头拿渐变分隔线', async ({ page }) => {
    await openTerminal(page)
    await page.waitForSelector('.tv-insp')
    // 它是 overflow:auto 的滚动容器(伪元素会滚走),而它的首个子元素
    // .tv-panel-head 带不透明底色(background-image 会被盖住)。两条路都堵死,
    // 所以改走它自己的面板头 —— 那才是这一栏真正的顶沿。
    // 先钉住前提本身:hud.css 顶部那段"不进高光线组的两类"把 .tv-insp 列为第三个
    // 滚动容器,它一旦不再滚,那段话就成了假话,而下面两条断言照样全绿。
    expect(await styleOf(page, '.tv-insp', 'overflow-y')).toBe('auto')
    expect(await styleOf(page, '.tv-insp', 'background-image', '::before')).toBe('none')
    expect(await styleOf(page, '.tv-panel-head', 'background-image', '::after')).toContain('gradient')
  })

  /** 四个角标各取一个点(取景框自己的四个角),返回"绘制在取景框之上、且底色不透明"
   *  的元素 —— 也就是把角标挡住的东西。空数组 = 四个角都看得见。
   *
   *  elementsFromPoint 会跳过 pointer-events: none 的元素,取景框因此永远不在它
   *  返回的栈里,拿它当参照物得先临时把节点改成可命中。命中顺序就是绘制顺序的逆序
   *  (谁在上面先命中谁),而 pointer-events 不参与层叠 —— 改它不会改变结论,
   *  只是把结论问得出来。读完就改回去。 */
  async function coveredCorners(page: Page) {
    return page.evaluate(() => {
      const frame = document.querySelector('.tv-grid > .hud-corners') as HTMLElement
      const b = frame.getBoundingClientRect()
      const prev = frame.style.pointerEvents
      frame.style.pointerEvents = 'auto'
      const out: string[] = []
      for (const name of ['TL', 'TR', 'BL', 'BR']) {
        const x = name.includes('L') ? b.left + 0.5 : b.right - 0.5
        const y = name.includes('T') ? b.top + 0.5 : b.bottom - 0.5
        const stack = document.elementsFromPoint(x, y)
        const i = stack.indexOf(frame)
        if (i < 0) { out.push(`${name}:取景框在这个点上根本没被命中`); continue }
        for (const el of stack.slice(0, i)) {
          if (getComputedStyle(el).backgroundColor !== 'rgba(0, 0, 0, 0)') {
            out.push(`${name}:${el.className || el.tagName}`)
          }
        }
      }
      frame.style.pointerEvents = prev
      return out
    })
  }

  // 上面四条问的都是"这个节点长什么样",没有一条问过"它到底看不看得见" ——
  // 而第一阶段的取景框 bug 正是带着一串这样的绿色断言上线的,spec 第三节把它列为
  // 结构性守卫存在的理由。这条补上那个问题:
  // 三栏都是定位元素、都带不透明底色、又都排在 .hud-corners 后面,同为
  // z-index: auto 时按文档序绘制 —— 取景框不抬 z-index 就整个被盖在底下。
  for (const width of [1600, 1500]) {
    test(`${width} 宽下四个角标都真的看得见,没有被三栏盖住`, async ({ page }) => {
      await openTerminal(page)
      await page.setViewportSize({ width, height: 900 })
      await page.waitForTimeout(100)
      expect(await coveredCorners(page)).toEqual([])
    })
  }

  // 抬到三栏之上以后,角标就画在栏的内容上了,所以还要管它落在哪。
  // 收起态下那一栏塌成 30px 宽的导轨,当中只有一枚 16px 的展开图标:inset 10px
  // 时竖划正好从图标当中划过去,inset 5px 落在图标盒左边。装饰划在图标上,
  // 比装饰看不见更糟 —— 前者毁的是内容。
  test('收起成导轨时,角标不划在导轨的展开图标上', async ({ page }) => {
    await openTerminal(page)
    await page.locator('.tv-tree .tv-collapse').click()
    await page.waitForSelector('.tv-rail.left')
    const gap = await page.evaluate(() => {
      const frame = document.querySelector('.tv-grid > .hud-corners')!.getBoundingClientRect()
      const icon = document.querySelector('.tv-rail.left svg')!.getBoundingClientRect()
      // 竖划是 1px 宽,占 [frame.left, frame.left + 1)。整条要落在图标盒左边。
      return { strokeRight: frame.left + 1, iconLeft: icon.left }
    })
    expect(gap.strokeRight).toBeLessThanOrEqual(gap.iconLeft)
  })

  // ≤1280 与沉浸模式下 .tv-main 顶到栅格右沿,右上角标必然划在工具条最右那颗
  // 重连按钮上(见 hud.css 里的实测数字)。这两种情形整圈收起 —— 关键是"真不画",
  // 不是"留着但被盖住":后者正是这一轮要修的 bug,再犯一次不会有任何断言发现。
  test('≤1280 与沉浸模式下整圈取景框收起', async ({ page }) => {
    // 不能走 styleOf():它的 waitForSelector 默认等 visible,而这条恰恰要读一个
    // display: none 的节点,等到超时也等不到。
    const display = () => page.evaluate(() =>
      getComputedStyle(document.querySelector('.tv-grid > .hud-corners')!).display)

    await openTerminal(page)
    expect(await display()).not.toBe('none')

    await page.setViewportSize({ width: 1280, height: 900 })
    await page.waitForTimeout(100)
    expect(await display()).toBe('none')

    await page.setViewportSize({ width: 1500, height: 900 })
    await page.waitForTimeout(100)
    expect(await display()).not.toBe('none')
    // 工具条里第五颗按钮是沉浸模式(粘贴 / 片段 / 导出 / 表格视图 / 沉浸 / 检查器 / 重连)。
    await page.locator('.tv-bar button[title]').nth(4).click()
    await page.waitForSelector('.tv-grid.zen')
    expect(await display()).toBe('none')
  })
})

test.describe('HUD 第二阶段 · 覆盖面', () => {
  // 防两件事:漏掉一个容器(装饰不全),以及选择器写宽了误伤别的容器(装饰过度)。
  // 数字写死是有意的 —— 将来增删容器时这条会红,逼人回来改这份清单,
  // 而不是让覆盖面悄悄漂移。
  //
  // 只数"清单里的几个在场"防得住漏登记,防不住选择器被写宽多圈进一个清单外的
  // 容器 —— 清单本身还是全中,谁会注意到 hit 数没变。这里额外数规则里逗号
  // 分隔出的选择器**总数**,并与清单长度比对:写宽了,总数会先于 hit 露馅。
  //
  // **这条因此给 hud.css 立了一条长期约束:下面三组(顶沿高光线、右上角标、
  // 列表行高光线)每组都必须保持成一条逗号并列的规则。** 普查靠
  // selectorText.includes(...) 找到那条规则、再数它的逗号 —— 把一组拆成两条规则
  // 本身是个合法重构,但拆完之后这里只会数到其中一条,报出来的是"14 个变成了 9 个"
  // 这种看不出所以然的数字,而不是"你拆了规则"。真要拆,就把这里一并改成
  // 「把所有 selectorText 含该组任一选择器的规则合起来数」,别只改数字。
  test('拿到面板档装饰的容器,正好是清单上那些(数量不多不少)', async ({ page }) => {
    await open(page, '/settings')
    const result = await page.evaluate(() => {
      // 顶沿高光线:十四个,含 .login-card。
      const hairlineList = ['.c-card', '.dash-card', '.login-card', '.perm-roles', '.set-nav',
        '.set-save', '.cat-side', '.cat-detail', '.rr-stat', '.chg-detail',
        '.pl-editor', '.notif-panel', '.usermenu', '.conn-bulkbar']
      // 右上角标:十三个,比高光线少一个 —— .login-card 自己有一圈四角取景框
      // (hud.css「登录页」段的 .hud-corners),再挂一个右上角标是两套角标叠在
      // 一起,所以角标清单里故意不含它。两组数字不相等是设计使然,不是笔误。
      const cornerList = ['.c-card', '.dash-card', '.perm-roles', '.set-nav',
        '.set-save', '.cat-side', '.cat-detail', '.rr-stat', '.chg-detail',
        '.pl-editor', '.notif-panel', '.usermenu', '.conn-bulkbar']

      // selectorText 里逗号分隔的每一段,不同浏览器的空白/换行序列化不一样,
      // 裁剪后再按非空过滤,不能直接信 split(',').length。
      const selectorCount = (t: string) => t.split(',').map((s) => s.trim()).filter(Boolean).length

      let hairlineHit = 0, hairlineTotal = 0
      let cornerHit = 0, cornerTotal = 0
      for (const sheet of document.styleSheets) {
        let rules: CSSRuleList
        try { rules = sheet.cssRules } catch { continue }
        for (const r of rules) {
          const t = (r as CSSStyleRule).selectorText
          if (!t) continue
          if (t.includes('.c-card::before')) {
            hairlineHit = hairlineList.filter((s) => t.includes(`${s}::before`)).length
            hairlineTotal = selectorCount(t)
          }
          // 排除 .c-card-head::after(卡头分隔线,另一条规则):它的选择器文本
          // 里也含子串 ".c-card",但紧跟的是 "-head::after" 不是 "::after"。
          if (t.includes('.c-card::after')) {
            cornerHit = cornerList.filter((s) => t.includes(`${s}::after`)).length
            cornerTotal = selectorCount(t)
          }
        }
      }
      return { hairlineHit, hairlineTotal, cornerHit, cornerTotal }
    })
    // 漏登记:清单里的某个不在规则里,hit < 清单长度。
    expect(result.hairlineHit).toBe(14)
    expect(result.cornerHit).toBe(13)
    // 装饰过度:规则里的选择器比清单多,total > 清单长度(hit 可能仍然满分)。
    expect(result.hairlineTotal).toBe(14)
    expect(result.cornerTotal).toBe(13)
  })

  test('拿到列表行装饰的容器,正好是四个(数量不多不少)', async ({ page }) => {
    await open(page, '/settings')
    const result = await page.evaluate(() => {
      const list = ['.chg-item', '.pl-item', '.aj-item', '.osc-item']
      const selectorCount = (t: string) => t.split(',').map((s) => s.trim()).filter(Boolean).length
      for (const sheet of document.styleSheets) {
        let rules: CSSRuleList
        try { rules = sheet.cssRules } catch { continue }
        for (const r of rules) {
          const t = (r as CSSStyleRule).selectorText
          if (t && t.includes('.chg-item::after')) {
            return { hit: list.filter((s) => t.includes(`${s}::after`)).length, total: selectorCount(t) }
          }
        }
      }
      return { hit: -1, total: -1 }
    })
    expect(result.hit).toBe(4)
    expect(result.total).toBe(4)
  })
})

/* ============================================================
   第三阶段:补齐、清理、空态框
   ============================================================ */

// 脚本扫描弹窗要点开才有 .scan-nums。列表接口给一份上传,扫描接口给一份三档
// 都非零的结果 —— 三档全是 0 的话,.danger / .warn 两个类不会挂上去,
// 那两条豁免断言就变成了对着不存在的元素断言。
const SCAN = {
  filename: 'demo.sql', total: 12, high: 2, mid: 3, safe: 7, hasRisky: true,
  statements: [
    { index: 1, sql: 'DROP TABLE t;', command: 'DROP', risk: 'high', noWhere: false },
    { index: 2, sql: 'UPDATE t SET a=1;', command: 'UPDATE', risk: 'mid', noWhere: true },
    { index: 3, sql: 'SELECT 1;', command: 'SELECT', risk: 'safe', noWhere: false },
  ],
}

async function openScanModal(page: Page) {
  await open(page, 'about:blank')
  await page.route('**/api/v1/scripts/uploads', (r) => r.fulfill(envelope([
    { id: 1, userId: 1, filename: 'demo.sql', path: '/tmp/demo.sql', size: 120,
      source: 'upload', createdAt: '2026-09-20T10:00:00Z' },
  ])))
  await page.route('**/api/v1/scripts/scan', (r) => r.fulfill(envelope(SCAN)))
  await page.goto('/scripts')
  // 下发按钮:ScriptCard 的 .sc-ops 里那颗 variant="primary"。
  await page.click('.sc-ops .c-btn.v-primary')
  await page.waitForSelector('.scan-nums')
}

test.describe('HUD 第三阶段', () => {
  test('扫描弹窗的大数字也渐变裁字,两档语义色都豁免', async ({ page }) => {
    await openScanModal(page)
    // 普通档:和 .dash-nums b / .us-statv 同一套处理。
    const plain = await page.locator('.scan-nums div:not(.danger):not(.warn) > b').first().evaluate((el) => {
      const cs = getComputedStyle(el)
      return { clip: cs.backgroundClip || cs.webkitBackgroundClip, color: cs.color, bg: cs.backgroundImage }
    })
    expect(plain.clip).toBe('text')
    expect(plain.bg).toContain('gradient')
    expect(plain.color).toBe('rgba(0, 0, 0, 0)')

    // 两档豁免。.dash-nums 只有一档(warn),这里有两档 —— 语义色是这两个数字
    // 唯一要传达的东西,渐变会把它冲淡。
    for (const tone of ['danger', 'warn']) {
      const cs = await page.locator(`.scan-nums .${tone} > b`).first().evaluate((el) => {
        const s = getComputedStyle(el)
        return { clip: s.backgroundClip || s.webkitBackgroundClip, color: s.color }
      })
      expect(cs.clip, `${tone} 档不该被裁字`).not.toBe('text')
      expect(cs.color, `${tone} 档要保留语义色`).not.toBe('rgba(0, 0, 0, 0)')
    }
  })

  test('.term-kv 是活的,不在终端 v1 的清理范围里', async ({ page }) => {
    await openTerminal(page)
    await page.waitForSelector('.term-kv')
    // theme.css 里那一段 .term-* 大多是终端 v1 的死样式,但 .term-kv 不是 ——
    // v2 的检查器(terminal/index.tsx 的 <dl className="term-kv">)还在用它,
    // 而且 .tv-insp .term-kv 专门覆写过。整段删掉会把检查器的键值排版打散。
    const cs = await page.locator('.term-kv').first().evaluate((el) => {
      const s = getComputedStyle(el)
      const dt = el.querySelector('dt')
      return {
        display: s.display, dir: s.flexDirection,
        dtFont: dt ? getComputedStyle(dt).textTransform : null,
      }
    })
    expect(cs.display).toBe('flex')
    expect(cs.dir).toBe('column')
    expect(cs.dtFont).toBe('uppercase')
  })

  test('空态框拿角标,不拿顶沿高光线', async ({ page }) => {
    await open(page, '/inbox')
    await page.waitForSelector('.ib-empty')
    // 角标说的是"这是一块划定的区域" —— 空态框正是。
    expect(parseFloat(await styleOf(page, '.ib-empty', 'border-top-width', '::after'))).toBeGreaterThan(0)
    expect(parseFloat(await styleOf(page, '.ib-empty', 'border-right-width', '::after'))).toBeGreaterThan(0)
    // 高光线说的是"这块面板的内容从这里开始" —— 空态框里没有内容,那句话是假的。
    expect(await styleOf(page, '.ib-empty', 'background-image', '::before')).toBe('none')
  })
})
