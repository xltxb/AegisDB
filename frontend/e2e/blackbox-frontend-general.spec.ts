import { test, expect, type Page } from '@playwright/test'
import { envelope, failure, stubCommon, gotoTerminal } from './helpers'

// 2026-10 全面黑盒测试 · 工单 05(前端与通用)。每条缺陷一个用例,钉住修复后的可见行为。

const CONNS = [
  { id: 1, name: 'ORDER-PROD', env: 'prod', engine: 'MySQL 8.0', host: 'h', port: 3306, database: 'shop', defaultRole: 'dba_l2', status: 'online', policy: 'strict', tags: '' },
  { id: 2, name: 'BILL-PROD', env: 'prod', engine: 'MySQL 8.0', host: 'h', port: 3306, database: 'bill', defaultRole: 'dba_l2', status: 'online', policy: 'strict', tags: '' },
  { id: 3, name: 'SANDBOX', env: 'dev', engine: 'MySQL 8.0', host: 'h', port: 3306, database: 'app', defaultRole: 'dev', status: 'online', policy: 'audit-only', tags: '' },
]
const TIERS = [
  { code: 'prod', displayName: '生产环境 · PROD', sortOrder: 0, requireMfa: true, dangerBanner: true, countsInPending: true, scanBaseline: true, strictNoWhere: true, connLayer: 'L1', defaultRole: 'dba_l2' },
  { code: 'dev', displayName: '测试 · DEV', sortOrder: 1, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, strictNoWhere: false, connLayer: 'L4', defaultRole: 'dev' },
]
const ENVS = [
  { code: 'prod', displayName: '生产环境 · PROD', tierCode: 'prod', sortOrder: 0 },
  { code: 'dev', displayName: '测试 · DEV', tierCode: 'dev', sortOrder: 1 },
]

async function shell(page: Page) {
  await stubCommon(page, { tiers: TIERS, envs: ENVS })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(CONNS)))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
}

const apRow = (over: Record<string, unknown> = {}) => ({
  id: 1, apNo: 'AP-20261019-2417', type: 'release', releaseId: 7, command: 'REL-0087 升级单审批', riskLevel: 'high',
  connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders',
  keyword: '', initiatorId: 2, initiator: 'Chen Hao', reason: 'x', status: 'pending',
  createdAt: '2026-09-14T08:00:00Z', steps: [], canDecide: false, blockReason: 'x', canExecute: false, canCancel: false, ...over,
})

// ---- 1. 自助修改密码 ----
test.describe('自助修改密码', () => {
  test('头像菜单打开弹窗,提交后换上服务端重签的 token', async ({ page }) => {
    const bodies: any[] = []
    await shell(page)
    await page.route('**/api/v1/auth/password', (r) => {
      bodies.push(r.request().postDataJSON())
      return r.fulfill(envelope({ session: '1.fresh-token', expiresAt: '2026-10-19 21:00:00' }))
    })
    await page.goto('/#/dashboard')
    await page.locator('.avatar').click()
    await page.getByTestId('menu-password').click()
    const modal = page.getByTestId('password-modal')
    await expect(modal).toBeVisible()

    // 前端先挡掉两次不一致,不发请求
    await page.getByTestId('pw-old').fill('vela123')
    await page.getByTestId('pw-new').fill('newpass99')
    await page.getByTestId('pw-confirm').fill('newpass98')
    await page.getByTestId('pw-submit').click()
    await expect(page.getByTestId('pw-err')).toHaveText('两次输入的新密码不一致')
    expect(bodies).toHaveLength(0)

    await page.getByTestId('pw-confirm').fill('newpass99')
    await page.getByTestId('pw-submit').click()
    await expect(modal).toHaveCount(0)
    expect(bodies).toEqual([{ oldPassword: 'vela123', newPassword: 'newpass99' }])
    expect(await page.evaluate(() => localStorage.getItem('vela_session'))).toBe('1.fresh-token')
    await expect(page.getByText('密码已修改,其它设备上的登录已失效')).toBeVisible()
  })

  test('服务端拒绝时原样显示理由,弹窗不关', async ({ page }) => {
    await shell(page)
    await page.route('**/api/v1/auth/password', (r) => r.fulfill(failure('原密码错误')))
    await page.goto('/#/dashboard')
    await page.locator('.avatar').click()
    await page.getByTestId('menu-password').click()
    await page.getByTestId('pw-old').fill('wrong-old')
    await page.getByTestId('pw-new').fill('newpass99')
    await page.getByTestId('pw-confirm').fill('newpass99')
    await page.getByTestId('pw-submit').click()
    await expect(page.getByTestId('pw-err')).toHaveText('原密码错误')
    await expect(page.getByTestId('password-modal')).toBeVisible()
    expect(await page.evaluate(() => localStorage.getItem('vela_session'))).toBe('1.e2e-token')
  })
})

// ---- 3. 终端页写死的原型文案 ----
test('终端:无标签时副标题是中性提示;树底部按可见实例统计', async ({ page }) => {
  await shell(page)
  await page.goto('/#/terminal')
  await expect(page.locator('.inst').first()).toBeVisible()
  await expect(page.locator('.tb-sub')).toHaveText('从左侧选择实例打开标签页')
  await expect(page.getByTestId('tree-foot')).toHaveText('2 环境 · 3 实例 · 已接入网关')
  await page.locator('.inst', { hasText: 'ORDER-PROD' }).click()
  await expect(page.locator('.tb-sub')).toHaveText('prod-ORDER-PROD · dba_l2')
})

// ---- 4. 用户与权限:加载中不显示 0 ----
test('用户与权限:用户列表没到之前不报「0 用户」', async ({ page }) => {
  await shell(page)
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([{ id: 1, code: 'admin', name: '平台管理员', layer: 'L0', icon: 'crown', members: 1 }])))
  await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope({ id: 1, code: 'admin', name: '平台管理员', layer: 'L0', icon: 'crown', menus: {}, matrix: {}, members: [], memberIds: [], tags: [] })))
  let release!: () => void
  const gate = new Promise<void>((res) => { release = res })
  await page.route('**/api/v1/users', async (r) => {
    await gate
    return r.fulfill(envelope([{ id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW', dept: '', roles: ['平台管理员'], roleIds: [1], primaryRoleId: 1, status: 'active', kind: 'human', mfaEnabled: false, lastActive: '', ssoBound: false, canDisable: false, disableBlock: '' },
      { id: 2, name: 'Chen Hao', email: 'chenhao@vela.io', initials: 'CH', dept: '', roles: [], roleIds: [], primaryRoleId: 0, status: 'active', kind: 'human', mfaEnabled: false, lastActive: '', ssoBound: false, canDisable: true, disableBlock: '' }]))
  })
  await page.goto('/#/admin/permissions')
  await expect(page.locator('.tb-sub')).toHaveText('加载中…')
  await page.locator('.vt', { hasText: '用户视图' }).click().catch(() => {})
  await expect(page.locator('.tb-sub')).not.toContainText('0 用户')
  release()
  await expect(page.locator('.tb-sub')).toHaveText('1 角色 · 2 用户')
})

// ---- 5. 英文下分层表格列头不重叠 ----
test('分层与环境(英文):开关列头可换行,彼此不重叠', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('vela_lang', 'en'))
  await shell(page)
  await page.goto('/#/admin/env-tiers')
  const spans = page.getByTestId('tier-th').locator('> span')
  await expect(spans.nth(3)).toContainText(/pending/i)
  const boxes = await spans.evaluateAll((els) => els.map((e) => {
    const r = document.createRange(); r.selectNodeContents(e)
    const b = r.getBoundingClientRect(); const cell = e.getBoundingClientRect()
    return { textRight: b.right, cellRight: cell.right, text: e.textContent }
  }))
  for (const b of boxes) {
    if (!b.text) continue
    expect(b.textRight, `列头「${b.text}」越过了自己的格子`).toBeLessThanOrEqual(b.cellRight + 1)
  }
})

// ---- 6 & 10. 审批页副标题、英文单号与状态下拉 ----
test('审批页:副标题不以「·」开头', async ({ page }) => {
  await shell(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => r.fulfill(envelope({ items: [apRow()], total: 1, pending: 1, page: 1, pageSize: 20 })))
  await page.goto('/#/approvals')
  const sub = (await page.getByTestId('appr-sub').innerText()).trim()
  expect(sub.startsWith('·')).toBe(false)
  expect(sub).toBe('通过后由发起人自己执行，全程写入审计')
})

test('审批列表(英文):单号不折行,「All statuses」不被截断', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('vela_lang', 'en'))
  await shell(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => r.fulfill(envelope({ items: [apRow()], total: 1, pending: 1, page: 1, pageSize: 20 })))
  await page.goto('/#/approvals')
  const no = page.locator('.tr .apno').first()
  await expect(no).toHaveText('#AP-20261019-2417')
  const lineH = await no.evaluate((e) => parseFloat(getComputedStyle(e).lineHeight) || 18)
  expect((await no.boundingBox())!.height).toBeLessThan(lineH * 1.5)
  const label = page.locator('.chead .stsel').first().locator('.control span').first()
  await expect(label).toHaveText('All statuses')
  expect(await label.evaluate((e) => e.scrollWidth <= e.clientWidth + 1)).toBe(true)
})

// ---- 7. 新建连接留空提交 ----
test('新建连接:留空点「测试保存」标出必填项并提示,不发请求', async ({ page }) => {
  const posts: string[] = []
  await shell(page)
  page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/api/v1/connections')) posts.push(r.url()) })
  await page.goto('/#/admin/connections')
  await page.getByRole('button', { name: '新建连接' }).click()
  await page.getByRole('button', { name: '测试保存' }).click()
  await expect(page.getByTestId('new-conn-required')).toBeVisible()
  await expect(page.getByTestId('new-conn-name')).toHaveClass(/invalid/)
  await expect(page.getByTestId('new-conn-host')).toHaveClass(/invalid/)
  expect(posts).toHaveLength(0)
  // 填上名字,红框跟着撤掉那一格
  await page.getByTestId('new-conn-name').fill('order-2')
  await expect(page.getByTestId('new-conn-name')).not.toHaveClass(/invalid/)
  await expect(page.getByTestId('new-conn-host')).toHaveClass(/invalid/)
})

// ---- 8. 审批弹窗重复提交 ----
test('审批弹窗:同一事件循环里连点两下「提交审批」只发一次', async ({ page }) => {
  const execs: unknown[] = []
  await shell(page)
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'approve', risk: 'high', requiresApproval: true, matchedRule: 'DROP' })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    execs.push(r.request().postDataJSON())
    return r.fulfill(envelope({ intercepted: true, approvalNo: 'AP-1', risk: 'high', output: '' }))
  })
  await gotoTerminal(page, 'SANDBOX') // 测试环境:不弹生产确认
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  await page.keyboard.type('drop table t;')
  await page.keyboard.press('Enter')
  await page.locator('.reason textarea').fill('回归验证')
  await page.getByTestId('appr-submit').evaluate((b) => { (b as HTMLButtonElement).click(); (b as HTMLButtonElement).click() })
  await expect.poll(() => execs.length).toBe(1)
  await page.waitForTimeout(500)
  expect(execs).toHaveLength(1)
})

// ---- 8b. 审批弹窗的焦点 ----
// 2026-10-29 生产:弹窗出来后焦点还在终端上,接着打的原因(连同中文输入法的候选框)全落在身后的终端里,
// 原因框是空的、提交按钮一直灰着。弹窗打开即聚焦原因框;关掉之后焦点回到终端。
test('审批弹窗:打开即聚焦原因框,直接打字就进原因框;取消后焦点回到终端', async ({ page }) => {
  await shell(page)
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'approve', risk: 'high', requiresApproval: true, matchedRule: 'DROP' })))
  await gotoTerminal(page, 'SANDBOX')
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  await page.keyboard.type('drop table t;')
  await page.keyboard.press('Enter')
  const reason = page.getByTestId('appr-reason')
  await expect(reason).toBeFocused()
  await page.keyboard.type('清理临时表')
  await expect(reason).toHaveValue('清理临时表')
  await expect(page.getByTestId('appr-submit')).toBeEnabled()
  await page.locator('.appr-overlay').getByRole('button', { name: '取消' }).click()
  await expect(page.locator('.appr-overlay')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea'))).toBe(true)
})

// ---- 9. 手机宽度 ----
test.describe('390px 手机宽度', () => {
  test.use({ viewport: { width: 390, height: 800 } })

  async function noHorizontalOverflow(page: Page) {
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  }

  test('顶栏:标题不竖排,次要按钮收进「更多」,右侧不出屏', async ({ page }) => {
    await shell(page)
    await page.goto('/#/dashboard')
    const title = page.locator('.tb-title')
    await expect(title).toHaveText('总览')
    expect((await title.boundingBox())!.height).toBeLessThan(30)
    await expect(page.locator('.tb-sub')).toBeHidden()
    const right = await page.locator('.tb-right').boundingBox()
    expect(right!.x + right!.width).toBeLessThanOrEqual(390)
    await expect(page.locator('.pill.tb-sec').first()).toBeHidden()
    await page.getByTestId('topbar-more').click()
    const panel = page.getByTestId('topbar-more-panel')
    await expect(panel).toContainText('EN')
    await expect(panel).toContainText('深色')
    await noHorizontalOverflow(page)
  })

  test('审计页:筛选区换行,不溢出、按钮不竖排', async ({ page }) => {
    await shell(page)
    await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) => r.fulfill(envelope([])))
    await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => r.fulfill(envelope({ items: [], total: 0, groups: {} })))
    await page.goto('/#/audit')
    const bar = page.getByTestId('audit-fbar')
    await expect(bar).toBeVisible()
    const barBox = (await bar.boundingBox())!
    const kids = await bar.locator('.f, .fbtns > *').evaluateAll((els) => els.map((e) => e.getBoundingClientRect().right))
    for (const r of kids) expect(r).toBeLessThanOrEqual(barBox.x + barBox.width + 1)
    for (const b of await bar.locator('.fbtns button').all()) expect((await b.boundingBox())!.height).toBeLessThanOrEqual(40)
    await noHorizontalOverflow(page)
  })

  test('终端:实例树默认收起,展开是抽屉,选中实例后收起', async ({ page }) => {
    await shell(page)
    await page.goto('/#/terminal')
    await expect(page.getByTestId('tree-rail')).toBeVisible()
    await expect(page.locator('.tree')).toBeHidden()
    await page.getByTestId('tree-rail').click()
    const tree = page.locator('.tree')
    await expect(tree).toBeVisible()
    expect((await tree.boundingBox())!.width).toBeLessThan(390)
    await page.locator('.inst', { hasText: 'ORDER-PROD' }).click()
    await expect(page.locator('.tree')).toBeHidden() // 收起但不卸载:再展开时展开状态还在
    await expect(page.locator('.tab', { hasText: 'ORDER-PROD' })).toBeVisible()
    const term = (await page.locator('.term').boundingBox())!
    expect(term.width).toBeGreaterThan(250)
    await noHorizontalOverflow(page)
  })
})

// 2026-10-26 回归:抽屉断点原先是 1080px,半屏窗口(960px 左右)里树从一直可见变成每次都要点开。
// 768–1080 恢复成树嵌在页面里。
test.describe('960px 半屏', () => {
  test.use({ viewport: { width: 960, height: 800 } })
  test('终端:实例树嵌在页面里,不是抽屉', async ({ page }) => {
    await shell(page)
    await page.goto('/#/terminal')
    const tree = page.locator('.tree')
    await expect(tree).toBeVisible()
    await expect(tree).not.toHaveClass(/drawer/)
    await expect(page.getByTestId('tree-drawer-mask')).toHaveCount(0)
    await page.locator('.inst', { hasText: 'ORDER-PROD' }).click()
    await expect(tree).toBeVisible() // 选了实例树还在
    const term = (await page.locator('.term').boundingBox())!
    expect(term.width).toBeGreaterThan(500)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(960)
  })
})
