import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// L-FE1(2026-10 安全审计):数据查询的 SQL 草稿存在浏览器里,主动登出后还在 —— 共用电脑时
// 下一个人打开开发者工具就能读到。登出时清掉;界面偏好(语言、主题)不动。
test('主动登出清掉查询草稿,保留界面偏好', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/dashboard**', (r) => r.fulfill(envelope({})))
  await page.route('**/api/v1/auth/logout', (r) => r.fulfill(envelope({ ok: true })))
  await page.addInitScript(() => {
    if (sessionStorage.getItem('seeded')) return
    sessionStorage.setItem('seeded', '1')
    localStorage.setItem('aegisdb_query_tabs_1', JSON.stringify({ active: 1, tabs: [{ id: 1, title: 'q', grantId: 0, sql: 'SELECT secret FROM t' }] }))
    localStorage.setItem('aegisdb_lang', 'zh')
    localStorage.setItem('aegisdb_query_saved_1', JSON.stringify([{ sql: 'SELECT 1', grantId: 1, db: 'x', at: 1 }]))
  })
  await page.goto('/#/dashboard')
  await page.locator('.avatar').click()
  await page.locator('.um-logout').click()
  await expect(page).toHaveURL(/#\/login/)
  expect(await page.evaluate(() => localStorage.getItem('aegisdb_query_tabs_1'))).toBeNull()
  expect(await page.evaluate(() => localStorage.getItem('aegisdb_lang'))).toBe('zh')
  // 收藏的常用查询是人主动存的,不跟着草稿清(2026-10-26)
  expect(await page.evaluate(() => localStorage.getItem('aegisdb_query_saved_1'))).not.toBeNull()
})
