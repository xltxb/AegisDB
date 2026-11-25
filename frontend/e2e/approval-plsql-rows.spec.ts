import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// Oracle 的 PL/SQL 匿名块不报影响行数:审批单存 -1,详情里显示"PL/SQL 块不返回影响行数",
// 不显示一个错的"影响 0 行"(2026-10 生产 AP-2559)。普通语句照旧显示行数。

const done = (resultRows: number, command: string) => ({
  id: 1, apNo: 'AP-2559', type: 'cmd', connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-g16-h01', database: 'G16GCI',
  command, initiator: 'garret', initiatorId: 1, reason: '工单', riskLevel: 'high', status: 'approved',
  createdAt: '2026-10-13T09:00:00Z', executedAt: '2026-10-13T09:40:00Z', execStatus: 'success', result: '执行成功', resultRows,
  canExecute: false, steps: [],
})

async function open(page: Page, ap: ReturnType<typeof done>) {
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [ap], total: 1, pending: 0, page: 1, pageSize: 20 })))
  await page.goto('/#/approvals')
  await page.locator('.tr').first().click()
}

test('PL/SQL 块:显示「不返回影响行数」,不显示影响 0 行', async ({ page }) => {
  await open(page, done(-1, "BEGIN\ndelete from T_AGENT_CONFIG where PRODUCT_ID in ('C70');\nCOMMIT;\nEND;"))
  await expect(page.getByTestId('ap-result-rows')).toHaveText('PL/SQL 块不返回影响行数')
})

test('普通语句:照旧显示影响行数(包括真的 0 行)', async ({ page }) => {
  await open(page, done(0, "DELETE FROM t WHERE id = -1"))
  await expect(page.getByTestId('ap-result-rows')).toHaveText('影响 0 行')
})
