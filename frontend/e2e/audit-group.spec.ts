import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 审计日志按审批单合并:同一张审批单的操作(提交被拦截 → 审批 → 执行)在列表里合成一条,
// 标出共几条;点开详情有这张单的全部操作(时间线),点其中一条看那一条。开关关掉回到逐条。

const base = { actor: 'Lin Wei', instance: 'prod-order-01', database: 'orders', hash: 'deadbeef', clientIp: '10.0.0.9', dataAction: 'delete', risk: 'high' }
const TRAIL = [
  { id: 11, occurredAt: '2026-09-28T01:00:00Z', command: 'DROP TABLE t_old;', result: 'pending', eventType: 'intercept', approvalNo: 'AP-9', operator: '' },
  { id: 12, occurredAt: '2026-09-28T01:05:00Z', command: 'DROP TABLE t_old;', result: 'pending', eventType: 'approve', approvalNo: 'AP-9', operator: '张伟' }, // 后端:通过 = 已批准、尚未执行
  { id: 13, occurredAt: '2026-09-28T01:10:00Z', command: 'DROP TABLE t_old;', result: 'executed', eventType: 'exec', approvalNo: 'AP-9', operator: 'Lin Wei' },
].map((r) => ({ ...base, ...r }))
const PLAIN = { ...base, id: 20, occurredAt: '2026-09-28T02:00:00Z', command: 'SELECT 1', result: 'executed', eventType: 'exec', approvalNo: '', dataAction: 'read', risk: 'low' }

async function openAudit(page: Page) {
  const listQs: string[] = []
  const trailReqs: string[] = []
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) => r.fulfill(envelope(['prod-order-01'])))
  await page.route((u) => u.pathname.includes('/api/v1/audit/approval/'), (r) => {
    trailReqs.push(new URL(r.request().url()).pathname.split('/').pop() || '')
    return r.fulfill(envelope(TRAIL))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    const u = new URL(r.request().url())
    listQs.push(u.searchParams.get('group') ?? '')
    const grouped = u.searchParams.get('group') === 'approval'
    const items = grouped ? [PLAIN, TRAIL[2]] : [PLAIN, ...[...TRAIL].reverse()]
    return r.fulfill(envelope({ items, total: items.length, page: 1, pageSize: 50, ...(grouped ? { groups: { 'AP-9': 3 } } : {}) }))
  })
  await page.goto('/#/admin/audit')
  await expect(page.locator('.tr').first()).toBeVisible()
  return { listQs, trailReqs }
}

test('默认按审批单合并:同一张单一条,标出共几条', async ({ page }) => {
  const { listQs } = await openAudit(page)
  expect(listQs[0]).toBe('approval')
  await expect(page.locator('.tr:not(.sk)')).toHaveCount(2)
  await expect(page.getByTestId('audit-group-count')).toHaveText(/3/)
})

test('点开合并的那一条:时间线按先后列出全部操作,点其中一条切过去', async ({ page }) => {
  const { trailReqs } = await openAudit(page)
  await page.locator('.tr', { hasText: 'DROP TABLE t_old' }).click()
  await expect(page.getByTestId('audit-trail-item')).toHaveCount(3)
  expect(trailReqs).toEqual(['AP-9'])
  const items = page.getByTestId('audit-trail-item')
  await expect(items.nth(0)).toBeVisible()
  // 当前打开的是最新的一步(执行),时间线上它是高亮的那一条
  await expect(items.nth(2)).toHaveClass(/on/)
  // 同一个 pending:拦截那一步是"审批中",审批那一步是"已通过" —— 不是又回到审批中
  await expect(items.nth(0)).toContainText('审批中')
  await expect(items.nth(1)).toContainText('已通过')
  await expect(items.nth(2)).toContainText('已执行')
  await items.nth(1).click() // 切到审批那一步
  await expect(items.nth(1)).toHaveClass(/on/)
  await expect(page.locator('.drawer')).toContainText('张伟') // 审批人(operator)
})

test('没有审批单的记录点开:没有时间线,不去查', async ({ page }) => {
  const { trailReqs } = await openAudit(page)
  await page.locator('.tr', { hasText: 'SELECT 1' }).click()
  await expect(page.locator('.drawer')).toBeVisible()
  await expect(page.getByTestId('audit-trail-item')).toHaveCount(0)
  expect(trailReqs).toEqual([])
})

test('关掉「合并审批单」:回到逐条', async ({ page }) => {
  const { listQs } = await openAudit(page)
  await page.getByTestId('audit-group-toggle').click()
  await expect.poll(() => listQs.at(-1)).toBe('')
  await expect(page.locator('.tr:not(.sk)')).toHaveCount(4)
  await expect(page.getByTestId('audit-group-count')).toHaveCount(0)
})
