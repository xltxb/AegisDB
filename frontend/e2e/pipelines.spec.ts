import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 发布流程配置页 —— 重构成时间轴式的步骤编排之后,要守住的是**行为**,不是样式:
//
//   1. 列表卡片把阶段摘要标签化(人工审批 → 执行变更 → …),而不是一串折行的文字;
//   2. 步骤卡上的操作(上移/下移/复制/删除)和底部「+ 添加步骤」弹层真的改到了数据,
//      而且保存时按**当前顺序**发出去 —— 流程的全部意义就在顺序;
//   3. 校验阶段一直被埋着的 expect 现在露出来了,而且写进了配置;
//   4. 只读角色看不到任何编辑控件。

const PIPELINES = [
  {
    id: 1, name: '标准发布流程', description: '', tierCode: '', enabled: true, isDefault: true,
    stages: [
      { id: 11, name: '人工审批', type: 'approve', config: '', onFailure: 'abort' },
      { id: 12, name: '执行变更', type: 'execute', config: '', onFailure: 'abort' },
      { id: 13, name: '结果通知', type: 'notify', config: '', onFailure: 'continue' },
    ],
  },
  {
    id: 2, name: '开发自主发布', description: '开发环境直接执行', tierCode: 'dev', enabled: true, isDefault: false,
    stages: [{ id: 21, name: '执行变更', type: 'execute', config: '', onFailure: 'abort' }],
  },
]

const ROLES = [
  { id: 1, code: 'admin', name: '平台管理员', layer: 'L0', icon: 'crown', count: 1 },
  { id: 2, code: 'dba_owner', name: 'DBA 负责人', layer: 'L1', icon: 'shield', count: 2 },
]

async function openPipelines(page: Page, opts: { readonly?: boolean; tiers?: unknown } = {}) {
  await stubCommon(page, { tiers: opts.tiers })
  if (opts.readonly) {
    await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({
      id: 9, name: 'Zhao Lei', email: 'zhaolei@aegisdb.io', initials: 'ZL',
      roleId: 3, roleCode: 'ro', roleName: '研发只读', layer: 'L4',
      roleIds: [3], roleNames: ['研发只读'], roleCodes: ['ro'], canApprove: false, mfaEnabled: false,
      menus: { terminal: true, pipeline: true, pipelines: true, releases: true }, capabilities: {},
    })))
  }
  // 审批节点未配角色时走全局默认链;共享桩里那条链是空的,而这一页要验的正是
  // "会找谁"被解析成人名胶囊 —— 给链上放一个人。
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [{ id: 1, name: 'Lin Wei', initials: 'LW', dept: 'DBA' }] })))
  await page.route('**/api/v1/pipelines', (r) => r.fulfill(envelope(PIPELINES)))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope(ROLES)))
  await page.route('**/api/v1/roles/*', (r) => r.fulfill(envelope({ ...ROLES[1], members: [{ name: 'Lin Wei', kind: 'human' }], memberIds: [1], tags: [] })))
  await page.goto('/#/pipelines')
  if (opts.readonly) return // 非管理员进不了管理后台,不会有列表
  await expect(page.locator('.pitem').first()).toBeVisible()
}

const cards = (page: Page) => page.locator('.timeline .step')
const cardName = (page: Page, i: number) => cards(page).nth(i).locator('.name')

test.describe('发布流程配置', () => {
  test('列表卡片把阶段摘要标签化,并带默认/分层标记', async ({ page }) => {
    await openPipelines(page)

    const first = page.locator('.pitem').first()
    await expect(first.locator('.stag')).toHaveCount(3)
    await expect(first.locator('.stag').nth(0)).toContainText('人工审批')
    await expect(first.locator('.stag').nth(2)).toContainText('结果通知')
    await expect(first.locator('.badge')).toContainText('默认')
    await expect(page.locator('.pitem').nth(1).locator('.tier')).toHaveText('DEV')
  })

  test('步骤画成时间轴:序号环 + 类型徽标,第一条默认选中', async ({ page }) => {
    await openPipelines(page)

    await expect(page.locator('.pitem').first()).toHaveClass(/on/)
    await expect(cards(page)).toHaveCount(3)
    await expect(cards(page).nth(0).locator('.ring')).toHaveText('1')
    await expect(cards(page).nth(2).locator('.ring')).toHaveText('3')
    await expect(cardName(page, 0)).toHaveText('人工审批')
    // 徽标显示失败策略:桩数据里通知那一步是 continue,其余是 abort。
    await expect(cards(page).nth(0).locator('.badge')).toHaveText('失败中止')
    await expect(cards(page).nth(2).locator('.badge')).toHaveText('失败继续')
    // 审批人解析出来的人名以胶囊显示。
    await expect(cards(page).nth(0).locator('.pill').first()).toBeVisible()
  })

  test('底部「+ 添加步骤」弹层加一步,复制、上移、删除都改到数据,保存按当前顺序发出', async ({ page }) => {
    await openPipelines(page)

    // 添加:弹层里选「执行后校验」→ 追加到末尾
    await page.locator('.toolbar .add').click()
    await expect(page.locator('.toolbar .pop')).toBeVisible()
    await page.locator('.toolbar .card', { hasText: '执行后校验' }).click()
    await expect(cards(page)).toHaveCount(4)
    await expect(cardName(page, 3)).toHaveText('执行后校验')
    await expect(page.locator('.toolbar .pop')).toHaveCount(0) // 选完即收起

    // 校验阶段露出了 expect:选「结果为空」
    await cards(page).nth(3).locator('.si', { hasText: '结果为空' }).click()
    await expect(cards(page).nth(3).locator('.si.active')).toHaveText('结果为空')

    // 复制第 1 步 → 插在它下面
    await cards(page).nth(0).locator('.ab[title="复制这一步"]').click()
    await expect(cards(page)).toHaveCount(5)
    await expect(cardName(page, 1)).toHaveText('人工审批')

    // 把第 5 步(校验)上移一格
    await cards(page).nth(4).locator('.ab[title="上移"]').click()
    await expect(cardName(page, 3)).toHaveText('执行后校验')
    await expect(cardName(page, 4)).toHaveText('结果通知')

    // 删掉复制出来的那一步
    await cards(page).nth(1).locator('.ab[title="删除"]').click()
    await expect(cards(page)).toHaveCount(4)

    // 第一步不能再上移,最后一步不能再下移
    await expect(cards(page).nth(0).locator('.ab[title="上移"]')).toBeDisabled()
    await expect(cards(page).nth(3).locator('.ab[title="下移"]')).toBeDisabled()

    // 点徽标把第 1 步切成"失败继续"
    await cards(page).nth(0).locator('.badge').click()
    await expect(cards(page).nth(0).locator('.badge')).toHaveText('失败继续')

    // 保存:类型顺序必须就是屏幕上的顺序,校验阶段带着 expect,失败策略也跟着走
    const saved: any[] = []
    await page.route('**/api/v1/pipelines/*', (r) => {
      saved.push(r.request().postDataJSON())
      return r.fulfill(envelope({ ...PIPELINES[0] }))
    })
    await page.locator('.ehead .vbtn.v-primary').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].stages.map((s: any) => s.type)).toEqual(['approve', 'execute', 'verify', 'notify'])
    expect(JSON.parse(saved[0].stages[2].config).expect).toBe('empty')
    expect(saved[0].stages[0].onFailure).toBe('continue')
  })

  test('步骤卡可折叠,折叠后主体不再显示', async ({ page }) => {
    await openPipelines(page)
    const c = cards(page).nth(0)
    await expect(c.locator('.body')).toBeVisible()
    await c.locator('.fold').click()
    await expect(c.locator('.body')).toBeHidden()
    await expect(c).toHaveClass(/collapsed/)
  })

  // 流水线编排移进了管理后台(只有平台管理员):非管理员连页面都进不去,送回用户后台。
  test('非管理员进不了流水线编排,送回用户后台', async ({ page }) => {
    await openPipelines(page, { readonly: true })
    await expect(page).toHaveURL(/#\/dashboard/)
  })
})

// 2026-10-30:「自动执行」步骤只能挂在打开了「允许自动执行」的分层上。添加面板里这张卡片
// 一直摆着,不允许时置灰并说明原因;后端保存时同样会拒(见 release_auto_execute_test.go)。
test.describe('自动执行步骤', () => {
  const TIERS2 = [
    { code: 'prod', displayName: 'PROD', sortOrder: 0, requireMfa: true, dangerBanner: true, countsInPending: true,
      scanBaseline: true, strictNoWhere: true, maskSensitive: true, bizExportRestrict: true, autoExecute: false,
      connLayer: 'L1', defaultRole: 'dba_l2' },
    { code: 'dev', displayName: 'DEV', sortOrder: 4, requireMfa: false, dangerBanner: false, countsInPending: false,
      scanBaseline: false, strictNoWhere: false, maskSensitive: false, bizExportRestrict: false, autoExecute: true,
      connLayer: 'L4', defaultRole: 'developer' },
  ]

  test('适用全部分层的流程不能加自动执行;dev 流程可以加,并能保存出 auto_execute', async ({ page }) => {
    await openPipelines(page, { tiers: TIERS2 })

    // 第一条是「适用全部分层」:卡片置灰,悬停说明
    await page.locator('.toolbar .add').click()
    const auto = page.getByTestId('add-step-auto_execute')
    await expect(auto).toBeDisabled()
    await expect(auto).toHaveAttribute('title', /指定了适用分层/)
    await page.keyboard.press('Escape')

    // 第二条是 dev 流程:可以加,卡片名「自动执行」,没有确认人一栏
    await page.locator('.pitem').nth(1).click()
    await page.locator('.toolbar .add').click()
    await expect(auto).toBeEnabled()
    await auto.click()
    const last = cards(page).last()
    await expect(last.locator('.name')).toHaveText('自动执行')
    await expect(last.getByText('确认人')).toHaveCount(0)
    await expect(page.getByTestId('auto-exec-warn')).toHaveCount(0)

    // 把执行变更删掉(只留一个执行类阶段)后保存,发出去的是 auto_execute
    await cards(page).nth(0).locator('.ab[title="删除"]').click()
    let body: any = null
    await page.route('**/api/v1/pipelines/2', async (r) => {
      if (r.request().method() === 'PUT') body = r.request().postDataJSON()
      await r.fulfill(envelope({ ...PIPELINES[1] }))
    })
    await page.getByRole('button', { name: '保存' }).click()
    await expect.poll(() => body?.stages?.map((s: any) => s.type)).toEqual(['auto_execute'])
  })

  test('流程改挂到不允许自动执行的分层,自动执行卡片上直接提示', async ({ page }) => {
    const pls = [{ ...PIPELINES[1], stages: [{ id: 21, name: '自动执行', type: 'auto_execute', config: '', onFailure: 'abort' }] }]
    await stubCommon(page, { tiers: TIERS2.map((t) => (t.code === 'dev' ? { ...t, autoExecute: false } : t)) })
    await page.route('**/api/v1/pipelines', (r) => r.fulfill(envelope(pls)))
    await page.route('**/api/v1/roles', (r) => r.fulfill(envelope(ROLES)))
    await page.goto('/#/pipelines')
    await expect(page.getByTestId('auto-exec-warn')).toContainText('DEV')
  })
})
