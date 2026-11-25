import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// AI 巡检报告 · 工单 04(.scratch/inspection-report):实例列表、报告历史、沙箱查看器。

const TODAY = '2026-10-15'
const rep = (id: number, connectionId: number, instanceName: string, reportDate: string, level: string, c = 0, w = 0) => ({
  id, connectionId, instanceName, reportDate, level, score: level === 'critical' ? 40 : level === 'warning' ? 75 : 96,
  criticalCount: c, warningCount: w, infoCount: 1, sizeBytes: 1000, updatedAt: '2026-10-15T03:00:00Z',
})
const LATEST = {
  today: TODAY,
  items: [
    { connectionId: 1, instance: 'prod-ok', env: 'prod', engine: 'MySQL 8.0', report: rep(11, 1, 'prod-ok', TODAY, 'ok') },
    { connectionId: 2, instance: 'prod-bad', env: 'prod', engine: 'MySQL 8.0', report: rep(12, 2, 'prod-bad', TODAY, 'critical', 2, 1) },
    { connectionId: 3, instance: 'prod-stale', env: 'prod', engine: 'Oracle', report: rep(13, 3, 'prod-stale', '2026-10-14', 'ok') },
    { connectionId: 4, instance: 'prod-never', env: 'prod', engine: 'TiDB', report: null },
    { connectionId: 5, instance: 'prod-warn', env: 'prod', engine: 'MySQL 8.0', report: rep(15, 5, 'prod-warn', TODAY, 'warning', 0, 3) },
  ],
}
const ADMIN = { ...ME, menus: { ...ME.menus, inspection: true } }

// 报告里一段"不怀好意"的脚本:想读平台的登录凭证、改父页面、带着凭证调平台接口。
// 在 sandbox="allow-scripts"(没有 allow-same-origin)里,这些都应做不到。
const EVIL = `<!doctype html><html><body><h1>巡检报告</h1><div id="out">running</div><script>
  var r = [];
  try { r.push('ls:' + (localStorage.getItem('vela_session') || 'none')) } catch (e) { r.push('ls:blocked') }
  try { parent.document.title = 'HACKED'; r.push('parent:changed') } catch (e) { r.push('parent:blocked') }
  try { fetch('/api/v1/inspection-hack', { headers: { 'X-Stolen': (function () { try { return localStorage.getItem('vela_session') } catch (e) { return 'none' } })() || 'none' } }).catch(function () {}) } catch (e) {}
  document.getElementById('out').textContent = r.join(' ');
</script></body></html>`

async function open(page: Page, path: string, html = '<!doctype html><h1 id="h">报告正文</h1>') {
  const hack: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ADMIN)))
  await page.route('**/api/v1/inspection-reports/latest', (r) => r.fulfill(envelope(LATEST)))
  await page.route((u) => u.pathname.endsWith('/api/v1/inspection-reports'), (r) =>
    r.fulfill(envelope({ items: [rep(12, 2, 'prod-bad', TODAY, 'critical', 2, 1), rep(10, 2, 'prod-bad', '2026-10-14', 'warning', 0, 2)], total: 2 })))
  await page.route('**/api/v1/inspection-reports/12', (r) => r.fulfill(envelope({
    ...rep(12, 2, 'prod-bad', TODAY, 'critical', 2, 1),
    findings: [
      { severity: 'warning', category: '索引', title: 't_user 缺少索引' },
      { severity: 'critical', category: '分区', title: 't_order 分区将用尽', object: 'shop.t_order' },
    ],
  })))
  await page.route('**/api/v1/inspection-reports/12/html', (r) => r.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', body: html }))
  await page.route('**/api/v1/inspection-reports/12/view', (r) => r.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: html }))
  await page.route('**/api/v1/inspection-hack', (r) => { hack.push(r.request().headers()['x-stolen'] || ''); return r.fulfill({ status: 200, body: '' }) })
  await page.goto(path)
  return hack
}

test('实例列表:严重 > 警告 > 今天未收到 > 正常;筛选与搜索', async ({ page }) => {
  await open(page, '/#/inspection')
  const rows = page.getByTestId('insp-row')
  await expect(rows).toHaveCount(5)
  await expect(rows.nth(0)).toContainText('prod-bad')
  await expect(rows.nth(1)).toContainText('prod-warn')
  // 今天未收到:最新一份不是今天的、从没收到过的
  await expect(rows.nth(2)).toContainText('今天未收到')
  await expect(rows.nth(3)).toContainText('今天未收到')
  await expect(rows.nth(4)).toContainText('prod-ok')
  await expect(page.getByTestId('insp-summary')).toContainText('今天已收到 3 / 5')
  await page.getByTestId('insp-filter-missing').click()
  await expect(rows).toHaveCount(2)
  await page.getByTestId('insp-filter-all').click()
  await page.getByTestId('insp-search').fill('warn')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('prod-warn')
})

test('进入实例历史,再打开报告:问题摘要严重的在前,报告在只给 allow-scripts 的沙箱里', async ({ page }) => {
  await open(page, '/#/inspection')
  await page.getByTestId('insp-row').filter({ hasText: 'prod-bad' }).click()
  await expect(page).toHaveURL(/instance=2/)
  const hist = page.getByTestId('insp-history')
  await expect(hist).toHaveCount(2)
  await expect(hist.first()).toContainText(TODAY)
  await hist.first().click()
  await expect(page).toHaveURL(/report=12/)
  const viewer = page.getByTestId('insp-viewer')
  await expect(viewer).toContainText('prod-bad · ' + TODAY)
  await expect(viewer.locator('.frow').first()).toContainText('t_order 分区将用尽')
  const frame = page.getByTestId('insp-frame')
  await expect(frame).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(page.frameLocator('[data-testid=insp-frame]').locator('#h')).toHaveText('报告正文')
})

test('报告里的脚本拿不到登录凭证、动不了父页面', async ({ page }) => {
  const hack = await open(page, '/#/inspection?instance=2&report=12', EVIL)
  const out = page.frameLocator('[data-testid=insp-frame]').locator('#out')
  await expect(out).not.toHaveText('running')
  await expect(out).toContainText('ls:blocked')
  await expect(out).toContainText('parent:blocked')
  expect(await page.title()).not.toContain('HACKED')
  await page.waitForTimeout(500)
  for (const h of hack) expect(h).not.toContain('e2e-token')
})

test('没有巡检报告菜单的角色看不到入口', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, id: 6, roleCode: 'ro', roleCodes: ['ro'], menus: { terminal: true } })))
  await page.goto('/#/dashboard')
  await expect(page.locator('.rail-item[title="巡检"]')).toHaveCount(0)
  await page.goto('/#/inspection')
  await expect(page).toHaveURL(/#\/dashboard/)
})
