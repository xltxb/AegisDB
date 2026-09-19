import { test, expect } from '@playwright/test'
import { envelope, seedSession, stubShell } from './fixtures'

// 挂在迁移任务上的执行阶段,不能给出「确认执行」按钮 —— 按下去只会让人以为
// 自己推进了什么。判据是 oscJobId,不是 status:两种 waiting 的 status 一模一样。
//
// 夹具里放了**三个**都是 waiting 的执行阶段:
//   · stage 72(排第一个)—— 挂着还活着的迁移任务 #17(oscRunning: true),
//     该有指向 OSC 页的链接,不该有残局提示;
//   · stage 71(排第二个)—— 挂着的迁移任务 #18 已经没人推进(oscRunning: false),
//     该有残局提示,不该有那条"点此查看进度"的链接 —— 两句话不能同时出现在
//     同一张卡片上,先读到"正在跑"、再读到"已经没人推进它",人会先当它正常,
//     扫一眼就走开的人根本读不到第二句;
//   · stage 70(排第三个)—— 没挂任务,是"等人点确认执行"那种真人工闸。
// 三个阶段的 status 全是 waiting,只有 oscJobId / oscRunning 能把它们分开:
//   - 单阶段夹具下,把判据从 oscJobId 换成 status 分不出对错,变异测试杀不掉;
//   - 只有一个挂任务阶段时,把"活的/死的"两条消息合并渲染(旧的 bug)也测不出来,
//     因为夹具里没有第二个挂任务阶段作对照。
// stage 72/71(oscJobId>0)排在 stage 70(真人工闸)前面,是为了验出 waitingGate
// (页头「确认执行」按钮的判据)有没有漏掉 `oscJobId === 0`——漏了的话,数组里
// 排第一的 stage 72 会被误当成页头该指向的那道闸。

function releaseFixture() {
  return {
    id: 7, relNo: 'REL-7', title: '给订单表加索引', status: 'waiting',
    connectionId: 1, database: 'app', sql: 'ALTER TABLE t_order ADD INDEX i (c)',
    oscMode: '', creator: 'Lin Wei',
    stages: [
      {
        id: 72, releaseId: 7, stepOrder: 1, name: '执行', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '· [1/2] 走 OSC:约 830 万行,超过阈值 200 万 · 任务 #17\n',
        findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: 'Lin Wei', execCursor: 0, oscJobId: 17, oscRunning: true,
        startedAt: null, finishedAt: null,
      },
      {
        id: 71, releaseId: 7, stepOrder: 2, name: '清理确认', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '· [2/2] 走 OSC:约 120 万行 · 任务 #18\n',
        findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: 'Lin Wei', execCursor: 1, oscJobId: 18, oscRunning: false,
        startedAt: null, finishedAt: null,
      },
      {
        id: 70, releaseId: 7, stepOrder: 3, name: '二次确认', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '', findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: '', execCursor: 0, oscJobId: 0, oscRunning: false,
        startedAt: null, finishedAt: null,
      },
    ],
  }
}

test('等迁移跑完的阶段不给「确认执行」,而是指向那个任务或残局提示;真人工闸照样有按钮', async ({ page }) => {
  await seedSession(page)
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page)

  const release = releaseFixture()
  // 让这张单成为列表默认选中的第一条 —— 这个页面从不读 URL 里的 id,
  // ChangesPage 靠 `items[0]` 落在第一张单上(见 index.tsx 的 `currentId`)。
  await page.route('**/api/v1/releases?**', (r) =>
    r.fulfill(envelope({ items: [release], total: 1, page: 1, pageSize: 20 })))
  await page.route('**/api/v1/releases/7', (r) => r.fulfill(envelope(release)))

  // 记下「继续」接口实际推进的是哪个阶段 —— 页头那颗按钮点没点对,光看按钮文案
  // 看不出来:它对着 stage 72/71(挂着任务)还是 stage 70(真人工闸)亮着,只有
  // 点开之后请求带的 stageId 才说得清。
  let continuedStage = 0
  await page.route('**/api/v1/releases/7/stages/*/continue', async (r) => {
    const m = /\/stages\/(\d+)\/continue/.exec(new URL(r.request().url()).pathname)
    continuedStage = m ? Number(m[1]) : 0
    await r.fulfill(envelope({ ok: true }))
  })

  await page.goto('/changes')

  // ---- 页头的「确认执行」只能对着真人工闸(70),不能对着挂了任务的阶段(72/71) ----
  await page.locator('.chg-dhead button:has-text("确认执行")').click()
  await expect.poll(() => continuedStage).toBe(70)

  // ---- stage 72:挂着的迁移任务还活着 —— 只有指向 OSC 页的链接,没有确认执行
  //      按钮,也不该有"已经没人推进"的残局提示 ----
  await page.locator('.sg-strip button', { hasText: '执行' }).first().click()
  await expect(page.locator('.stage-osc-wait')).toContainText('#17')
  await expect(page.locator('.chg-stage button:has-text("确认执行")')).toHaveCount(0)
  await expect(page.locator('.stage-osc-orphan')).toHaveCount(0)

  // ---- stage 71:挂着的迁移任务已经没人推进 —— 只有残局提示,没有确认执行按钮,
  //      也不该再有"点此查看进度"的链接(与 stage 72 互斥,不能两句话一起出现) ----
  await page.locator('.sg-strip button', { hasText: '清理确认' }).first().click()
  await expect(page.locator('.stage-osc-orphan')).toContainText('#18')
  await expect(page.locator('.chg-stage button:has-text("确认执行")')).toHaveCount(0)
  await expect(page.locator('.stage-osc-wait')).toHaveCount(0)

  // ---- stage 70:真人工闸 —— 该有确认执行按钮,不该有链接或残局提示 ----
  await page.locator('.sg-strip button', { hasText: '二次确认' }).first().click()
  await expect(page.locator('.chg-stage button:has-text("确认执行")')).toHaveCount(1)
  await expect(page.locator('.stage-osc-wait')).toHaveCount(0)
  await expect(page.locator('.stage-osc-orphan')).toHaveCount(0)
})
