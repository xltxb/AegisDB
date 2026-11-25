import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// issue #131:「升级到负责人」开关没有任何逻辑读取,拿掉;「超时自动升级」是真的通知,说明写清楚通知谁。
test('审批设置:没有升级开关,超时处理写明通知谁,保存不带 approval.escalate', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') { saved.push(r.request().postDataJSON()); return r.fulfill(envelope({ ok: true })) }
    return r.fulfill(envelope({ settings: { 'approval.onTimeout': '"auto-escalate"', 'approval.escalate': 'true' }, secretsSet: {} }))
  })
  await page.goto('/#/admin/settings?tab=approval')
  const row = page.locator('.srow:visible', { hasText: '审批超时处理' })
  await expect(row).toContainText('超时自动升级')
  await expect(row).toContainText('通知默认审批人')
  await expect(page.getByText('升级到负责人')).toHaveCount(0)
  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  const all = Object.assign({}, ...saved)
  expect(all).not.toHaveProperty('approval.escalate')
  expect(all['approval.onTimeout']).toBe('auto-escalate')
})

// 升级通知在铃铛里:黄色时钟(不是成功也不是失败),英文界面能翻译。
test('升级通知按界面语言显示', async ({ page }) => {
  await stubCommon(page)
  await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 50 })))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope({
    unread: 1,
    items: [{ id: 1, type: 'approval-escalated', title: '审批超时未处理', titleRef: JSON.stringify({ code: 'm81da8912' }), body: '李雷 在 order-cluster 上发起的审批超过时限仍未处理，请跟进', bodyRef: JSON.stringify({ code: 'mf54408da', args: { 0: '李雷', 1: 'order-cluster' } }), refNo: 'AP-9', read: false, createdAt: '2026-11-25T10:00:00+08:00' }],
  })))
  await page.goto('/#/releases')
  await page.locator('.notif .bell').click()
  const item = page.locator('.pitem').first()
  await expect(item).toContainText('AP-9')
  await expect(item).toContainText('Approval overdue')
  await expect(item).toContainText('raised by 李雷 on order-cluster is overdue')
  await expect(item.locator('.pic')).toHaveClass(/warn/)
})

// issue #134:没有这项设置(或存着不认识的值)时后端按「保持等待」处理,界面要照实显示;
// 保存是整份提交的,显示成「自动升级」会把没人选过的值写进去。
for (const [name, stored] of [['没有', {}], ['不认识', { 'approval.onTimeout': '"explode"' }]] as const) {
  test(`审批超时处理:存的值${name}时显示并保存「保持等待」`, async ({ page }) => {
    const saved: any[] = []
    await stubCommon(page)
    await page.route('**/api/v1/settings', (r) => {
      if (r.request().method() === 'PUT') { saved.push(r.request().postDataJSON()); return r.fulfill(envelope({ ok: true })) }
      return r.fulfill(envelope({ settings: stored, secretsSet: {} }))
    })
    await page.goto('/#/admin/settings?tab=approval')
    const row = page.locator('.srow:visible', { hasText: '审批超时处理' })
    await expect(row.locator('.w180')).toContainText('保持等待')
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => saved.length).toBeGreaterThan(0)
    expect(Object.assign({}, ...saved)['approval.onTimeout']).toBe('keep-waiting')
  })
}
