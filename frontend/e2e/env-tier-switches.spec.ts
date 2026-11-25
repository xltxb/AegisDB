import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 2026-10-29:敏感字段脱敏、数据查询导出限制按分层开关(默认 prod / staging / gli 开,dev / uat 关)。
// 分层页上两列开关照服务端的值显示,点一下只改这一项(其余开关原样带上)。

const T = (code: string, on: boolean, extra: Record<string, unknown> = {}) => ({
  code, displayName: code.toUpperCase(), sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false,
  scanBaseline: false, strictNoWhere: true, maskSensitive: on, bizExportRestrict: on, connLayer: 'L', defaultRole: 'dba_l2', ...extra,
})

const ON = /(^|\s)on(\s|$)/

test('分层页:脱敏与数据查询导出限制两列开关,点一下只改这一项', async ({ page }) => {
  const puts: { code: string; body: any }[] = []
  await stubCommon(page, {
    tiers: [T('prod', true, { scanBaseline: true }), T('dev', false)],
    envs: [{ code: 'prod', displayName: 'PROD', tierCode: 'prod', sortOrder: 0 }, { code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 1 }],
  })
  await page.route('**/api/v1/env-tiers/*', (r) => {
    puts.push({ code: r.request().url().split('/').pop()!, body: r.request().postDataJSON() })
    return r.fulfill(envelope({}))
  })
  await page.route('**/api/v1/connections*', (r) => r.fulfill(envelope([])))
  await page.goto('/#/admin/env-tiers')

  await expect(page.getByTestId('tier-th')).toContainText('敏感字段脱敏')
  await expect(page.getByTestId('tier-th')).toContainText('数据查询导出限制')
  const devMask = page.getByTestId('tier-mask-dev')
  await expect(devMask).not.toHaveClass(ON)
  await expect(page.getByTestId('tier-mask-prod')).toHaveClass(ON)
  await expect(page.getByTestId('tier-bizexport-prod')).toHaveClass(ON)

  await devMask.click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0].code).toBe('dev')
  expect(puts[0].body).toMatchObject({ maskSensitive: true, bizExportRestrict: false, strictNoWhere: true })
  await page.getByTestId('tier-bizexport-prod').click()
  await expect.poll(() => puts.length).toBe(2)
  expect(puts[1].code).toBe('prod')
  expect(puts[1].body).toMatchObject({ bizExportRestrict: false, maskSensitive: true, requireMfa: false })
})
