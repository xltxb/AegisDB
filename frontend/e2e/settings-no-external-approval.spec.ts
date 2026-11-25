import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 外部飞书审批(审批魔方)对接已移除(#137):设置 → 审批页不再出现它的开关与表单,
// 保存时也不再带任何 approval.external.* 键,即使库里还留着旧值。

async function open(page: Page, settings: Record<string, string>) {
  const saved: Record<string, unknown>[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/settings', async (r) => {
    if (r.request().method() === 'PUT') {
      saved.push(r.request().postDataJSON())
      return r.fulfill(envelope({ ok: true }))
    }
    return r.fulfill(envelope({ settings, secretsSet: { 'approval.external.token': true }, adminIpAllowlistForcedOff: false }))
  })
  await page.goto('/#/admin/settings?tab=approval')
  return saved
}

test('审批页不再有外部飞书审批', async ({ page }) => {
  await open(page, { 'approval.external.enabled': 'true' })
  await expect(page.getByText('审批超时')).toBeVisible()
  await expect(page.getByText('外部飞书审批')).toHaveCount(0)
  await expect(page.getByTestId('ext-release-row')).toHaveCount(0)
})

test('库里留着 approval.external.enabled=true,保存的 PUT 体不含 approval.external.* 键', async ({ page }) => {
  const saved = await open(page, { 'approval.external.enabled': 'true', 'approval.external.baseURL': 'https://x.example' })
  await expect(page.getByText('审批超时')).toBeVisible()
  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  const keys = Object.keys(saved[saved.length - 1])
  expect(keys.filter((k) => k.startsWith('approval.external.'))).toEqual([])
})
