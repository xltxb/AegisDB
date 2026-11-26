import { test, expect, type Page } from '@playwright/test'

// 大脚本(#161):详情页只渲染开头一段,不把几十万行整段高亮塞进页面 —— 那样打开要卡十几秒,
// 单子执行中时状态灯的动画每一帧都要重绘整块代码,页面一直卡着。行数和「复制」仍是完整脚本。

import { envelope, stubCommon, ME } from './helpers'

const LINES = 20000
const SQL = Array.from({ length: LINES }, (_, i) => `INSERT INTO t_order (id) VALUES (${i + 1});`).join('\n')
const REL = {
  id: 31, relNo: 'REL-31', title: 'big', pipelineId: 1, pipelineName: 'Uat', connectionId: 7, instance: 'UAT-MYSQL',
  database: 'db', env: 'uat', tierCode: 'uat', engine: 'mysql', changeType: 'dml', projectId: 0, projectName: '',
  sql: SQL, reason: '', creatorId: 9, creator: 'q', source: 'console', clientName: '', externalRef: '',
  status: 'success', risk: 'low', error: '', createdAt: '2026-11-09T15:24:20+08:00', startedAt: '2026-11-09T15:24:21+08:00',
  finishedAt: '2026-11-09T15:26:30+08:00',
  stages: [{ id: 311, releaseId: 31, stepOrder: 1, name: '执行变更', type: 'execute', status: 'success', log: '', config: '', onFailure: 'stop', confirmedBy: '' }],
}

async function open(page: Page, rel: typeof REL) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, canSeeAllReleases: true })))
  await page.route('**/api/v1/releases?*', (r) =>
    r.fulfill(envelope({ items: [{ ...rel, sql: rel.sql.slice(0, 400) }], total: 1, page: 1, pageSize: 50, envs: ['uat'] })))
  await page.route(/\/api\/v1\/releases\/\d+(\?.*)?$/, (r) => r.fulfill(envelope(rel)))
  await page.goto('/#/releases')
  await page.locator('.ide-code').waitFor({ state: 'visible', timeout: 15000 })
}

test.describe('变更发布 · 大脚本', () => {
  test('只渲染开头一段,说清共多少行、显示了多少行;行数统计仍是全部', async ({ page }) => {
    await open(page, REL)
    const code = page.locator('.ide-code')
    await expect(code).toContainText('VALUES (1);')
    await expect(code).toContainText('VALUES (5000);')
    await expect(code).not.toContainText('VALUES (5001);')
    await expect(page.locator('.ide-meta')).toHaveText('20000 行')
    await expect(page.locator('[data-testid="rl-sql-truncated"]')).toContainText('20000')
    await expect(page.locator('[data-testid="rl-sql-truncated"]')).toContainText('5000')
    const gutter = (await page.locator('.ide-gutter').textContent()) || ''
    expect(gutter.trim().split('\n').pop()).toBe('5000')
  })

  test('「复制」复制的是完整脚本', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await open(page, REL)
    await page.locator('.ide-copy').click()
    const got = await page.evaluate(() => navigator.clipboard.readText())
    expect(got.length).toBe(SQL.length)
  })

  test('小脚本照常全部显示,没有截断提示', async ({ page }) => {
    await open(page, { ...REL, sql: 'RENAME TABLE a TO b;\nSELECT 1;' })
    await expect(page.locator('.ide-code')).toContainText('SELECT 1;')
    await expect(page.locator('[data-testid="rl-sql-truncated"]')).toHaveCount(0)
  })

  test('单行超长(无换行的大脚本)也按字数截断', async ({ page }) => {
    const one = 'INSERT INTO t VALUES ' + Array.from({ length: 60000 }, (_, i) => `(${i})`).join(',') + ';'
    await open(page, { ...REL, sql: one })
    await expect(page.locator('[data-testid="rl-sql-truncated"]')).toBeVisible()
    const shown = ((await page.locator('.ide-code').textContent()) || '').length
    expect(shown).toBeLessThan(one.length)
  })
})
