import { test, expect, type Page } from '@playwright/test'

// 变更发布页的关键信息:改哪个环境的哪个库、对应上游哪张单、谁从哪儿提的。
// 原来这些挤在标题下一行灰字里(用户 2026-11-09 反馈"看不到环境、库、上游单号"),
// 现在列表卡片和详情头部各有一块明确标注的区域。

import { envelope, stubCommon, ME } from './helpers'

const API_REL = {
  id: 31, relNo: 'REL-31', title: 'newVIP-SQL-orderID=16583-exec_order=5-UAT-DDL', pipelineId: 1, pipelineName: 'Uat',
  connectionId: 7, instance: 'UAT-MYSQL', database: 'uat_c66_gp_activity', env: 'uat', tierCode: 'uat',
  engine: 'mysql', changeType: 'ddl', projectId: 3, projectName: 'GP',
  sql: 'RENAME TABLE a TO b;', reason: '[UAT] DDL 自动执行,来源 UpgradeOrder#16583',
  creatorId: 9, creator: 'query', source: 'api', clientName: 'srd_platform', externalRef: 'autopub:16583:UAT:297040:1',
  status: 'success', risk: 'low', error: '',
  createdAt: '2026-11-09T15:24:20+08:00', startedAt: '2026-11-09T15:24:21+08:00', finishedAt: '2026-11-09T15:26:30+08:00',
  stages: [
    { id: 310, releaseId: 31, stepOrder: 1, name: '规范审查', type: 'review', status: 'success', log: '', config: '', onFailure: 'stop' },
    { id: 311, releaseId: 31, stepOrder: 2, name: '执行变更', type: 'execute', status: 'success', log: '', config: '', onFailure: 'stop', confirmedBy: 'Chen Hao' },
  ],
}
const CONSOLE_REL = {
  ...API_REL, id: 30, relNo: 'REL-30', title: '手工发布', source: 'console', clientName: '', externalRef: '',
  projectId: 0, projectName: '', reason: '', creator: 'Lin Wei', finishedAt: null, env: 'fat', tierCode: 'dev', database: 'yone',
  status: 'waiting',
  stages: [{ id: 300, releaseId: 30, stepOrder: 1, name: '执行变更', type: 'execute', status: 'waiting', log: '', config: '', onFailure: 'stop', confirmedBy: '' }],
}
const AUTO_REL = {
  ...API_REL, id: 29, relNo: 'REL-29', title: '自动执行', source: 'console', clientName: '', externalRef: '', creator: 'Lin Wei',
  stages: [{ id: 290, releaseId: 29, stepOrder: 1, name: '自动执行', type: 'auto_execute', status: 'success', log: '', config: '', onFailure: 'stop', confirmedBy: '' }],
}
const ALL = [API_REL, CONSOLE_REL, AUTO_REL]

async function openReleases(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, canSeeAllReleases: true })))
  await page.route('**/api/v1/releases?*', (r) =>
    r.fulfill(envelope({ items: ALL, total: ALL.length, page: 1, pageSize: 50, envs: ['fat', 'uat'] })))
  await page.route(/\/api\/v1\/releases\/\d+(\?.*)?$/, (r) => {
    const id = Number(new URL(r.request().url()).pathname.split('/').pop())
    return r.fulfill(envelope(ALL.find((x) => x.id === id)))
  })
  await page.goto('/#/releases')
  await page.locator('.ritem').first().waitFor({ state: 'visible', timeout: 15000 })
}

const kv = (page: Page, k: string) => page.locator(`[data-testid="rl-keyinfo"] .kv[data-k="${k}"] .v`)

test.describe('变更发布 · 关键信息', () => {
  test('列表卡片:环境、库、带标签的上游单号与来源系统', async ({ page }) => {
    await openReleases(page)
    const card = page.locator('.ritem').first()
    await expect(card.locator('.tag.env')).toHaveText('uat')
    await expect(card.locator('.rdb')).toHaveText('/ uat_c66_gp_activity')
    await expect(card.locator('.xref')).toHaveText('上游autopub:16583:UAT:297040:1')
    await expect(card.locator('.rfrom')).toContainText('srd_platform')
    await expect(card.locator('.rfrom')).toContainText('query')
  })

  test('详情头部:环境/分层、实例、库、上游单号、来源、发起人、项目、原因', async ({ page }) => {
    await openReleases(page)
    await expect(kv(page, 'env')).toHaveText('uatUAT')
    await expect(kv(page, 'inst')).toHaveText('UAT-MYSQL · mysql')
    await expect(kv(page, 'db')).toHaveText('uat_c66_gp_activity')
    await expect(kv(page, 'ref')).toContainText('autopub:16583:UAT:297040:1')
    await expect(kv(page, 'source')).toHaveText('API srd_platform')
    await expect(kv(page, 'creator')).toHaveText('query')
    await expect(kv(page, 'project')).toHaveText('GP')
    await expect(kv(page, 'pipeline')).toHaveText('Uat')
    await expect(kv(page, 'time')).toContainText('→')
    // 原因移到关键信息里,变更内容下面不再重复一份
    await expect(page.locator('.reason')).toHaveCount(1)
    await expect(page.locator('[data-testid="rl-keyinfo"] .reason')).toContainText('UpgradeOrder#16583')
  })

  test('控制台提交的单:来源写控制台,没有上游单号显示 —,不留空', async ({ page }) => {
    await openReleases(page)
    await page.locator('.ritem').nth(1).click()
    await expect(page.locator('.detail .dt')).toContainText('REL-30')
    await expect(kv(page, 'source')).toHaveText('控制台')
    await expect(kv(page, 'ref')).toHaveText('—')
    await expect(kv(page, 'project')).toHaveText('—')
    await expect(kv(page, 'env')).toHaveText('fatDEV')
    await expect(kv(page, 'time')).not.toContainText('→')
    await expect(page.locator('.ritem').nth(1).locator('.xref')).toHaveCount(0)
    await expect(page.locator('.reason')).toHaveCount(0)
  })

  test('执行人:点确认执行的人(不是发起人)、待确认、自动执行', async ({ page }) => {
    await openReleases(page)
    await expect(kv(page, 'creator')).toHaveText('query')
    await expect(kv(page, 'executor')).toHaveText('Chen Hao')
    await expect(page.locator('.ritem').first().locator('.rexec')).toHaveText('→ 执行 Chen Hao')

    await page.locator('.ritem').nth(1).click()
    await expect(page.locator('.detail .dt')).toContainText('REL-30')
    await expect(kv(page, 'executor')).toHaveText('待确认执行')
    await expect(page.locator('.ritem').nth(1).locator('.rexec')).toHaveCount(0)

    await page.locator('.ritem').nth(2).click()
    await expect(page.locator('.detail .dt')).toContainText('REL-29')
    await expect(kv(page, 'executor')).toHaveText('自动执行')
  })

  // 2026-11-23 用户:环境是淡色小胶囊,太不显眼。改成分层色实心块(CONSOLE_REL 是 dev 分层 → 绿)。
  test('环境醒目:列表与详情都是分层色实心块', async ({ page }) => {
    await openReleases(page)
    const solid = await page.evaluate(() => getComputedStyle(document.querySelector('.page') as Element).getPropertyValue('--success').trim())
    const bg = (l: ReturnType<Page['locator']>) => l.evaluate((el) => getComputedStyle(el).backgroundColor)
    const toRgb = await page.evaluate((c) => { const d = document.createElement('div'); d.style.color = c; document.body.append(d); const v = getComputedStyle(d).color; d.remove(); return v }, solid)
    const chip = page.locator('.ritem').nth(1).locator('.tag.env')
    await expect(chip).toHaveClass(/success/)
    expect(await bg(chip)).toBe(toRgb)
    await page.locator('.ritem').nth(1).click()
    await expect(page.locator('.detail .dt')).toContainText('REL-30')
    const big = kv(page, 'env').locator('.envchip')
    await expect(big).toHaveText('fat')
    expect(await bg(big)).toBe(toRgb)
  })
})
