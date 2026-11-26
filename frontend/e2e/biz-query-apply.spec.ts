import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, autoConfirm, ME } from './helpers'

// 在网关里申请数据查询权限(issue #173)。钉住:
//   · applyMode=internal(默认):拦截卡片上的「去申请权限」在本页弹出申请框,提交的请求体
//     就是选的实例 / 库 / 天数 / 导出 / 理由;服务端的报错原样显示;提交后说清楚在等审批
//   · applyMode=external:照旧是外部链接,不弹申请框
//   · 已有授权的人在工具条上也能申请更多库
//   · 我的申请:待审批的可以撤回

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = (id: number, db: string) => ({
  id, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: db,
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2099-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账',
})
const REQ = (id: number, over: Record<string, unknown> = {}) => ({
  id, sourceId: 12, source: 'orders-replica', database: 'orders_db', days: 14, allowExport: true, reason: '月底对账',
  apNo: `AP-80${id}`, status: 'pending', createdAt: '2026-11-27T09:00:00+08:00', ...over,
})
const SOURCES = [{ id: 12, name: 'orders-replica', engine: 'mysql', env: 'prod', status: 'online' }, { id: 13, name: 'crm-replica', engine: 'pg', env: 'prod', status: 'online' }]

async function open(page: Page, o: { grants?: any[]; limits?: Record<string, unknown>; mine?: () => any[] } = {}) {
  const posts: any[] = []
  const cancels: string[] = []
  await stubCommon(page)
  await autoConfirm(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({
    grants: o.grants ?? [], limits: { maxGrantDays: 30, exportEnabled: true, applyMode: 'internal', applyUrl: '', ...o.limits },
  })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.route('**/api/v1/bizquery/apply/sources', (r) => r.fulfill(envelope(SOURCES)))
  await page.route(/\/api\/v1\/bizquery\/apply\/sources\/\d+\/databases$/, (r) =>
    r.fulfill(envelope(r.request().url().includes('/12/') ? ['orders_db', 'finance_db'] : ['crm'])))
  await page.route('**/api/v1/bizquery/apply/mine', (r) => r.fulfill(envelope(o.mine ? o.mine() : [])))
  await page.route('**/api/v1/bizquery/apply', (r) => {
    const b = r.request().postDataJSON()
    posts.push(b)
    if (posts.length === 1 && b.reason === '重复') {
      return r.fulfill({ status: 200, contentType: 'application/json',
        body: JSON.stringify({ code: 40001, msg: '你对 orders-replica/orders_db 已有一张待审批的申请 AP-8001,请等它处理完或先撤回', data: null }) })
    }
    return r.fulfill(envelope(REQ(2, { ...b, apNo: 'AP-8002' })))
  })
  await page.route(/\/api\/v1\/bizquery\/apply\/\d+\/cancel$/, (r) => { cancels.push(r.request().url()); return r.fulfill(envelope(null)) })
  await page.goto('/#/query')
  return { posts, cancels }
}

test('网关内申请:拦截卡片弹出申请框,提交的就是选的实例 / 库 / 天数 / 导出 / 理由', async ({ page }) => {
  let mine: any[] = []
  const { posts } = await open(page, { mine: () => mine })
  const card = page.getByTestId('q-no-grants')
  await expect(card).toContainText('提交申请')
  const apply = page.getByTestId('q-apply')
  await expect(apply).not.toHaveAttribute('href') // 不再是外链
  await apply.click()

  const modal = page.getByTestId('q-apply-modal')
  await expect(modal).toBeVisible()
  await expect(page.getByTestId('q-apply-days')).toHaveValue('7') // 默认 7 天
  // 没填理由:前端先挡,不发请求
  await page.getByTestId('q-apply-source').selectOption('12')
  await page.getByTestId('q-apply-db').selectOption('finance_db')
  await page.getByTestId('q-apply-submit').click()
  await expect(page.getByTestId('q-apply-err')).toContainText('申请理由')
  expect(posts).toHaveLength(0)

  // 服务端拒绝:原样显示
  await page.getByTestId('q-apply-reason').fill('重复')
  await page.getByTestId('q-apply-submit').click()
  await expect(page.getByTestId('q-apply-err')).toContainText('已有一张待审批的申请 AP-8001')

  await page.getByTestId('q-apply-days').fill('14')
  await page.getByTestId('q-apply-export').check()
  await page.getByTestId('q-apply-reason').fill('月底对账')
  mine = [REQ(2, { database: 'finance_db', apNo: 'AP-8002' })]
  await page.getByTestId('q-apply-submit').click()
  await expect(page.getByTestId('q-apply-done')).toContainText('AP-8002')
  await expect(page.getByTestId('q-apply-done')).toContainText('等待审批')
  expect(posts[1]).toEqual({ sourceId: 12, database: 'finance_db', days: 14, allowExport: true, reason: '月底对账' })

  // 关掉申请框,拦截卡片上看得到这张待审批的申请
  await modal.getByRole('button', { name: '关闭' }).click()
  await expect(modal).toHaveCount(0)
  const item = card.getByTestId('q-apply-item')
  await expect(item).toContainText('待审批')
  await expect(item).toContainText('AP-8002')
  await expect(item).toContainText('finance_db')
})

test('导出总开关关着:申请框里没有「需要导出」', async ({ page }) => {
  await open(page, { limits: { exportEnabled: false } })
  await page.getByTestId('q-apply').click()
  await expect(page.getByTestId('q-apply-modal')).toBeVisible()
  await expect(page.getByTestId('q-apply-export')).toHaveCount(0)
})

test('外部申请:拦截卡片照旧是外部链接,不弹申请框', async ({ page }) => {
  await open(page, { limits: { applyMode: 'external', applyUrl: 'https://oa.example.com/apply' } })
  const apply = page.getByTestId('q-apply')
  await expect(apply).toHaveAttribute('href', 'https://oa.example.com/apply')
  await expect(apply).toHaveAttribute('target', '_blank')
  await expect(page.getByTestId('q-apply-mine')).toHaveCount(0)
})

test('已有授权:工具条上的「申请权限」打开申请框;外部模式下是链接', async ({ page }) => {
  await open(page, { grants: [G(1, 'orders_db')] })
  await expect(page.getByTestId('q-sql')).toBeVisible()
  await page.getByTestId('q-apply-more').click()
  await expect(page.getByTestId('q-apply-modal')).toBeVisible()
  await page.getByTestId('q-apply-source').selectOption('13')
  await expect(page.getByTestId('q-apply-db').locator('option')).toHaveText(['选择库', 'crm'])
})

test('已有授权且外部申请:工具条上是外部链接', async ({ page }) => {
  await open(page, { grants: [G(1, 'orders_db')], limits: { applyMode: 'external', applyUrl: 'https://oa.example.com/apply' } })
  await expect(page.getByTestId('q-sql')).toBeVisible()
  await expect(page.getByTestId('q-apply-more')).toHaveAttribute('href', 'https://oa.example.com/apply')
})

test('我的申请:待审批的可以撤回,撤回后状态刷新', async ({ page }) => {
  let mine = [REQ(5), REQ(4, { status: 'rejected', apNo: 'AP-804' })]
  const { cancels } = await open(page, { mine: () => mine })
  const items = page.getByTestId('q-no-grants').getByTestId('q-apply-item')
  await expect(items).toHaveCount(2)
  await expect(items.nth(1).getByTestId('q-apply-withdraw')).toHaveCount(0) // 已驳回的不能撤回
  mine = [REQ(5, { status: 'cancelled' }), mine[1]]
  await items.first().getByTestId('q-apply-withdraw').click()
  await expect.poll(() => cancels.length).toBe(1)
  expect(cancels[0]).toMatch(/\/bizquery\/apply\/5\/cancel$/)
  await expect(items.first()).toContainText('已撤回')
  await expect(items.first().getByTestId('q-apply-withdraw')).toHaveCount(0)
})
