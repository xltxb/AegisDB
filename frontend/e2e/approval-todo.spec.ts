import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 右上角的"待办"。钉四件事:
//
//   1. 有待办时它必须**显眼**:琥珀色 + 数量,标签页标题也挂上数量 —— 人切去别的
//      标签页时,那是唯一还看得见的提示。
//   2. 没待办时它安静下来:不上色、不脉冲、说"无待办"。
//   3. 点开是一张面板,每行说清"等你审批"还是"等你执行",点一行直接进那张单。
//   4. 既没有审批菜单也没有终端菜单的人,压根不显示它 —— 他没有任何能处理的动作。

const TODO = {
  decide: 1, execute: 1, confirm: 1,
  items: [
    { id: 11, apNo: 'AP-9001', kind: 'decide', initiator: 'Chen Hao', instance: 'prod-order-01', database: 'orders', riskLevel: 'high', command: 'DROP TABLE orders_2024_q3;', createdAt: '2026-09-14T08:00:00Z' },
    { id: 12, apNo: 'AP-9002', kind: 'execute', initiator: 'Lin Wei', instance: 'prod-order-01', database: 'orders', riskLevel: 'mid', command: 'ALTER TABLE orders DROP COLUMN legacy_ref;', createdAt: '2026-09-14T08:10:00Z' },
    // 第三类:发布单停在人工闸上。id 是**发布单** id,apNo 是发布单号,stage 是闸的类型。
    { id: 77, apNo: 'REL-0087', kind: 'confirm', initiator: 'Lin Wei', instance: 'prod-order-01', database: 'orders', riskLevel: 'mid', command: 'ALTER TABLE orders ADD COLUMN memo varchar(64);', stage: 'manual', createdAt: '2026-09-14T08:20:00Z' },
  ],
}
const NONE = { decide: 0, execute: 0, confirm: 0, items: [] }

async function open(page: Page, todo: unknown, me = ME, path = '/#/dashboard') {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  // 后注册的先匹配:盖掉 stubCommon 里那个空的。
  await page.route('**/api/v1/approvals/todo', (r) => r.fulfill(envelope(todo)))
  // 审批页自己拉的列表。glob 里的 ? 是单字符通配,所以按 pathname 匹配。
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [], total: 0, pending: 0, page: 1, pageSize: 20 })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.goto(path)
}

test.describe('右上角待办', () => {
  test('有待办:琥珀色 + 数量,标签页标题也带上', async ({ page }) => {
    await open(page, TODO)
    const pill = page.getByTestId('todo-pill')
    await expect(pill).toHaveClass(/hot/)
    await expect(pill).toContainText('待办')
    await expect(pill.locator('.num')).toHaveText('3')
    await expect(page).toHaveTitle(/^\(3\) /)
  })

  test('没待办:安静的"无待办",标题不带数量', async ({ page }) => {
    await open(page, NONE)
    const pill = page.getByTestId('todo-pill')
    await expect(pill).toContainText('无待办')
    await expect(pill).not.toHaveClass(/hot/)
    await expect(page).not.toHaveTitle(/^\(/)
    await pill.click()
    await expect(page.getByTestId('todo-panel')).toContainText('没有在等你处理的工单')
  })

  test('面板分清"等你审批"和"等你执行",点一行进那张单', async ({ page }) => {
    await open(page, TODO)
    await page.getByTestId('todo-pill').click()
    const panel = page.getByTestId('todo-panel')
    await expect(panel).toContainText('待审批 1')
    await expect(panel).toContainText('待执行 1')
    await expect(panel).toContainText('待确认 1')
    const rows = panel.locator('.titem')
    await expect(rows).toHaveCount(3)
    await expect(rows.nth(0).locator('.tkind')).toHaveText('等你审批')
    await expect(rows.nth(0)).toContainText('AP-9001')
    await expect(rows.nth(0)).toContainText('DROP TABLE orders_2024_q3;')
    await expect(rows.nth(1).locator('.tkind')).toHaveText('等你执行')
    // 第三类要说清停在**哪一道闸**上。后端送的是类型(manual),界面按读者的语言渲染它 ——
    // 阶段名是派生出来的规范中文,直接摆上去英文界面就会夹一句中文。
    await expect(rows.nth(2).locator('.tkind')).toHaveText('等你确认')
    await expect(rows.nth(2)).toContainText('REL-0087')
    await expect(rows.nth(2).locator('.tstage')).toHaveText('人工确认')
    await rows.nth(0).click()
    await expect(page).toHaveURL(/#\/approvals\?ap=AP-9001/)
    await expect(panel).toHaveCount(0)
  })

  test('待确认那一条点进去的是**发布单**,而且它不在"我的"列表里也照样摊开', async ({ page }) => {
    await open(page, TODO)
    // 确认人常常不是发起人,而发布页默认范围是"我的" —— 列表里没有这一张。
    await page.route((u) => u.pathname.endsWith('/api/v1/releases'), (r) =>
      r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 20 })))
    await page.route((u) => u.pathname.endsWith('/api/v1/releases/77'), (r) =>
      r.fulfill(envelope({
        id: 77, relNo: 'REL-0087', title: '订单表加备注字段', pipelineId: 1, pipelineName: '标准发布',
        connectionId: 1, instance: 'prod-order-01', database: 'orders', env: 'prod', tierCode: 'prod',
        engine: 'MySQL', sql: 'ALTER TABLE orders ADD COLUMN memo varchar(64);', reason: '产品需求',
        creatorId: 1, creator: 'Lin Wei', source: 'console', status: 'waiting', risk: 'mid', error: '',
        createdAt: '2026-09-14T08:20:00Z', stages: [],
      })))
    await page.getByTestId('todo-pill').click()
    await page.getByTestId('todo-panel').locator('.titem').nth(2).click()
    await expect(page).toHaveURL(/#\/releases\?rel=77/)
    // 列表是空的,详情卡仍然摊开了那一张 —— 在已加载的列表里找的话,这里会是一片空白。
    await expect(page.locator('.detail .dt')).toContainText('REL-0087')
  })

  test('没有审批也没有终端菜单的人看不到它', async ({ page }) => {
    const viewer = { ...ME, menus: { db: true } }
    await open(page, TODO, viewer, '/#/connections')
    await expect(page.locator('.topbar')).toBeVisible()
    await expect(page.getByTestId('todo-pill')).toHaveCount(0)
  })
})
