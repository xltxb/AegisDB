import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-11-23:发布流程「人工审批」阶段的审批单是否推审批魔方,单独一个开关
// approval.external.releaseEnabled,默认关;只在外部审批打开时出现。

async function open(page: Page, settings: Record<string, string>) {
  const saved: Record<string, unknown>[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/settings', async (r) => {
    if (r.request().method() === 'PUT') {
      saved.push(r.request().postDataJSON())
      return r.fulfill(envelope({ ok: true }))
    }
    return r.fulfill(envelope({ settings, secretsSet: {}, adminIpAllowlistForcedOff: false }))
  })
  await page.goto('/#/admin/settings?tab=approval')
  return saved
}

test('外部审批关着:不出现发布审批开关', async ({ page }) => {
  await open(page, { 'approval.external.enabled': 'false' })
  await expect(page.getByText('外部飞书审批(审批魔方)')).toBeVisible()
  await expect(page.getByTestId('ext-release-row')).toHaveCount(0)
})

test('外部审批开着:发布审批开关默认关,打开后保存带上 releaseEnabled=true', async ({ page }) => {
  const saved = await open(page, { 'approval.external.enabled': 'true' })
  const row = page.getByTestId('ext-release-row')
  await expect(row).toBeVisible()
  await expect(row).toContainText('发布审批也推送审批魔方')

  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  expect(saved[saved.length - 1]['approval.external.releaseEnabled']).toBe(false)

  await row.locator('button.vsw').click()
  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(1)
  expect(saved[saved.length - 1]['approval.external.releaseEnabled']).toBe(true)
})
