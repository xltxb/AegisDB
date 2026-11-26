import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 设置 · 数据查询 · 权限申请方式(issue #173)。钉住:
//   · 默认网关内申请,外部申请页地址收起来;选外部系统才出现地址框;
//   · 保存时带 bizQuery.applyMode;不再有写死的默认外部地址(没配过就是空)。

async function open(page: Page, settings: Record<string, string> = {}) {
  const saved: any[] = []
  await stubCommon(page)
  for (const p of ['**/api/v1/api-clients', '**/api/v1/service-accounts']) {
    await page.route(p, (r) => r.fulfill(envelope([])))
  }
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings, secretsSet: {} }))
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope(null))
  })
  await page.goto('/#/settings')
  await page.locator('.tabs .tab', { hasText: '数据查询' }).click()
  return saved
}

const save = (page: Page) => page.getByRole('button', { name: /保存/ }).first().click()

test('默认网关内申请:地址框收起;保存带 applyMode=internal,地址为空', async ({ page }) => {
  const saved = await open(page)
  await expect(page.getByTestId('biz-apply-mode-internal')).toHaveClass(/active/)
  await expect(page.getByTestId('biz-apply-url')).toHaveCount(0)
  await save(page)
  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]['bizQuery.applyMode']).toBe('internal')
  expect(saved[0]['bizQuery.applyUrl']).toBe('')
})

test('切到外部系统:出现地址框,保存带 applyMode=external 与地址;切回去地址框又收起', async ({ page }) => {
  const saved = await open(page)
  await page.getByTestId('biz-apply-mode-external').click()
  await page.getByTestId('biz-apply-url').fill('https://oa.example.com/apply')
  await save(page)
  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]['bizQuery.applyMode']).toBe('external')
  expect(saved[0]['bizQuery.applyUrl']).toBe('https://oa.example.com/apply')
  await page.getByTestId('biz-apply-mode-internal').click()
  await expect(page.getByTestId('biz-apply-url')).toHaveCount(0)
})

test('已存为外部系统:进来就是外部,地址框显示存着的地址', async ({ page }) => {
  await open(page, { 'bizQuery.applyMode': '"external"', 'bizQuery.applyUrl': '"https://oa.example.com/x"' })
  await expect(page.getByTestId('biz-apply-mode-external')).toHaveClass(/active/)
  await expect(page.getByTestId('biz-apply-url')).toHaveValue('https://oa.example.com/x')
})
