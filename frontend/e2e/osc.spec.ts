import { test, expect, type Page } from '@playwright/test'
import { envelope, seedSession, stubShell } from './fixtures'

// 在线表结构变更(ADR 0011)这张页面上,有三件事只有在浏览器里才成立:
// 按钮到底点不点得动、警告有没有真的出现在按钮旁边、以及重启之后残局露不露头。
//
// 它们各自对应一种真实的坏法:
//   · 开关关着但按钮能点 —— 半成品被点起来,在生产库上建了影子表;
//   · 警告只写在 ADR 里 —— 发起的人从来不会读到"没有从库限流";
//   · 残局不显示 —— 一张影子表在库里躺到磁盘报警才被发现;
//   · 一次没限流的迁移看起来和限了流的一模一样 —— 事后没人答得出从库当时被护着没有。

const JOBS = {
  live: {
    id: 12, connectionId: 1, schema: 'app', table: 't_order',
    alter: 'ADD INDEX idx_memo (memo)', status: 'copying', shadow: 't_order_gho',
    copiedRows: 4000, totalRows: 10000, err: '', createdBy: 'Lin Wei',
    createdAt: '2026-09-18T10:00:00Z', updatedAt: '2026-09-18T10:01:00Z', finishedAt: null,
    running: true,
    throttle: '未启用:主库上没有发现从库(单机实例,或从库没配 report_host)', throttled: false,
  },
  stranded: {
    id: 11, connectionId: 1, schema: 'app', table: 't_user',
    alter: 'ADD INDEX idx_mail (mail)', status: 'failed', shadow: 't_user_gho',
    copiedRows: 120, totalRows: 900, err: '回放中断:binlog 位点丢失',
    createdBy: 'Lin Wei', createdAt: '2026-09-17T22:00:00Z',
    updatedAt: '2026-09-17T22:10:00Z', finishedAt: '2026-09-17T22:10:00Z', running: false,
    throttle: '已启用:2 个从库,阈值 30s', throttled: true,
  },
}

async function openOsc(page: Page, opts: { enabled: boolean; jobs?: unknown[] }) {
  await seedSession(page)
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page)
  await page.route('**/api/v1/osc/status', (r) =>
    r.fulfill(envelope({
      enabled: opts.enabled,
      caveats: ['限流装不装得起来,要看目标实例当时的样子:主库报不出从库时照跑,但不限流。'],
    })))
  await page.route('**/api/v1/osc/jobs', (r) => {
    if (r.request().method() === 'POST') {
      r.fulfill(envelope({ ...JOBS.live, id: 99 }))
      return
    }
    r.fulfill(envelope(opts.jobs ?? []))
  })
  await page.goto('/osc')
  await expect(page.locator('.page-head h1')).toBeVisible()
}

test('开关关着时"开始迁移"点不动,而且页面上写着为什么', async ({ page }) => {
  await openOsc(page, { enabled: false })

  await expect(page.locator('.page-head button:has-text("开始迁移")')).toBeDisabled()
  // 缺限流这件事必须出现在页面上 —— 不是出现在 ADR 里。
  await expect(page.locator('.osc-caveats')).toContainText('从库')
  await expect(page.locator('.osc-caveats')).toContainText('osc.enabled')
})

test('开关打开后才点得动,并且能发起一次迁移', async ({ page }) => {
  await openOsc(page, { enabled: true })

  const start = page.locator('.page-head button:has-text("开始迁移")')
  await expect(start).toBeEnabled()
  await start.click()

  // 弹窗开着,但四个字段填齐之前发起按钮不放行。
  const go = page.locator('.c-modal button:has-text("发起")')
  await expect(go).toBeDisabled()

  await page.selectOption('.c-modal select', { index: 0 })
  const posted = page.waitForRequest(
    (r) => r.url().includes('/api/v1/osc/jobs') && r.method() === 'POST')

  // 没有可选实例时下拉只有一个 "—",这条用例关心的是**表单到请求**这一段,
  // 所以直接填一个实例 id 进去就够了 —— 选项从哪来由连接列表决定,不在这里。
  await page.evaluate(() => {
    const sel = document.querySelector('.c-modal select') as HTMLSelectElement
    const opt = document.createElement('option')
    opt.value = '1'
    sel.appendChild(opt)
    sel.value = '1'
    sel.dispatchEvent(new Event('change', { bubbles: true }))
  })
  const inputs = page.locator('.c-modal input')
  await inputs.nth(0).fill('app')
  await inputs.nth(1).fill('t_order')
  await inputs.nth(2).fill('ADD INDEX idx_memo (memo)')

  await expect(go).toBeEnabled()
  await go.click()
  const req = await posted
  expect(req.postDataJSON()).toMatchObject({
    connectionId: 1, schema: 'app', table: 't_order', alter: 'ADD INDEX idx_memo (memo)',
  })
})

test('半途失败留下的影子表排在列表前面,而且说得出是哪一张', async ({ page }) => {
  await openOsc(page, { enabled: true, jobs: [JOBS.live, JOBS.stranded] })

  const box = page.locator('.osc-stranded')
  await expect(box).toBeVisible()
  await expect(box).toContainText('app.t_user_gho')   // 库里躺着的那张表,原样给出
  await expect(box).toContainText('#11')
  // 还在跑的那条不是残局 —— 它有人管。
  await expect(box).not.toContainText('t_order_gho')

  // 残留块要排在历史列表**之前**:它是唯一需要人现在动手的东西。
  const strandedY = await box.boundingBox()
  const listY = await page.locator('.osc-list').boundingBox()
  expect(strandedY!.y).toBeLessThan(listY!.y)
})

// 网关重启之后,库里那条 copying 记录的状态和"正在跑"一模一样。把它当成有人在管,
// 那张影子表就会一直躺到磁盘报警;给它一个中止按钮,按下去只会回一句"任务不在运行中",
// 而人会以为自己已经止住了它。
test('重启后没人推进的任务进残局清单,而且不给中止按钮', async ({ page }) => {
  await openOsc(page, {
    enabled: true,
    jobs: [{ ...JOBS.live, running: false }],
  })

  await expect(page.locator('.osc-stranded')).toContainText('t_order_gho')
  await expect(page.locator('.osc-item .osc-orphan')).toBeVisible()
  await expect(page.locator('.osc-item button:has-text("中止")')).toHaveCount(0)
  // 也不该画一根正在推进的进度条。
  await expect(page.locator('.osc-bar')).toHaveCount(0)
})

test('在途任务给得出中止,终态任务不给', async ({ page }) => {
  await openOsc(page, { enabled: true, jobs: [JOBS.live, JOBS.stranded] })

  const rows = page.locator('.osc-item')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0).locator('button:has-text("中止")')).toBeVisible()
  await expect(rows.nth(1).locator('button:has-text("中止")')).toHaveCount(0)

  const aborted = page.waitForRequest(
    (r) => r.url().includes('/api/v1/osc/jobs/12/abort') && r.method() === 'POST')
  await rows.nth(0).locator('button:has-text("中止")').click()
  await aborted
})

// 估算行数为 0 时不能画出一根看起来完成了的进度条 —— 那是最危险的一种误报。
test('总行数未知时写"未知",不画进度条', async ({ page }) => {
  await openOsc(page, {
    enabled: true,
    jobs: [{ ...JOBS.live, totalRows: 0, copiedRows: 777 }],
  })
  await expect(page.locator('.osc-unknown')).toContainText('777')
  await expect(page.locator('.osc-bar')).toHaveCount(0)
})

test('限流没开起来的任务,页面上挂着那句留痕', async ({ page }) => {
  // 这句话不能只落在库里。事后追查"那次把从库拖垮的迁移,当时限流开着吗",
  // 人是到这张页面上来看的 —— 而限流开没开,两种任务长得一模一样。
  await openOsc(page, { enabled: true, jobs: [JOBS.live, JOBS.stranded] })

  const warned = page.locator('.osc-throttle.warn')
  await expect(warned).toHaveCount(1)
  await expect(warned).toContainText('未启用')
  // 限流开着的那条不该也被标成警示 —— 警示一旦乱响就没人看了。
  await expect(page.locator('.osc-throttle:not(.warn)')).toContainText('已启用')
})
