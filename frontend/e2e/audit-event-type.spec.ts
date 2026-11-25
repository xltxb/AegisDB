import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 审计页上的**事件类型**与**增删改查**。
//
// 同一条命令在审计里会留下不止一行:被拦下来那一次,和审批那一次。两行的命令文本一模
// 一样,而 (风险, 结果) 也可能完全相同 —— 所以类型是**存下来的一列**,不是算出来的。
//
// 钉四条:
//   1. 每一行画出自己的类型;0039 之前写下的行这一列是空,界面如实显示一个短横,
//      **不替它猜一个** —— 在一张哈希链审计表上猜等于造证据;
//   2. 筛选发到**服务端**(?event=),不是在当前页里过滤;多选逗号分隔;
//   3. 条件改了要按「查询」才生效 —— 按之前列表还是上一次的结果,而不是一个改到一半
//      的条件拼出来的中间态;
//   4. 导出链接跟着**列表此刻真的按着的**那组条件走。
//
// 2026-09 页面重构之后,筛选从"改一格就发一次请求"改成了 draft + 查询,所以下面每次
// 改完条件都要按一次按钮 —— 这正是要钉住的行为之一。

const ROWS = [
  { id: 1, command: 'DROP TABLE orders_2024_q3;', risk: 'high', result: 'pending', eventType: 'intercept', dataAction: 'delete' },
  { id: 2, command: 'DROP TABLE orders_2024_q3;', risk: 'high', result: 'pending', eventType: 'approve', dataAction: 'delete' },
  { id: 3, command: 'SELECT 1', risk: 'low', result: 'executed', eventType: 'exec', dataAction: 'read' },
  { id: 4, command: 'login', risk: 'low', result: 'executed', eventType: 'login', dataAction: 'other' },
  // 0039 / 0040 之前的老行:两列都空。
  { id: 5, command: 'ALTER TABLE orders ADD COLUMN memo varchar(64);', risk: 'mid', result: 'executed', eventType: '', dataAction: '' },
].map((r) => ({
  occurredAt: '2026-09-15T08:00:00Z', actor: 'Lin Wei', instance: 'prod-order-01',
  database: 'orders', approvalNo: '', hash: 'deadbeef', clientIp: '10.0.0.9', ...r,
}))

async function openAudit(page: Page) {
  const urls: string[] = []
  await stubCommon(page)
  // 实例下拉的清单。不桩的话这一条会打到真后端去。
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) =>
    r.fulfill(envelope(['prod-order-01'])))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    const u = new URL(r.request().url())
    const want = u.searchParams.get('event') ?? ''
    const act = u.searchParams.get('action') ?? ''
    urls.push(want + '|' + act)
    // 多选是逗号分隔的一串,服务端按 IN 筛 —— 这里照着同一条语义过滤。
    const evs = want ? want.split(',') : []
    const acts = act ? act.split(',') : []
    let items = evs.length ? ROWS.filter((x) => evs.includes(x.eventType)) : ROWS
    if (acts.length) items = items.filter((x) => acts.includes(x.dataAction))
    return r.fulfill(envelope({ items, total: items.length, page: 1, pageSize: 50 }))
  })
  await page.goto('/#/admin/audit')
  await expect(page.locator('.tr').first()).toBeVisible()
  return urls
}

const eventSelect = (page: Page) => page.getByTestId('audit-event-filter')
const actionSelect = (page: Page) => page.getByTestId('audit-action-filter')
const search = (page: Page) => page.getByTestId('audit-search')

/** 在一个多选下拉里勾中某一项(勾完菜单不关,多选本来就是接着挑的)。 */
async function pick(sel: ReturnType<typeof eventSelect>, label: string) {
  await sel.locator('.control').click()
  await sel.locator('.opt', { hasText: label }).first().click()
  await sel.locator('.control').click() // 收起,免得菜单盖住下面的按钮
}

test.describe('审计日志 · 事件类型与增删改查', () => {
  test('每行画出自己的类型;没记的那一行显示短横而不是猜一个', async ({ page }) => {
    await openAudit(page)
    const rows = page.locator('.tr')
    await expect(rows).toHaveCount(5)
    await expect(rows.nth(0).locator('.evbadge')).toHaveText('已拦截')
    await expect(rows.nth(1).locator('.evbadge')).toHaveText('审批')
    await expect(rows.nth(2).locator('.evbadge')).toHaveText('已执行')
    await expect(rows.nth(3).locator('.evbadge')).toHaveText('登录')
    // 前两行命令一模一样、风险与结果也一样,只有类型不同 —— 这正是它必须被存下来的理由。
    await expect(rows.nth(0)).toContainText('orders_2024_q3')
    await expect(rows.nth(1)).toContainText('orders_2024_q3')
    // 老行:短横,并且不带任何一类的颜色。
    await expect(rows.nth(4).locator('.evbadge')).toHaveText('—')
    await expect(rows.nth(4).locator('.evbadge')).toHaveClass(/ev-none/)
  })

  test('增删改查:每行画出自己的那一类,老行是短横', async ({ page }) => {
    await openAudit(page)
    const rows = page.locator('.tr')
    await expect(rows.nth(0).locator('.actbadge')).toHaveText('删')
    await expect(rows.nth(2).locator('.actbadge')).toHaveText('查')
    // 登录不是"查" —— 原来那段分类正是这么错的。
    await expect(rows.nth(3).locator('.actbadge')).toHaveText('非数据')
    await expect(rows.nth(4).locator('.actbadge')).toHaveText('—')
    await expect(rows.nth(4).locator('.actbadge')).toHaveClass(/act-none/)
  })

  test('每行带上是谁、从哪台机器', async ({ page }) => {
    await openAudit(page)
    // "谁"和"从哪台机器"在同一格里 —— 它们是一句话的两半。
    await expect(page.locator('.tr').first().locator('.who')).toContainText('Lin Wei')
    await expect(page.locator('.tr').first().locator('.who')).toContainText('10.0.0.9')
  })

  test('选一个类型,按查询才发到服务端', async ({ page }) => {
    const urls = await openAudit(page)
    expect(urls[0]).toBe('|') // 首次加载不带类型

    await pick(eventSelect(page), '已拦截')
    // 还没按查询:请求没有新的一条,列表也还是全部五行。
    expect(urls[urls.length - 1]).toBe('|')
    await expect(page.locator('.tr')).toHaveCount(5)
    await expect(page.locator('.dirty')).toBeVisible()

    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('intercept|')
    await expect(page.locator('.tr')).toHaveCount(1)
    await expect(page.locator('.tr')).toContainText('orders_2024_q3')
    await expect(page.locator('.dirty')).toBeHidden()

    // 重置:请求里的 event 要清空,不是留着上一次的。
    await page.getByRole('button', { name: /重置/ }).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('|')
    await expect(page.locator('.tr')).toHaveCount(5)
  })

  test('多选:两类一起筛,逗号分隔发出去', async ({ page }) => {
    const urls = await openAudit(page)
    await pick(eventSelect(page), '已拦截')
    await pick(eventSelect(page), '登录')
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('intercept,login|')
    // 被拦下的那条 + 登录那条
    await expect(page.locator('.tr')).toHaveCount(2)
  })

  test('两个筛选正交:同时选是取交集,不是谁盖掉谁', async ({ page }) => {
    const urls = await openAudit(page)
    await pick(actionSelect(page), '删')
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('|delete')
    await expect(page.locator('.tr')).toHaveCount(2) // 拦截那条 + 审批那条

    // 再叠一个事件类型:两个条件一起发出去,结果只剩一条。
    await pick(eventSelect(page), '已拦截')
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('intercept|delete')
    await expect(page.locator('.tr')).toHaveCount(1)
  })

  test('导出链接带上同一组筛选条件', async ({ page }) => {
    await openAudit(page)
    await pick(eventSelect(page), '登录')
    await search(page).click()
    await expect(page.locator('.tr')).toHaveCount(1)

    // 导出走的是一个带查询串的 URL。拦下它,看筛选条件有没有跟过去 —— 两边不一致
    // 会导出一份人没在看的东西。
    let exportURL = ''
    await page.route((u) => u.pathname.endsWith('/api/v1/audit/export'), (r) => {
      exportURL = r.request().url()
      return r.fulfill({ status: 200, contentType: 'text/csv', body: 'time,actor\n' })
    })
    await page.getByRole('button', { name: /导出/ }).click()
    await expect.poll(() => exportURL).toContain('event=login')
  })

  test('详情抽屉:完整语句 + 这一行的上下文', async ({ page }) => {
    await openAudit(page)
    await page.locator('.tr').first().click()
    const drawer = page.locator('.drawer')
    await expect(drawer).toBeVisible()
    await expect(drawer.locator('.dcmd')).toContainText('DROP TABLE orders_2024_q3')
    await expect(drawer).toContainText('10.0.0.9')
    await expect(drawer).toContainText('deadbeef') // 链式校验值
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
  })
})
