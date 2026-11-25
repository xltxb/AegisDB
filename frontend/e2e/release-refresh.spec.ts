import { test, expect, type Page } from '@playwright/test'

// 变更发布页:摊开那张单的阶段日志不能被列表刷新盖掉;顶部有手动刷新。
//
// 用户实测 REL-1 两个阶段都「已完成」、库里也执行成功了,「执行变更」卡片却写着
// "该阶段暂无日志"。后端写了日志,详情接口也带着 —— 是页面把**列表**接口的数据(故意不带
// 阶段日志,见后端 ListReleases)盖到了摊开的那张上:切「我的 / 全部」、轮询都会触发。

import { envelope, stubCommon } from './helpers'

const EXEC_LOG = '· 执行完成 · 1 条语句 · 影响 1 行\n· [1/1] 执行成功 · 1 行受影响 (12ms)\n    update t_bond set bond_type=1 where bondseq_id=855048;\n'

function release(id: number, withLogs: boolean) {
  return {
    id, relNo: `REL-${id}`, title: `abiu-test-${id}`, pipelineId: 1, pipelineName: 'fat',
    connectionId: 7, instance: 'fat-tidb', database: 'yone', env: 'fat', tierCode: 'dev',
    engine: 'tidb', changeType: 'dml', sql: 'update t_bond set bond_type=1 where bondseq_id=855048;', reason: '',
    creatorId: 1, creator: 'Lin Wei', source: 'api', status: 'success', risk: 'low', error: '',
    createdAt: '2026-10-30T10:16:25+08:00', startedAt: null, finishedAt: null,
    stages: [
      { id: id * 10 + 1, releaseId: id, stepOrder: 1, name: '规范审查', type: 'review', status: 'success',
        log: withLogs ? '· 审查通过 · 1 条语句' : '', config: '', onFailure: 'stop' },
      { id: id * 10 + 2, releaseId: id, stepOrder: 2, name: '执行变更', type: 'execute', status: 'success',
        log: withLogs ? EXEC_LOG : '', config: '', onFailure: 'stop' },
    ],
  }
}

// 列表照真后端的样子:不带阶段日志。详情带。
async function openReleases(page: Page, counts: { list: number; detail: Record<number, number> }, gate?: Promise<void>) {
  await stubCommon(page)
  await page.route('**/api/v1/releases?*', async (r) => {
    counts.list++
    if (gate && counts.list > 1) await gate
    await r.fulfill(envelope({ items: [release(2, false), release(1, false)], total: 2, page: 1, pageSize: 50 }))
  })
  await page.route(/\/api\/v1\/releases\/\d+$/, async (r) => {
    const id = Number(new URL(r.request().url()).pathname.split('/').pop())
    counts.detail[id] = (counts.detail[id] || 0) + 1
    if (gate && counts.detail[id] > 1) await gate
    await r.fulfill(envelope(release(id, true)))
  })
  await page.goto('/#/releases')
  await page.locator('.ritem').first().waitFor({ state: 'visible', timeout: 15000 })
}

const execNode = (page: Page) => page.locator('.node', { hasText: '执行变更' })
const stageBox = (page: Page) => page.locator('.sbox')

test.describe('变更发布 · 阶段日志与刷新', () => {
  test('执行变更的日志来自详情,切「全部」后列表刷新也不把它盖成"暂无日志"', async ({ page }) => {
    const counts = { list: 0, detail: {} as Record<number, number> }
    await openReleases(page, counts)

    await execNode(page).click()
    await expect(stageBox(page)).toContainText('update t_bond set bond_type=1 where bondseq_id=855048')
    await expect(stageBox(page)).toContainText('1 行受影响')

    // 切范围只重拿列表 —— 列表里没有日志。修之前这一下就变成"该阶段暂无日志"。
    const before = counts.list
    await page.locator('.seg .si', { hasText: '全部' }).click()
    await expect.poll(() => counts.list).toBeGreaterThan(before)
    await expect(stageBox(page)).toContainText('1 行受影响')
    await expect(stageBox(page)).not.toContainText('该阶段暂无日志')
  })

  test('刷新按钮重拿列表和选中单的详情,选中的单和阶段不变,刷新中有加载态', async ({ page }) => {
    let release_!: () => void
    const gate = new Promise<void>((res) => { release_ = res })
    const counts = { list: 0, detail: {} as Record<number, number> }
    await openReleases(page, counts, gate)

    // 选第二张(REL-1),不是默认的第一张
    await page.locator('.ritem', { hasText: 'REL-1' }).click()
    await expect.poll(() => counts.detail[1] || 0).toBeGreaterThan(0)
    await execNode(page).click()
    await expect(stageBox(page)).toContainText('1 行受影响')

    const list0 = counts.list
    const det0 = counts.detail[1]
    const btn = page.locator('button.rl-refresh')
    await btn.click()
    // 请求被 gate 挂住时:按钮显示加载态、不可再点
    await expect(btn).toContainText('刷新中')
    await expect(btn).toBeDisabled()
    release_()
    await expect(btn).toBeEnabled()
    await expect(btn).not.toContainText('刷新中')

    expect(counts.list).toBeGreaterThan(list0)
    expect(counts.detail[1]).toBeGreaterThan(det0)
    // 选中的还是 REL-1,摊开的阶段还在,日志还在
    await expect(page.locator('.ritem.on')).toContainText('REL-1')
    await expect(stageBox(page)).toContainText('执行变更')
    await expect(stageBox(page)).toContainText('update t_bond set bond_type=1')
  })
})
