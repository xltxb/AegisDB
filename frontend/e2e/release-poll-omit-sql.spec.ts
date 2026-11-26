import { test, expect, type Page } from '@playwright/test'

// 轮询不重复传脚本(#163):同一张单首次打开取完整脚本,之后轮询带 ?omitSql=1,页面上的脚本保持不变;
// 换一张单,先取它的完整脚本。

import { envelope, stubCommon, ME } from './helpers'

const base = {
  pipelineId: 1, pipelineName: 'Uat', connectionId: 7, instance: 'UAT-MYSQL', database: 'db', env: 'uat', tierCode: 'uat',
  engine: 'mysql', changeType: 'ddl', projectId: 0, projectName: '', reason: '', creatorId: 9, creator: 'q',
  source: 'console', clientName: '', externalRef: '', status: 'running', risk: 'low', error: '',
  createdAt: '2026-11-09T15:24:20+08:00', startedAt: '2026-11-09T15:24:21+08:00', finishedAt: null,
}
const REL = (id: number, sql: string) => ({
  ...base, id, relNo: `REL-${id}`, title: `t${id}`, sql,
  stages: [{ id: id * 10, releaseId: id, stepOrder: 1, name: '执行变更', type: 'execute', status: 'running', log: '', config: '', onFailure: 'stop', confirmedBy: '' }],
})
const A = REL(31, 'CREATE TABLE alpha (id INT);')
const B = REL(32, 'CREATE TABLE beta (id INT);')

async function open(page: Page, calls: string[]) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, canSeeAllReleases: true })))
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [A, B], total: 2, page: 1, pageSize: 50, envs: ['uat'] })))
  await page.route(/\/api\/v1\/releases\/\d+(\?.*)?$/, (r) => {
    const u = new URL(r.request().url())
    const id = Number(u.pathname.split('/').pop())
    const omit = u.searchParams.get('omitSql') === '1'
    calls.push(`${id}${omit ? ':omit' : ''}`)
    const rel = id === A.id ? A : B
    return r.fulfill(envelope(omit ? { ...rel, sql: '', sqlOmitted: true } : rel))
  })
  await page.goto('/#/releases')
  await page.locator('.ide-code').waitFor({ state: 'visible', timeout: 15000 })
}

test('轮询不带脚本,页面上的脚本不变;换单先取完整脚本', async ({ page }) => {
  const calls: string[] = []
  await open(page, calls)
  await expect(page.locator('.ide-code')).toContainText('alpha')
  await expect.poll(() => calls.filter((c) => c === '31:omit').length, { timeout: 10000 }).toBeGreaterThanOrEqual(2)
  expect(calls[0]).toBe('31')
  expect(calls.filter((c) => c === '31')).toHaveLength(1)
  await expect(page.locator('.ide-code')).toContainText('alpha')
  await expect(page.locator('.ritem').first()).toContainText('t31')

  await page.locator('.ritem').nth(1).click()
  await expect(page.locator('.ide-code')).toContainText('beta')
  const firstB = calls.findIndex((c) => c.startsWith('32'))
  expect(calls[firstB]).toBe('32')
  await expect.poll(() => calls.filter((c) => c === '32:omit').length, { timeout: 10000 }).toBeGreaterThanOrEqual(1)
  await expect(page.locator('.ide-code')).toContainText('beta')
})
