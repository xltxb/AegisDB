import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 上传脚本 / 导出的保存路径没配时:系统设置在管理后台,只有平台管理员进得去。
// 别人看到"去设置"点了会被守卫挡回原页,看着像没反应 —— 改成说清该找谁(2026-10 审查)。

const L2 = { ...ME, id: 3, name: 'Chen Hao', roleCode: 'l2', roleCodes: ['l2'], roleNames: ['DBA L2'], menus: { terminal: true } }

async function uploadWithoutPath(page: Page, me: typeof ME) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/scripts/uploads', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/scripts/upload', (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 42600, msg: '未配置上传脚本保存路径', data: null }) }))
  await page.goto('/#/uploads')
  await page.locator('input[type=file]').setInputFiles({ name: 'a.sql', mimeType: 'text/plain', buffer: Buffer.from('SELECT 1;') })
  await expect(page.locator('.notice')).toBeVisible()
}

test('非管理员:路径未配置时不给"去设置",说清找平台管理员', async ({ page }) => {
  await uploadWithoutPath(page, L2)
  await expect(page.getByRole('button', { name: '去设置' })).toHaveCount(0)
  await expect(page.getByTestId('path-ask-admin')).toHaveText('请联系平台管理员在系统设置里配置')
})

test('平台管理员:路径未配置时"去设置"直达管理后台的系统设置', async ({ page }) => {
  await uploadWithoutPath(page, ME)
  await expect(page.getByTestId('path-ask-admin')).toHaveCount(0)
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({ settings: {}, secretsSet: {} })))
  await page.getByRole('button', { name: '去设置' }).click()
  await expect(page).toHaveURL(/#\/admin\/settings/)
})
