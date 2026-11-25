import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 系统名称与 LOGO 可配置(.scratch/branding):
//   - 未登录的登录页、登录后的侧栏 / 浏览器标题 / favicon / 设置页「关于」都显示配置的值;
//   - 没配置时一切照旧(DP DB GATEWAY + 帆船图标 + 原 favicon);
//   - 管理员在「系统设置 → 品牌」里改,只发 brand.* 三项,存成了当前页面立刻换掉。

// 1x1 PNG
const PNG_B64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=='
const LOGO = `data:image/png;base64,${PNG_B64}`
const BRAND = { name: '星河数据网关', nameEn: 'Galaxy DB', logo: LOGO }

async function stubLoginPage(page: Page, branding: unknown) {
  await page.route((u) => u.pathname.startsWith('/api/'), (r) => r.fulfill({ status: 502, body: 'e2e: unstubbed endpoint' }))
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: false, loginUrl: '' })))
  await page.route('**/api/v1/auth/branding', (r) => r.fulfill(envelope(branding)))
}

const favicon = (page: Page) => page.locator('link[rel~="icon"]').getAttribute('href')

test('未登录的登录页:显示配置的名称与 LOGO,浏览器标题和 favicon 一起换', async ({ page }) => {
  await stubLoginPage(page, BRAND)
  await page.goto('/#/login')
  await expect(page.getByTestId('login-brand-name')).toHaveText('星河数据网关')
  await expect(page.locator('.login .logo [data-testid="brand-logo-img"]')).toHaveAttribute('src', LOGO)
  await expect(page).toHaveTitle('星河数据网关')
  await expect.poll(() => favicon(page)).toBe(LOGO)
})

test('没配置:登录页保持 DP DB GATEWAY、帆船图标、原 favicon', async ({ page }) => {
  await stubLoginPage(page, { name: '', nameEn: '', logo: '' })
  await page.goto('/#/login')
  await expect(page.getByTestId('login-brand-name')).toHaveText('DP DB GATEWAY')
  await expect(page.locator('.login .logo [data-testid="brand-logo-default"]')).toBeVisible()
  await expect(page).toHaveTitle('DP DB GATEWAY')
  expect(await favicon(page)).toMatch(/^data:image\/svg\+xml,/)
})

test('品牌接口拉不到:登录页照常用默认值', async ({ page }) => {
  await page.route((u) => u.pathname.startsWith('/api/'), (r) => r.fulfill({ status: 502, body: 'down' }))
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: false, loginUrl: '' })))
  await page.goto('/#/login')
  await expect(page.getByTestId('login-brand-name')).toHaveText('DP DB GATEWAY')
  await expect(page).toHaveTitle('DP DB GATEWAY')
})

test('登录后:侧栏 LOGO、浏览器标题(含待办数)、英文界面用英文名称', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/branding', (r) => r.fulfill(envelope(BRAND)))
  await page.route('**/api/v1/approvals/todo', (r) => r.fulfill(envelope({ decide: 2, execute: 0, items: [] })))
  await page.goto('/#/admin/settings?tab=appearance')
  await expect(page.locator('[data-testid="rail-logo"] [data-testid="brand-logo-img"]')).toHaveAttribute('src', LOGO)
  await expect(page.locator('.about .an')).toHaveText('星河数据网关')
  await expect(page).toHaveTitle(/^\(\d+\) 星河数据网关$/)
  await page.getByText('English', { exact: true }).click()
  await expect(page).toHaveTitle(/^\(\d+\) Galaxy DB$/)
  await expect(page.locator('.about .an')).toHaveText('Galaxy DB')
})

test('管理员在设置页改名称、上传 LOGO:只发 brand.* 三项,当前页面立刻换掉', async ({ page }) => {
  const saved: Record<string, unknown>[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: {}, secretsSet: {} }))
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({ ok: true }))
  })
  await page.goto('/#/admin/settings?tab=brand')
  await expect(page.getByTestId('brand-panel')).toBeVisible()
  await expect(page.locator('[data-testid="rail-logo"] [data-testid="brand-logo-default"]')).toBeVisible()
  // 只改品牌,不出现底部那个「保存设置」吸底栏(它不管这一页)
  await expect(page.locator('.savebar')).toHaveCount(0)

  await page.getByTestId('brand-name').fill('星河数据网关')
  await page.getByTestId('brand-name-en').fill('Galaxy DB')
  await page.getByTestId('brand-logo-file').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from(PNG_B64, 'base64') })
  await expect(page.locator('[data-testid="brand-logo-preview"] img')).toHaveAttribute('src', LOGO)
  await page.getByTestId('brand-save').click()

  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]).toEqual({ 'brand.name': '星河数据网关', 'brand.nameEn': 'Galaxy DB', 'brand.logo': LOGO })
  await expect(page.locator('[data-testid="rail-logo"] [data-testid="brand-logo-img"]')).toHaveAttribute('src', LOGO)
  await expect(page).toHaveTitle('星河数据网关')
  await expect.poll(() => favicon(page)).toBe(LOGO)

  // 恢复默认图标再存:logo 发空串
  await page.getByTestId('brand-logo-clear').click()
  await page.getByTestId('brand-save').click()
  await expect.poll(() => saved.length).toBe(2)
  expect(saved[1]['brand.logo']).toBe('')
  await expect(page.locator('[data-testid="rail-logo"] [data-testid="brand-logo-default"]')).toBeVisible()
})

test('上传超大或不支持的图片:前端直接拦下,不发请求', async ({ page }) => {
  const saved: unknown[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: {}, secretsSet: {} }))
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({ ok: true }))
  })
  await page.goto('/#/admin/settings?tab=brand')
  await page.getByTestId('brand-logo-file').setInputFiles({ name: 'big.png', mimeType: 'image/png', buffer: Buffer.alloc(300 * 1024) })
  await expect(page.getByText('图片不能超过 256 KB').first()).toBeVisible()
  await page.getByTestId('brand-logo-file').setInputFiles({ name: 'x.bmp', mimeType: 'image/bmp', buffer: Buffer.from('BM') })
  await expect(page.getByText('不支持的图片格式').first()).toBeVisible()
  await expect(page.locator('[data-testid="brand-logo-preview"] img')).toHaveCount(0)
  expect(saved).toHaveLength(0)
})
