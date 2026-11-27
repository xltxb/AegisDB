import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 审批列表上的**工单类型**。
//
// 四类工单混在一张列表里,而它们要人做的事完全不同:命令单批完要发起人自己去执行,
// 升级单归流水线,窗口单批准即生效,导出单由后台任务跑。更要紧的是后三类的「命令」
// 其实是一句描述,连表名都解析不出来 —— 那一格显示的是一个短横,不说类型的话,那一行
// 看起来只是一张内容古怪的命令单。
//
// 钉三条:
//   1. 标签只画给**不是命令单**的三类 —— 给占绝大多数的命令单每行挂一枚"命令单"是
//      纯噪音,而这三类补的正是命令栏缺的那条信息;
//   2. 标签按服务端送来的 type 画,不照着三个外键在前端再推一遍;
//   3. 筛选在**服务端**做:选了类型,请求里就带 type=,而不是在当前页里过滤 —— 列表是
//      分页的,只筛当前页会对一张躺在第三页的工单回答"没有"。

const ROWS = [
  { id: 1, apNo: 'AP-9101', type: 'cmd', command: 'DROP TABLE orders_2024_q3;', riskLevel: 'high' },
  { id: 2, apNo: 'AP-9102', type: 'release', releaseId: 77, command: 'REL-0087 升级单审批', riskLevel: 'mid' },
  { id: 3, apNo: 'AP-9103', type: 'window', windowId: 5, command: '开启执行窗口「双十一封板」', riskLevel: 'mid' },
  { id: 4, apNo: 'AP-9104', type: 'export', exportJobId: 9, command: 'EXPORT 含敏感字段(原值) orders', riskLevel: 'mid' },
].map((r) => ({
  connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders',
  keyword: '', initiatorId: 2, initiator: 'Chen Hao', reason: '回归', status: 'pending',
  createdAt: '2026-09-14T08:00:00Z', steps: [], canDecide: false, blockReason: '仅审批链成员可处理该工单',
  canExecute: false, canCancel: false, ...r,
}))

async function openPage(page: Page, rows = ROWS) {
  const urls: string[] = []
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => {
    const u = new URL(r.request().url())
    urls.push(u.searchParams.get('type') ?? '')
    const want = u.searchParams.get('type') || ''
    const items = want ? rows.filter((x) => x.type === want) : rows
    return r.fulfill(envelope({ items, total: items.length, pending: items.length, page: 1, pageSize: 20 }))
  })
  await page.goto('/#/approvals')
  await expect(page.locator('.tr').first()).toBeVisible()
  return urls
}

const typeSelect = (page: Page) => page.locator('.chead .vsel').nth(1)

test.describe('审批列表 · 工单类型', () => {
  test('三类非命令单各带一枚标签,命令单不带', async ({ page }) => {
    await openPage(page)
    const row = (no: string) => page.locator('.tr', { hasText: no })
    await expect(row('AP-9101').locator('.tybadge')).toHaveCount(0)
    await expect(row('AP-9102').locator('.tybadge')).toHaveText('升级单')
    await expect(row('AP-9103').locator('.tybadge')).toHaveText('窗口单')
    await expect(row('AP-9104').locator('.tybadge')).toHaveText('导出单')
  })

  // 批准之后 executed_at 永远是空的那几类(升级单、窗口单、导出单)不能显示成"待执行";
  // 只有命令单是。按服务端的 type 判,不在前端拿外键拼(#182:导出单曾被漏掉)。
  test('已批准的非命令单不显示"待执行",命令单显示', async ({ page }) => {
    await openPage(page, ROWS.map((r) => ({ ...r, status: 'approved' })))
    const st = (no: string) => page.locator('.tr', { hasText: no }).locator('.stbadge')
    await expect(st('AP-9101')).toHaveText('待执行')
    for (const no of ['AP-9102', 'AP-9103', 'AP-9104']) await expect(st(no)).not.toHaveText('待执行')
  })

  test('选一个类型,筛选发到服务端而不是在当前页里过滤', async ({ page }) => {
    const urls = await openPage(page)
    expect(urls[0]).toBe('') // 首次加载不带类型

    await typeSelect(page).locator('.control').click()
    await typeSelect(page).locator('.opt', { hasText: '升级单' }).click()

    await expect.poll(() => urls[urls.length - 1]).toBe('release')
    await expect(page.locator('.tr')).toHaveCount(1)
    await expect(page.locator('.tr')).toContainText('AP-9102')

    // 回到"全部类型":请求里的 type 要清空,不是留着上一次的。
    await typeSelect(page).locator('.control').click()
    await typeSelect(page).locator('.opt', { hasText: '全部类型' }).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('')
    await expect(page.locator('.tr')).toHaveCount(4)
  })

  // 筛选项从类型表生成,不再手写一份:在线变更单曾被漏掉(#188)。
  test('筛选列表包含全部类型,在线变更单也能筛', async ({ page }) => {
    const urls = await openPage(page)
    await typeSelect(page).locator('.control').click()
    await expect(typeSelect(page).locator('.opt')).toHaveText(['全部类型', '命令单', '升级单', '窗口单', '导出单', '在线变更', '权限申请'])
    await typeSelect(page).locator('.opt', { hasText: '在线变更' }).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('osc')
  })
})
