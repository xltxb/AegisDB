import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 设置读不出来时,页面上的值只是默认值。这时候能保存,就会用默认值盖掉线上设置
// (包括 IP 白名单、强制 MFA)。读失败要说出来、不让存,重试读成功之后才放开。

test('设置读取失败:提示并禁止保存,重试成功后恢复', async ({ page }) => {
  await stubCommon(page)
  let fail = true
  let puts = 0
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') { puts++; return r.fulfill(envelope(null)) }
    if (fail) return r.fulfill({ status: 500, body: 'boom' })
    return r.fulfill(envelope({ settings: { 'security.requireMFA': true }, secretsSet: {} }))
  })
  await page.goto('/#/settings?tab=security')

  const banner = page.getByTestId('settings-load-failed')
  await expect(banner).toBeVisible({ timeout: 15000 })
  const saveBtn = page.locator('.savebar button')
  await expect(saveBtn).toBeDisabled()
  await saveBtn.click({ force: true })
  expect(puts).toBe(0)

  fail = false
  await banner.getByRole('button').click()
  await expect(banner).toHaveCount(0)
  await expect(saveBtn).toBeEnabled()
  await saveBtn.click()
  await expect.poll(() => puts).toBe(1)
})

test('设置读取中:保存按钮不可点', async ({ page }) => {
  await stubCommon(page)
  let release!: () => void
  const gate = new Promise<void>((res) => { release = res })
  await page.route('**/api/v1/settings', async (r) => {
    await gate
    await r.fulfill(envelope({ settings: {}, secretsSet: {} }))
  })
  await page.goto('/#/settings?tab=security')
  const saveBtn = page.locator('.savebar button')
  await expect(saveBtn).toBeDisabled({ timeout: 15000 })
  release()
  await expect(saveBtn).toBeEnabled()
  await expect(page.getByTestId('settings-load-failed')).toHaveCount(0)
})
