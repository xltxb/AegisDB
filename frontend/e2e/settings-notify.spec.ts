import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// issue #129:「邮件通知」「拦截即时通知」两个开关没有任何后端逻辑读取,拿掉;飞书那一组照旧。
test('通知设置只剩飞书卡片,保存不带没有实现的开关', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') { saved.push(r.request().postDataJSON()); return r.fulfill(envelope({ ok: true })) }
    return r.fulfill(envelope({ settings: { 'notify.email': 'true', 'notify.push': 'true' }, secretsSet: {} }))
  })
  await page.goto('/#/admin/settings?tab=notify')
  await expect(page.getByText('事件通知与值班推送')).toHaveCount(0)
  await expect(page.getByPlaceholder('https://gw.corp.io')).toBeVisible()
  await expect(page.getByText('邮件通知')).toHaveCount(0)
  await expect(page.getByText('拦截即时通知')).toHaveCount(0)

  await page.getByRole('button', { name: '保存设置' }).click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  const all = Object.assign({}, ...saved)
  expect(all).not.toHaveProperty('notify.email')
  expect(all).not.toHaveProperty('notify.push')
  expect(all['notify.lark']).toBe(true)

  // 关掉飞书后,这张卡片只剩一行
  const row = page.locator('.srow:visible')
  await row.locator('button.vsw').click()
  await expect(page.getByPlaceholder('https://gw.corp.io')).toBeHidden()
  await expect(row).toHaveCount(1)
  await expect(row).toHaveClass(/last/)
})
