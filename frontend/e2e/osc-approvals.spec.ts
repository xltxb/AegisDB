import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, autoConfirm } from './helpers'

// 在线变更工单(生产大表加 / 删索引)在审批页的样子。钉:
//   1. 类型徽标「在线变更」;详情里有进度面板:进度条、阶段、已拷贝 / 已重放;
//   2. 有权的人在执行中能暂停 / 中止(中止要确认),请求打到 /osc/jobs/:id/:action;
//   3. 结束后提示保留的旧表需 DBA 手动删;
//   4. 大表直接 DDL 的工单上有醒目提示。

const AP = {
  id: 1, apNo: 'AP-8001', type: 'osc', connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders',
  command: 'ALTER TABLE orders ADD INDEX idx_created (created_at)', initiator: 'Lin Wei', initiatorId: 1, reason: '', riskLevel: 'high',
  status: 'approved', createdAt: '2026-10-01T08:00:00Z', result: '', resultRows: 0, canExecute: false,
  executedAt: '2026-10-01T09:00:00Z', execStatus: 'running', oscJobId: 7,
}
const JOB = {
  id: 7, connectionId: 1, instance: 'prod-order-01', database: 'orders', table: 'orders', statement: AP.command,
  initiator: 'Lin Wei', sizeRows: 2_000_000, sizeBytes: 3 * (1 << 30), status: 'running', phase: 'copying',
  rowsEstimated: 2_000_000, rowsCopied: 500_000, chunks: 500, rowsReplayed: 1234, cutOverAttempts: 0,
  startedAt: new Date(Date.now() - 90_000).toISOString(),
  throttled: true, throttleReason: '从库延迟 30 秒,超过阈值 10 秒', paused: false, canControl: true,
  warnings: JSON.stringify([{ code: 'long_transaction', reason: '当前有已运行 120 秒的事务:cut-over 需要短暂持锁' }]),
}

async function open(page: Page, ap: any, job: any) {
  const actions: string[] = []
  let state = { ...job }
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [ap], total: 1, pending: 0, page: 1, pageSize: 20 })))
  await page.route('**/api/v1/osc/jobs/7', (r) => r.fulfill(envelope(state)))
  await page.route(/\/api\/v1\/osc\/jobs\/7\/(pause|resume|abort|continue)$/, (r) => {
    const a = r.request().url().split('/').pop()!
    actions.push(a)
    if (a === 'pause') state = { ...state, paused: true }
    if (a === 'continue') state = { ...state, status: 'running', error: '' }
    if (a === 'abort') state = { ...state, status: 'aborted', phase: 'aborted', error: '已中止,影子表已清理,原表未动' }
    return r.fulfill(envelope({ ok: true }))
  })
  await autoConfirm(page)
  await page.goto('/#/approvals')
  return { actions, setState: (s: any) => { state = { ...state, ...s } } }
}

test('在线变更工单:类型徽标、进度面板、暂停与中止', async ({ page }) => {
  const { actions } = await open(page, AP, JOB)
  await expect(page.locator('.tybadge').first()).toHaveText('在线变更')
  await page.locator('.tr').first().click()
  const panel = page.getByTestId('osc-panel')
  await expect(panel).toContainText('ALTER TABLE orders ADD INDEX idx_created')
  await expect(panel).toContainText('约 2,000,000 行 / 3.0 GB')
  await expect(panel).toContainText('当前有已运行 120 秒的事务') // 中文界面照引擎原话
  await expect(page.getByTestId('osc-pct')).toHaveText('25%')
  await expect(page.getByTestId('osc-stat')).toContainText('拷贝中')
  await expect(page.getByTestId('osc-stat')).toContainText('已重放 1,234 行')
  await expect(page.getByTestId('osc-stat')).toContainText('限流中')
  await expect(page.getByTestId('osc-times')).toContainText('已运行') // 执行中:没有结束时刻,说已运行多久
  await expect(page.getByTestId('osc-throttle')).toHaveText('限流中:从库延迟 30 秒,超过阈值 10 秒') // 说清为什么在等

  await page.getByTestId('osc-pause').click()
  await expect(page.getByTestId('osc-resume')).toBeVisible()
  await page.getByTestId('osc-abort').click()
  await expect(panel).toContainText('已中止,影子表已清理')
  expect(actions).toEqual(['pause', 'abort'])
})

test('完成后提示保留的旧表需 DBA 手动删;无权的人看不到控制按钮', async ({ page }) => {
  await open(page, { ...AP, execStatus: 'success' },
    { ...JOB, status: 'done', phase: 'done', rowsCopied: 2_000_000, oldTable: '_orders_del', canControl: false,
      startedAt: '2026-10-01T22:31:48+08:00', finishedAt: '2026-10-01T23:09:00+08:00' })
  await page.locator('.tr').first().click()
  await expect(page.getByTestId('osc-old')).toContainText('_orders_del')
  // 开始、结束时刻与用时(时刻随浏览器时区,只断言与时区无关的部分)
  await expect(page.getByTestId('osc-times')).toContainText('开始 10-01')
  await expect(page.getByTestId('osc-times')).toContainText('结束 10-01')
  await expect(page.getByTestId('osc-times')).toContainText('用时 37 分 12 秒')
  await expect(page.getByTestId('osc-pct')).toHaveText('100%')
  await expect(page.getByTestId('osc-abort')).toHaveCount(0)
})

test('英文界面:前置检查提示按代码翻译', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('vela_lang', 'en'))
  await open(page, AP, JOB)
  await page.locator('.tr').first().click()
  await expect(page.getByTestId('osc-panel')).toContainText('A long transaction is running')
  await expect(page.getByTestId('osc-panel')).not.toContainText('事务')
})

test('大表直接 DDL 的工单上有醒目提示', async ({ page }) => {
  await open(page, { ...AP, type: 'cmd', oscJobId: undefined, status: 'pending', execStatus: '', executedAt: null,
    command: 'ALTER TABLE orders ADD COLUMN memo varchar(64)',
    notice: '表 orders 约 2,000,000 行 / 3.0GB,这条 DDL 将直接执行(在线变更不支持:不是加索引 / 删索引),可能长时间锁表',
    noticeRef: { code: 'oscBigDirect', args: { table: 'orders', rows: '2,000,000', size: '3.0GB', why: '不是加索引 / 删索引' } } }, JOB)
  await page.locator('.tr').first().click()
  await expect(page.getByTestId('ap-notice')).toContainText('可能长时间锁表')
  await expect(page.getByTestId('osc-panel')).toHaveCount(0)
})

test('设置:在线变更阈值,GB 存成字节', async ({ page }) => {
  const saved: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'GET') return r.fulfill(envelope({ settings: { 'osc.minRows': '500000', 'osc.minBytes': String(2 * (1 << 30)) }, secretsSet: {} }))
    saved.push(r.request().postDataJSON())
    return r.fulfill(envelope({}))
  })
  await page.goto('/#/settings')
  await expect(page.getByTestId('set-osc-rows')).toHaveValue('500000')
  await expect(page.getByTestId('set-osc-gb')).toHaveValue('2')
  await page.getByTestId('set-osc-gb').fill('1.5')
  await page.locator('button', { hasText: '保存' }).first().click()
  await expect.poll(() => saved.length).toBeGreaterThan(0)
  expect(saved[saved.length - 1]).toMatchObject({ 'osc.minRows': 500000, 'osc.minBytes': Math.round(1.5 * (1 << 30)), 'osc.maxThreadsRunning': 50, 'osc.chunkSize': 1000 })
})

test('网关重启打断的在线变更:说明已保留进度,可续跑', async ({ page }) => {
  const { actions } = await open(page, AP, {
    ...JOB, status: 'interrupted', throttled: false, phase: 'copying',
    error: '网关重启,迁移中断;影子表与进度已保留,可在工单里「续跑」或「放弃」',
  })
  await page.locator('.tr').first().click()
  await expect(page.getByTestId('osc-interrupted')).toHaveText('已中断')
  await expect(page.getByTestId('osc-interrupted-hint')).toContainText('影子表与进度已保留')
  await expect(page.getByTestId('osc-pause')).toHaveCount(0) // 没在跑:不给暂停
  await page.getByTestId('osc-continue').click()
  await expect(page.getByTestId('osc-pause')).toBeVisible() // 续跑后回到运行中的控制
  expect(actions).toEqual(['continue'])
})
