import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 数据查询权限申请单(type=grant,issue #173)在审批列表上。钉住:
//   · 带「权限申请」类型标签,命令栏按 commandRef 渲染描述;
//   · 批准后不是「待执行」、没有执行按钮 —— 批准即生成授权,与窗口单同一类;
//   · 详情里说「查询授权已生效」,输出按 resultRef(apGrantLive)按读者语言显示;
//   · 类型筛选里有它,筛选发到服务端(type=grant)。

const GRANT = {
  id: 7, apNo: 'AP-9201', type: 'grant', queryGrantReqId: 3, connectionId: 12, env: 'prod', tierCode: 'prod',
  instance: 'orders-replica', database: 'orders_db',
  command: '申请数据查询权限 · orders-replica/orders_db · 14 天 · 理由:月底对账',
  commandRef: { code: 'apGrantApply', args: { scope: 'orders-replica', db: 'orders_db', days: '14', reason: '月底对账' } },
  keyword: '', initiatorId: 9, initiator: '张三', reason: '月底对账', riskLevel: 'mid', status: 'approved',
  createdAt: '2026-11-27T08:00:00Z', steps: [], canDecide: false, canExecute: false, canCancel: false,
  executedAt: null, execStatus: '', result: '· 已批准,查询授权已生效', resultRef: JSON.stringify({ code: 'apGrantLive' }), resultRows: 0,
}

async function open(page: import('@playwright/test').Page, lang?: 'en') {
  const types: string[] = []
  await stubCommon(page)
  if (lang) await page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) => {
    types.push(new URL(r.request().url()).searchParams.get('type') ?? '')
    return r.fulfill(envelope({ items: [GRANT], total: 1, pending: 0, page: 1, pageSize: 20 }))
  })
  await page.goto('/#/approvals')
  await expect(page.locator('.tr').first()).toBeVisible()
  return types
}

test('权限申请单:有类型标签,批准后不是待执行、没有执行按钮,详情说已生效', async ({ page }) => {
  const types = await open(page)
  const row = page.locator('.tr', { hasText: 'AP-9201' })
  await expect(row.locator('.tybadge')).toHaveText('权限申请')
  await expect(row.locator('.stbadge')).not.toContainText('待执行')
  await expect(row.locator('.rowbtn.run')).toHaveCount(0)

  // 类型筛选:选「权限申请」,请求带 type=grant
  const typeSelect = page.locator('.chead .vsel').nth(1)
  await typeSelect.locator('.control').click()
  await typeSelect.locator('.opt', { hasText: '权限申请' }).click()
  await expect.poll(() => types[types.length - 1]).toBe('grant')

  await row.click()
  await expect(page.getByTestId('ap-grant-live')).toHaveText('查询授权已生效 · 无需手动执行')
  await expect(page.getByRole('button', { name: '执行', exact: true })).toHaveCount(0)
  await expect(page.getByText('申请数据查询权限 · orders-replica/orders_db · 14 天').first()).toBeVisible()
})

test('英文界面:类型标签与描述按读者语言显示', async ({ page }) => {
  await open(page, 'en')
  const row = page.locator('.tr', { hasText: 'AP-9201' })
  await expect(row.locator('.tybadge')).toHaveText('Access request')
  await row.click()
  await expect(page.getByTestId('ap-grant-live')).toContainText('Query access is live')
  await expect(page.getByText('Request data query access · orders-replica/orders_db · 14 days').first()).toBeVisible()
})
