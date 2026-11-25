import { test, expect } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 扫描结论按当前用户的完整判定给出:有语句被禁止时,横幅说清是哪一条、整份不会执行,
// 执行按钮灰掉 —— 不让人点下去才收到一句拒绝。

const CONN = {
  id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'mysql.internal', port: 3306, database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}

test('脚本里有被禁止的语句:横幅写明哪一条,执行按钮不可点', async ({ page }) => {
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/scripts/config', (r) => r.fulfill(envelope({ enabled: true, savePath: 'uploads' })))
  await page.route('**/api/v1/scripts/scan', (r) => r.fulfill(envelope({
    filename: 'mixed.sql', total: 2, high: 1, mid: 0, safe: 1, hasRisky: true,
    blocked: true, blockedRule: '第 2 条:能力矩阵 · 该环境禁止此操作',
    statements: [
      { index: 1, sql: 'INSERT INTO t VALUES (1)', command: 'INSERT', risk: 'safe', noWhere: false, action: 'allow' },
      { index: 2, sql: 'GRANT SELECT ON t TO x', command: 'GRANT', risk: 'high', noWhere: false, action: 'deny', rule: '能力矩阵 · 该环境禁止此操作' },
    ],
  })))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('input[type="file"].hidden-file').setInputFiles({
    name: 'mixed.sql', mimeType: 'text/plain', buffer: Buffer.from('INSERT INTO t VALUES (1);\nGRANT SELECT ON t TO x;\n'),
  })
  const banner = page.getByTestId('sc-banner')
  await expect(banner).toContainText('整份脚本不会执行')
  await expect(banner).toContainText('第 2 条')
  await expect(page.getByRole('button', { name: /提交审批后执行|执行脚本/ })).toBeDisabled()
})

// 班车时段:高危条数照报,但不送审 —— 横幅说清是哪个执行窗口放行的,按钮是"执行"而不是"提交审批"。
test('执行窗口内:横幅写明窗口免审批,可以直接执行', async ({ page }) => {
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/scripts/config', (r) => r.fulfill(envelope({ enabled: true, savePath: 'uploads' })))
  await page.route('**/api/v1/scripts/scan', (r) => r.fulfill(envelope({
    filename: 'game.sql', total: 2, high: 1, mid: 0, safe: 1, hasRisky: false, window: 'GAME域班车',
    statements: [
      { index: 1, sql: 'ALTER TABLE t ADD (c NUMBER)', command: 'ALTER', risk: 'high', noWhere: false, action: 'allow', rule: '执行窗口「GAME域班车」' },
      { index: 2, sql: 'COMMENT ON TABLE t IS \'x\'', command: 'COMMENT', risk: 'safe', noWhere: false, action: 'allow' },
    ],
  })))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('input[type="file"].hidden-file').setInputFiles({
    name: 'game.sql', mimeType: 'text/plain', buffer: Buffer.from('ALTER TABLE t ADD (c NUMBER);\nCOMMENT ON TABLE t IS \'x\';\n'),
  })
  const banner = page.getByTestId('sc-banner')
  await expect(banner).toContainText('GAME域班车')
  await expect(banner).toContainText('免审批')
  await expect(page.getByRole('button', { name: /提交审批后执行/ })).toHaveCount(0)
})
