import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 网关策略里「审批单执行超时」与「命令执行超时」分开:终端是交互式的(默认 30 秒),审批单常是
// 大表上的 DDL(默认 1 小时)。钉住:单独一行、读得回已存的值、保存时带上它并夹到 1–86400。

test('审批单执行超时单独一行,保存时夹到 1–86400', async ({ page }) => {
  const saved: Record<string, unknown>[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') {
      return r.fulfill(envelope({ settings: { 'gateway.execTimeout': '30', 'gateway.approvalExecTimeout': '7200' }, secretsSet: {} }))
    }
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({}))
  })
  await page.goto('/#/settings')
  const input = page.getByTestId('set-approval-timeout')
  await expect(input).toHaveValue('7200')
  await expect(page.getByText('审批单执行超时')).toBeVisible()

  await input.fill('999999') // 超过上限
  await page.locator('button', { hasText: '保存' }).first().click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  expect(saved[saved.length - 1]).toMatchObject({ 'gateway.approvalExecTimeout': 86400, 'gateway.execTimeout': 30 })
})
