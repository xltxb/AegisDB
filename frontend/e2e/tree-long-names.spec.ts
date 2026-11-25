import { test, expect } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 对象树里表名很长时,行首的表格图标被 flex 压扁(甚至压没),名字跟着往左错位。
// 钉住:图标始终是完整的 12px,名字在行内截断(省略号),悬停能看到全名。

const CONN = {
  id: 1, name: 'tidb-prod-c66-seamless-jili', engine: 'TiDB', env: 'prod', tierCode: 'prod', status: 'online',
  host: 'tidb.internal', port: 4000, database: 'c66', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const TABLES = [
  'g_ads_order_mx_summary_lavie_report_di',
  'g_ads_order_mx_summary_others_report_di',
  'g_dim_t_platform_game_audit_settings_df',
  'g_dim_t_platform_kind_audit_settings_df_with_an_even_longer_suffix',
  'g_dim_game_kind_df',
]

test('长表名不挤压图标,名字行内截断', async ({ page }) => {
  await stubCommon(page, {
    tiers: [{ code: 'prod', displayName: 'PROD', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'prod', displayName: 'PROD', tierCode: 'prod', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'c66_seamless_jili', tables: TABLES.map((name) => ({ name })) }] })))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/objects'), (r) =>
    r.fulfill(envelope({ functions: [], procedures: [], packages: [], triggers: [] })))
  await gotoTerminal(page)
  await page.locator('.inst.active', { hasText: CONN.name }).waitFor()
  await page.locator('.db', { hasText: 'c66_seamless_jili' }).click()

  const rows = page.locator('.tbl.click')
  await expect(rows).toHaveCount(TABLES.length)
  for (let i = 0; i < TABLES.length; i++) {
    const row = rows.nth(i)
    const icon = (await row.locator('svg').boundingBox())!
    expect(Math.round(icon.width), `${TABLES[i]} 的图标被压扁了`).toBe(12)
    const rb = (await row.boundingBox())!
    const nb = (await row.locator('.nm').boundingBox())!
    expect(nb.x + nb.width, `${TABLES[i]} 的名字溢出了行`).toBeLessThanOrEqual(rb.x + rb.width + 0.5)
    await expect(row).toHaveAttribute('title', new RegExp('^' + TABLES[i]))
  }
})
