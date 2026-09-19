import { test, expect } from '@playwright/test'
import { envelope, seedSession, stubShell } from './fixtures'

// 挂在迁移任务上的执行阶段,不能给出「确认执行」按钮 —— 按下去只会让人以为
// 自己推进了什么。判据是 oscJobId,不是 status:两种 waiting 的 status 一模一样。
//
// 夹具里特意放了**两个**都是 waiting 的执行阶段:
//   · stage 71(数组里排第一个)—— 挂着迁移任务 #17,是"等迁移跑完"那种 waiting;
//   · stage 70(排第二个)—— 没挂任务,是"等人点确认执行"那种真人工闸。
// 只有这样,才能让"把判据从 oscJobId 换成 status"这类变异真正被抓住:两个阶段的
// status 都是 waiting,单独一个阶段的夹具分不出对错,换了判据也照样"通过"。
// stage 71 排在 stage 70 前面,同样是故意的 —— 这样才能验出 waitingGate(页头那颗
// 「确认执行」按钮的判据)有没有漏掉 `oscJobId === 0` 这个条件:漏了的话,数组里
// 排第一的 stage 71 会被误当成页头该指向的那道闸。
//
// stage 71 的 oscRunning 设为 false,顺带覆盖 Step 5 的另一半判据:
// `oscJobId > 0 && !oscRunning` —— 网关重启后,任务已经没有进程在推进它,
// 页面要把这件事说出来,而不是让阶段安静地停在 waiting 上等一个不会来的结果。

function releaseFixture() {
  return {
    id: 7, relNo: 'REL-7', title: '给订单表加索引', status: 'waiting',
    connectionId: 1, database: 'app', sql: 'ALTER TABLE t_order ADD INDEX i (c)',
    oscMode: '', creator: 'Lin Wei',
    stages: [
      {
        id: 71, releaseId: 7, stepOrder: 1, name: '执行', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '· [1/1] 走 OSC:约 830 万行,超过阈值 200 万 · 任务 #17\n',
        findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: 'Lin Wei', execCursor: 0, oscJobId: 17, oscRunning: false,
        startedAt: null, finishedAt: null,
      },
      {
        id: 70, releaseId: 7, stepOrder: 2, name: '二次确认', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '', findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: '', execCursor: 0, oscJobId: 0, oscRunning: false,
        startedAt: null, finishedAt: null,
      },
    ],
  }
}

test('等迁移跑完的阶段不给「确认执行」,而是指向那个任务;真人工闸照样有按钮', async ({ page }) => {
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
  // 看不出来:它对着 stage 71(挂着任务)还是 stage 70(真人工闸)亮着,只有点开
  // 之后请求带的 stageId 才说得清。
  let continuedStage = 0
  await page.route('**/api/v1/releases/7/stages/*/continue', async (r) => {
    const m = /\/stages\/(\d+)\/continue/.exec(new URL(r.request().url()).pathname)
    continuedStage = m ? Number(m[1]) : 0
    await r.fulfill(envelope({ ok: true }))
  })

  await page.goto('/changes')

  // ---- 页头的「确认执行」只能对着真人工闸(70),不能对着挂了任务的阶段(71) ----
  await page.locator('.chg-dhead button:has-text("确认执行")').click()
  await expect.poll(() => continuedStage).toBe(70)

  // ---- stage 71:挂着迁移任务 —— 没有确认执行按钮,只有指向 OSC 页的链接,
  //      外加"任务已经没人推进"的残局提示(oscRunning: false) ----
  await page.locator('.sg-strip button', { hasText: '执行' }).first().click()
  await expect(page.locator('.stage-osc-wait')).toContainText('#17')
  await expect(page.locator('.chg-stage button:has-text("确认执行")')).toHaveCount(0)
  await expect(page.locator('.stage-osc-orphan')).toContainText('#17')

  // ---- stage 70:真人工闸 —— 该有确认执行按钮,不该有链接或残局提示 ----
  await page.locator('.sg-strip button', { hasText: '二次确认' }).first().click()
  await expect(page.locator('.chg-stage button:has-text("确认执行")')).toHaveCount(1)
  await expect(page.locator('.stage-osc-wait')).toHaveCount(0)
  await expect(page.locator('.stage-osc-orphan')).toHaveCount(0)
})
