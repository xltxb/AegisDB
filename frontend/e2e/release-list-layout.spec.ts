import { test, expect } from '@playwright/test'

// 变更发布页左栏:单子多到超出列表高度时,卡片应该滚动,而不是被压扁。
//
// 2026-11-10 改版后用户 33 张单时,每张卡片被压成一条细缝,只看得见单号那一行:
// 列表是限高的纵向 flex,卡片带 overflow: hidden,最小高度归 0,flex 就把它们一起压缩。

import { envelope, stubCommon } from './helpers'

function release(id: number) {
  return {
    id, relNo: `REL-${id}`, title: `批量订正-${id}`, pipelineId: 1, pipelineName: '标准发布流程',
    connectionId: 7, instance: 'bbopen-mysql-a', database: 'bbopen_db', env: 'dev', tierCode: 'dev',
    engine: 'mysql', changeType: 'dml', sql: 'select 1;', reason: '',
    creatorId: 1, creator: 'Lin Wei', source: 'console', status: 'aborted', risk: 'low', error: '',
    createdAt: '2026-10-19T11:54:36+08:00', startedAt: null, finishedAt: null,
    stages: [{ id: id * 10, releaseId: id, stepOrder: 1, name: '规范审查', type: 'review', status: 'success', log: '', config: '', onFailure: 'stop' }],
  }
}

test('30 张单:卡片完整显示标题和环境,列表自己滚动', async ({ page }) => {
  const all = Array.from({ length: 30 }, (_, i) => release(30 - i))
  await stubCommon(page)
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: all, total: all.length, page: 1, pageSize: 50 })))
  await page.route(/\/api\/v1\/releases\/\d+$/, (r) => {
    const id = Number(new URL(r.request().url()).pathname.split('/').pop())
    return r.fulfill(envelope(all.find((x) => x.id === id)))
  })
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/#/releases')
  const cards = page.locator('.ritem')
  await cards.first().waitFor({ state: 'visible', timeout: 15000 })
  await expect(cards).toHaveCount(30)

  // 每张卡片都比"单号 + 标题 + 环境"三行的内容高,而且没有被裁:内容高度 = 盒子高度
  const sizes = await cards.evaluateAll((els) => els.map((e) => ({ h: e.clientHeight, sh: e.scrollHeight })))
  for (const s of sizes) {
    expect(s.h).toBeGreaterThan(80)
    expect(s.sh).toBeLessThanOrEqual(s.h + 1)
  }
  // 超出的部分靠列表滚动,不是压缩
  const list = await page.locator('.list').evaluate((e) => ({ h: e.clientHeight, sh: e.scrollHeight }))
  expect(list.sh).toBeGreaterThan(list.h)
  await expect(cards.first().locator('.rtitle')).toBeVisible()
  await expect(cards.first().locator('.tag.env')).toBeVisible()
})
