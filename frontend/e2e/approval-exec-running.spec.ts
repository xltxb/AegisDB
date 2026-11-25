import { test, expect, type Page, type Route } from '@playwright/test'
import { envelope, stubCommon, autoConfirm } from './helpers'

// 审批单的执行状态以工单上落库的为准,不以"点了执行"为准。钉三件事:
//
//   1. 命令还在库上跑(执行请求没回):行上是「执行中」,不是「已执行」;
//   2. 跑完按实际结果显示:成功是「执行成功」,失败是「执行失败」;
//   3. 请求中途断了(代理超时),命令未必没跑:接着按工单状态轮询,出结论再提示。

const BASE = {
  id: 1, apNo: 'AP-7001', type: 'cmd', connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders',
  command: 'ALTER TABLE orders ADD COLUMN memo varchar(64);', initiator: 'Lin Wei', initiatorId: 1, reason: '', riskLevel: 'high',
  status: 'approved', createdAt: '2026-09-30T08:00:00Z', result: '· 已批准,等待发起人执行', resultRows: 0,
}

async function open(page: Page, onExecute: (r: Route, setState: (s: any) => void) => void) {
  let state: any = { ...BASE, canExecute: true, executedAt: null, execStatus: '' }
  const setState = (s: any) => { state = { ...state, ...s } }
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [state], total: 1, pending: 0, page: 1, pageSize: 20 })))
  await page.route('**/api/v1/approvals/1/execute', (r) => onExecute(r, setState))
  await autoConfirm(page)
  await page.goto('/#/approvals')
  await expect(page.locator('.stbadge').first()).toContainText('待执行')
}

const RUNNING = { canExecute: false, executedAt: '2026-09-30T09:00:00Z', execStatus: 'running' }

test('执行请求还没回:显示执行中;回来后按实际结果显示执行成功', async ({ page }) => {
  let release: () => void = () => {}
  await open(page, (r, setState) => {
    setState(RUNNING) // 服务端占位即写 running
    release = () => {
      setState({ execStatus: 'success', resultRows: 3, result: 'Query OK, 3 rows affected' })
      void r.fulfill(envelope({ risk: 'high', output: 'Query OK', rows: 3 }))
    }
  })
  await page.locator('.rowbtn.run').click()
  const badge = page.locator('.stbadge').first()
  await expect(badge).toHaveText('执行中')
  await expect(badge).toHaveClass(/running/)
  await expect(badge).not.toContainText('已执行')
  await page.locator('.tr').first().click() // 详情卡也说执行中,不把"等待发起人执行"当输出
  await expect(page.locator('.rhead')).toContainText('执行中')
  await expect(page.getByText('· 已批准,等待发起人执行')).toHaveCount(0)

  release()
  await expect(badge).toHaveText('执行成功')
  await expect(page.getByText('命令已执行完成')).toBeVisible()
})

test('请求中途断了:不当成失败,接着等工单上的结论;失败就说失败', async ({ page }) => {
  let finish: () => void = () => {}
  await open(page, (r, setState) => {
    setState(RUNNING)
    finish = () => setState({ execStatus: 'failed', result: '· 数据库执行失败: lock wait timeout' })
    void r.abort('timedout')
  })
  await page.locator('.rowbtn.run').click()
  await expect(page.getByText('命令仍在执行,结果出来后列表会自动更新')).toBeVisible()
  const badge = page.locator('.stbadge').first()
  await expect(badge).toHaveText('执行中')

  finish() // 命令在库上跑完了(失败)—— 页面靠轮询看到
  await expect(badge).toHaveText('执行失败', { timeout: 8000 })
  await expect(page.getByText('命令已下发,但目标库拒绝了它', { exact: false })).toBeVisible()
})
