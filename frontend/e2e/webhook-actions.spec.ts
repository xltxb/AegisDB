import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// Webhook 的「推送事件」= 审计日志的**增删改查**。
//
// 按这一维而不是按事件类型:接 SIEM 的人想说的通常是"所有删除都推给我",而被拦下来的 DROP
// 和执行掉的 DROP 是两个事件类型、同一件事 —— 按类型订阅答不了那句话。
//
// 钉三条:清单由服务端下发并画满;已存的订阅原样显示(不被界面悄悄滤掉);按一下保存,别的
// 订阅不会跟着消失。

const EVENT_TYPES = ['create', 'delete', 'update', 'read', 'other']

async function openWebhookPanel(page: Page, events: string) {
  const saved: string[] = []
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/settings'), (r) => {
    if (r.request().method() !== 'GET') return r.fallback()
    return r.fulfill(envelope({
      settings: {},
      webhook: { id: 1, endpoint: 'http://127.0.0.1:18099/ingest', secret: '', events, retryMax: 3, enabled: true },
      secretsSet: {},
      webhookHasSecret: true,
      webhookActions: EVENT_TYPES,
    }))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/settings/webhook'), (r) => {
    saved.push(String(r.request().postDataJSON()?.events ?? ''))
    return r.fulfill(envelope({ ok: true }))
  })
  await page.route('**/api/v1/settings/webhook/deliveries', (r) => r.fulfill(envelope([])))
  await page.goto('/#/settings')
  await page.locator('.tab', { hasText: 'Webhook' }).click()
  await page.getByTestId('wh-events').waitFor()
  return saved
}

test.describe('设置 · Webhook 推送事件(按增删改查订阅)', () => {
  test('五类全画出来,而且和审计日志的增删改查用同一组说法', async ({ page }) => {
    await openWebhookPanel(page, 'delete')
    const chips = page.getByTestId('wh-events').locator('.ev')
    await expect(chips).toHaveCount(5)
    // 与审计页「增删改查」列一字不差 —— 勾的那一枚和那一列里显示的要一眼对得上。
    await expect(chips.nth(0)).toHaveText('增')
    await expect(chips.nth(1)).toContainText('删')
    await expect(chips.nth(2)).toHaveText('改')
    await expect(chips.nth(3)).toHaveText('查')
    await expect(chips.nth(4)).toHaveText('非数据')
    await expect(page.getByText('与审计日志的「增删改查」一一对应')).toBeVisible()
  })

  test('已存的订阅照原样显示,不被界面悄悄滤掉', async ({ page }) => {
    await openWebhookPanel(page, 'create,delete')
    const chips = page.getByTestId('wh-events').locator('.ev')
    // 勾上的那两枚带对勾(组件用 <Check> 画),没勾的是虚线框。
    await expect(chips.nth(0).locator('svg')).toHaveCount(1)
    await expect(chips.nth(1).locator('svg')).toHaveCount(1)
    await expect(chips.nth(2)).toHaveClass(/off/)
    await expect(chips.nth(3)).toHaveClass(/off/)
    await expect(chips.nth(4)).toHaveClass(/off/)
  })

  test('按一下保存,别的订阅不会跟着消失', async ({ page }) => {
    const saved = await openWebhookPanel(page, 'create,delete')
    // 只点「查」。界面若在加载时过滤过已存的订阅,这一下发出去的就只剩「查」——
    // 两份别人配过的订阅凭空没了。
    await page.getByTestId('wh-events').locator('.ev').nth(3).click()
    await expect.poll(() => saved.length).toBeGreaterThan(0)
    const sent = saved[saved.length - 1].split(',').filter(Boolean).sort()
    expect(sent).toEqual(['create', 'delete', 'read'])
  })
})
