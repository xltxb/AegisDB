import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// Webhook 的「推送环境」:只推选定环境里实例的事件,空 = 全部环境。
//
// 钉四条:环境按列表画出来、没选时「全部环境」亮着;点一个环境就只推它;点「全部环境」清空;
// 已存的、列表里已经没有的环境代码照样画出来(否则看不见它、它却一直在过滤)。

const ENVS = [
  { code: 'prod', displayName: '生产环境', tierCode: 'prod', sortOrder: 0 },
  { code: 'hk-prod', displayName: '香港生产', tierCode: 'prod', sortOrder: 1 },
  { code: 'uat', displayName: 'UAT', tierCode: 'uat', sortOrder: 5 },
]

async function open(page: Page, envs: string) {
  const saved: any[] = []
  await stubCommon(page, { envs: ENVS })
  await page.route((u) => u.pathname.endsWith('/api/v1/settings'), (r) => {
    if (r.request().method() !== 'GET') return r.fallback()
    return r.fulfill(envelope({
      settings: {},
      webhook: { id: 1, endpoint: 'http://127.0.0.1:18099/ingest', secret: '', events: 'delete', envs, retryMax: 3, enabled: true },
      secretsSet: {}, webhookHasSecret: true, webhookActions: ['create', 'delete', 'update', 'read', 'other'],
    }))
  })
  await page.route((u) => u.pathname.endsWith('/api/v1/settings/webhook'), (r) => {
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({ ok: true }))
  })
  await page.route('**/api/v1/settings/webhook/deliveries', (r) => r.fulfill(envelope([])))
  await page.goto('/#/settings')
  await page.locator('.tab', { hasText: 'Webhook' }).click()
  await page.getByTestId('wh-envs').waitFor()
  return saved
}

test.describe('设置 · Webhook 推送环境', () => {
  test('没选环境时「全部环境」亮着,环境按列表画出来', async ({ page }) => {
    await open(page, '')
    const chips = page.getByTestId('wh-envs').locator('.ev')
    await expect(chips).toHaveText(['全部环境', '生产环境', '香港生产', 'UAT'])
    await expect(page.getByTestId('wh-env-all')).not.toHaveClass(/off/)
    await expect(chips.nth(1)).toHaveClass(/off/)
    await expect(page.getByText('登录等不属于任何实例的事件不受此限')).toBeVisible()
  })

  test('点一个环境就只推它;再点「全部环境」清空', async ({ page }) => {
    const saved = await open(page, '')
    await page.locator('[data-env="hk-prod"]').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0]).toMatchObject({ envs: 'hk-prod', events: 'delete' }) // 事件订阅不跟着变
    await expect(page.getByTestId('wh-env-all')).toHaveClass(/off/)
    await expect(page.locator('[data-env="hk-prod"] svg')).toHaveCount(1)

    await page.getByTestId('wh-env-all').click()
    await expect.poll(() => saved.length).toBe(2)
    expect(saved[1].envs).toBe('')
  })

  test('已存的、列表里已经没有的环境照样画出来,可以取消', async ({ page }) => {
    const saved = await open(page, 'uat,sh-prod')
    await expect(page.locator('[data-env="sh-prod"]')).toHaveText('sh-prod')
    await expect(page.locator('[data-env="uat"] svg')).toHaveCount(1)
    await page.locator('[data-env="sh-prod"]').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].envs).toBe('uat')
  })
})
