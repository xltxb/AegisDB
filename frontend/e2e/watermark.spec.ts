import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 水印。它是给截图看的,所以这里钉的是三件事:
//
//   1. 登录后每一页都有它,而且**内容跟着登录者变** —— 两个不同的用户画出来的不是同一张图。
//      一句谁截都一样的口号追不回任何东西。
//   2. 它不挡任何点击:pointer-events 必须是 none,底下的导航照常能点。
//   3. 随手删掉它,它会自己回来。

async function open(page: Page, me = ME) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.goto('/#/dashboard')
  await page.locator('.watermark').waitFor({ state: 'attached', timeout: 15000 })
  // 画布是挂载后才画的,等到 background-image 落下来。
  await expect.poll(() => page.locator('.watermark').evaluate((el) => getComputedStyle(el).backgroundImage)).toContain('data:image/png')
}

const bg = (page: Page) => page.locator('.watermark').evaluate((el) => getComputedStyle(el).backgroundImage)

test.describe('水印', () => {
  test('登录后就在,内容随登录者变化', async ({ page, browser }) => {
    await open(page)
    const a = await bg(page)
    expect(a).toContain('data:image/png')

    const other = await browser.newPage()
    await open(other, { ...ME, id: 2, name: 'Zhao Lei', email: 'zhaolei@aegisdb.io', initials: 'ZL' })
    const b = await bg(other)
    await other.close()
    expect(b).toContain('data:image/png')
    // 同一天、同一主题,只有身份不同 —— 图必须不同。相同就说明画的不是登录者。
    expect(b).not.toBe(a)
  })

  test('不挡点击:底下的导航照常能用', async ({ page }) => {
    await open(page)
    await expect(page.locator('.watermark')).toHaveCSS('pointer-events', 'none')
    await expect(page.locator('.watermark')).toHaveAttribute('aria-hidden', 'true')
    // 水印覆盖整个视口,导航项就在它底下 —— 点得动才算没挡。
    await page.locator('.rail-item', { hasText: '审批' }).click()
    await expect(page).toHaveURL(/#\/approvals/)
  })

  test('随手删掉会自己回来', async ({ page }) => {
    await open(page)
    await page.evaluate(() => document.querySelector('.watermark')?.remove())
    await expect(page.locator('.watermark')).toHaveCount(1)
    await expect(page.locator('.watermark')).toHaveCSS('pointer-events', 'none')
  })
})
