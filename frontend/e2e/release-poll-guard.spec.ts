import { test, expect } from '@playwright/test'

// 变更发布页的轮询不能自己把后端拖死。
//
// 2026-11-10 本地:SQLite 单连接被压慢后,发布页照样每 2.5 秒发一轮列表 + 详情,不等上一轮
// 回来;后端里请求越排越长(单条 SELECT 排到 4~24 秒),连登录都排在后面等了 1 分多钟,
// 用户看到的就是"登录不进去"。上一轮没回来,这一拍就该跳过。

import { envelope, stubCommon } from './helpers'

const SLOW_MS = 6000

function release() {
  return {
    id: 1, relNo: 'REL-1', title: 'poll-guard', pipelineId: 1, pipelineName: 'fat',
    connectionId: 7, instance: 'fat-tidb', database: 'yone', env: 'fat', tierCode: 'dev',
    engine: 'tidb', changeType: 'dml', sql: 'select 1;', reason: '',
    creatorId: 1, creator: 'Lin Wei', source: 'console', status: 'running', risk: 'low', error: '',
    createdAt: '2026-11-10T10:00:00+08:00', startedAt: '2026-11-10T10:00:01+08:00', finishedAt: null,
    stages: [{ id: 11, releaseId: 1, stepOrder: 1, name: '执行变更', type: 'execute', status: 'running', log: '', config: '', onFailure: 'stop' }],
  }
}

test('后端变慢时轮询不叠加:同一时刻列表、详情各最多一个请求在途', async ({ page }) => {
  test.setTimeout(60000)
  const cur = { list: 0, detail: 0 }
  const max = { list: 0, detail: 0 }
  const total = { list: 0, detail: 0 }
  // 第一次(进页面)快速返回,之后的轮询每个都要 SLOW_MS 才回 —— 比轮询间隔 2.5 秒长得多
  const slow = (k: 'list' | 'detail', body: unknown) => async (r: import('@playwright/test').Route) => {
    total[k]++
    cur[k]++
    max[k] = Math.max(max[k], cur[k])
    if (total[k] > 1) await new Promise((res) => setTimeout(res, SLOW_MS))
    cur[k]--
    await r.fulfill(envelope(body)).catch(() => { /* 页面已关 */ })
  }
  await stubCommon(page)
  await page.route('**/api/v1/releases?*', slow('list', { items: [release()], total: 1, page: 1, pageSize: 50 }))
  await page.route(/\/api\/v1\/releases\/\d+$/, slow('detail', release()))
  await page.goto('/#/releases')
  await page.locator('.ritem').first().waitFor({ state: 'visible', timeout: 15000 })

  // 进页面后约 2.5 秒开始轮询;等够一轮慢请求的时间,期间没保护的话会发出 3 轮左右
  await page.waitForTimeout(SLOW_MS + 4000)
  expect(total.list).toBeGreaterThan(1) // 轮询确实在跑,不是没发
  expect(max.list).toBe(1)
  expect(max.detail).toBe(1)
})
