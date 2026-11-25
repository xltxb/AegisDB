import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 确认弹窗(ConfirmDialog)替代了浏览器原生 confirm(2026-10-29):原生框顶着"aegisdb.xxx.com 显示",
// 命令挤在一行里看不清。钉住:文案里「…」的命令按等宽高亮;取消 / Esc / 点遮罩都不执行;确定 / Enter 才执行。

const AP = {
  id: 1, apNo: 'AP-7002', type: 'cmd', connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'tidb-prod-c66-dc', database: 'INFORMATION_SCHEMA',
  command: "GRANT SELECT ON `c66_dc`.* TO 'dbgw_readonly'@'%';", initiator: 'Lin Wei', initiatorId: 1, reason: '', riskLevel: 'high',
  status: 'approved', createdAt: '2026-10-29T05:18:00Z', result: '· 已批准,等待发起人执行', resultRows: 0, canExecute: true, executedAt: null, execStatus: '',
}

test('执行审批单:样式化确认框显示命令;取消、Esc、点遮罩都不执行,确定才执行', async ({ page }) => {
  const executes: string[] = []
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [AP], total: 1, pending: 0, page: 1, pageSize: 20 })))
  await page.route('**/api/v1/approvals/1/execute', (r) => { executes.push(r.request().url()); return r.fulfill(envelope({ risk: 'high', output: 'Query OK', rows: 0 })) })
  let nativeDialog = false
  page.on('dialog', (d) => { nativeDialog = true; void d.dismiss() })
  await page.goto('/#/approvals')

  const run = page.locator('.rowbtn.run')
  const dlg = page.getByTestId('confirm-dialog')
  await run.click()
  await expect(dlg).toBeVisible()
  await expect(dlg).toContainText('请确认操作')
  await expect(dlg.locator('.cf-code')).toHaveText("GRANT SELECT ON `c66_dc`.* TO 'dbgw_readonly'@'%';")
  await expect(page.getByTestId('confirm-message')).toContainText('只能执行一次')
  await page.getByTestId('confirm-cancel').click()
  await expect(dlg).toHaveCount(0)

  await run.click()
  await page.keyboard.press('Escape')
  await expect(dlg).toHaveCount(0)

  await run.click()
  await page.locator('.cf-mask').click({ position: { x: 10, y: 10 } })
  await expect(dlg).toHaveCount(0)
  await page.waitForTimeout(200)
  expect(executes).toEqual([])

  await run.click()
  await expect(page.getByTestId('confirm-ok')).toBeFocused()
  await page.keyboard.press('Enter')
  await expect.poll(() => executes.length).toBe(1)
  expect(nativeDialog).toBe(false)
})
