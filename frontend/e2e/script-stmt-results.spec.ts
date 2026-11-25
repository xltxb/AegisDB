import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// \i 脚本逐条输出执行结果(2026-10 用户要求):与 mysql 客户端 source 一样,每条都看得到影响了
// 几行、查出了什么;中途失败时,失败之前的几条和失败的那一条也照样打出来。

const CONN = {
  id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'mysql.internal', port: 3306, database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const SCAN = {
  filename: 'two.sql', total: 2, high: 0, mid: 0, safe: 2, hasRisky: false,
  statements: [
    { index: 1, sql: 'INSERT INTO t VALUES (1), (2), (3)', command: 'INSERT', risk: 'safe', noWhere: false, action: 'allow' },
    { index: 2, sql: 'SELECT id FROM t', command: 'SELECT', risk: 'safe', noWhere: false, action: 'allow' },
  ],
}
const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})

async function runScript(page: Page, execBody: object) {
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/scripts/config', (r) => r.fulfill(envelope({ enabled: true, savePath: 'uploads' })))
  await page.route('**/api/v1/scripts/scan', (r) => r.fulfill(envelope(SCAN)))
  await page.route('**/api/v1/scripts/execute', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(execBody) }))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('input[type="file"].hidden-file').setInputFiles({
    name: 'two.sql', mimeType: 'text/plain', buffer: Buffer.from('INSERT INTO t VALUES (1), (2), (3);\nSELECT id FROM t;\n'),
  })
  await page.getByRole('button', { name: '执行脚本' }).click()
}

test('脚本执行成功:每条都打出结果 —— 写语句的影响行数、查询的结果表格', async ({ page }) => {
  await runScript(page, { code: 0, msg: 'ok', data: { executed: true, statements: 2, savedPath: '/data/uploads/two.sql', results: [
    { pos: 1, sql: 'INSERT INTO t VALUES (1), (2), (3)', output: '执行成功 · 3 行受影响', rows: 3, ms: 4, risk: 'low' },
    { pos: 2, sql: 'SELECT id FROM t', columns: ['id'], data: [['1'], ['2'], ['3']], rows: 3, ms: 2, risk: 'low' },
  ] } })
  await expect.poll(() => screen(page)).toContain('脚本执行成功')
  const s = await screen(page)
  expect(s).toContain('[1/2] INSERT INTO t VALUES (1), (2), (3)')
  expect(s).toContain('3 行受影响')
  expect(s).toContain('[2/2] SELECT id FROM t')
  expect(s).toMatch(/\(3 行/)
  // 顺序:第 1 条的结果在第 2 条之前,汇总在最后
  expect(s.indexOf('[1/2]')).toBeLessThan(s.indexOf('[2/2]'))
  expect(s.indexOf('[2/2]')).toBeLessThan(s.indexOf('脚本执行成功'))
})

test('脚本中途失败:失败之前的结果和失败的那一条都打出来,再说停在哪', async ({ page }) => {
  await runScript(page, { code: 1001, msg: '已执行 1/2 条后失败 · 第 2 条: Table \'shop.t\' doesn\'t exist', data: { results: [
    { pos: 1, sql: 'INSERT INTO t VALUES (1), (2), (3)', output: '执行成功 · 3 行受影响', rows: 3, ms: 4, risk: 'low' },
    { pos: 2, sql: 'SELECT id FROM t', output: "· 数据库执行失败: Table 'shop.t' doesn't exist", failed: true, risk: 'low' },
  ] } })
  await expect.poll(() => screen(page)).toContain('已执行 1/2 条后失败')
  const s = await screen(page)
  expect(s).toContain('[1/2] INSERT INTO t VALUES (1), (2), (3)')
  expect(s).toContain('3 行受影响')
  expect(s).toContain("✗ 数据库执行失败: Table 'shop.t' doesn't exist")
})

// 生成 DDL 的脚本:单元格是多行的 ALTER TABLE。表格里只能一行一截,脚本里又没法补 \G,
// 所以竖排完整打出来 —— 与在 mysql 客户端里 source 看到的一样全(2026-10 用户反馈)。
test('脚本里查出多行长文本(生成的 DDL):竖排完整显示,不截断', async ({ page }) => {
  const ddl = "ALTER TABLE `gli_gp_gateway`.`t_vip_activity_reimburse` ADD PARTITION (\n  PARTITION `p_20280101` VALUES LESS THAN ('2028-01-02 00:00:00'),\n  PARTITION `p_20280131` VALUES LESS THAN ('2028-02-01 00:00:00')\n);"
  await runScript(page, { code: 0, msg: 'ok', data: { executed: true, statements: 2, results: [
    { pos: 1, sql: "SET @target_date = '2028-01-31'", output: '执行成功', ms: 1, risk: 'low' },
    { pos: 2, sql: 'WITH RECURSIVE', columns: ['ddl'], data: [[ddl]], rows: 1, ms: 61, risk: 'low' },
  ] } })
  await expect.poll(() => screen(page)).toContain('脚本执行成功')
  const s = await screen(page)
  expect(s).toContain('1. row')
  expect(s).toContain("PARTITION `p_20280101` VALUES LESS THAN ('2028-01-02 00:00:00'),")
  expect(s).toContain("PARTITION `p_20280131` VALUES LESS THAN ('2028-02-01 00:00:00')")
  expect(s).not.toContain('…')
})
