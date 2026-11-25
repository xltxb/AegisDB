import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 审计页上的**入口来源**(迁移 0063)。
//
// 起因:同样是一条 SELECT,有的来自数据查询页、有的来自 Web 命令行,表格里完全看不出
// 区别。来源是写行时由服务端按路由 / 入口盖上并进哈希链的一列,页面只负责如实画出来。
//
// 钉四条:
//   1. 每行画出自己的来源;0063 之前的旧行显示「未知 / 旧记录」,不替它猜;
//   2. 来源筛选发到服务端(?source=),多选逗号分隔,按「查询」才生效;
//   3. 「未知 / 旧记录」也能单独筛(source=unknown);
//   4. 导出链接带上来源条件;详情抽屉里也有来源。

const ROWS = [
  { id: 1, command: "select * from msg_sms_send_record where msg_id like '2026%'", source: 'query' },
  { id: 2, command: 'select count(1) from msg_sms_send_record', source: 'terminal' },
  { id: 3, command: 'EXPORT SELECT market_type FROM t_player', source: 'export' },
  { id: 4, command: 'login via sso', source: 'auth' },
  { id: 5, command: 'SELECT 1', source: '' }, // 0063 之前的旧行
].map((r) => ({
  occurredAt: '2026-11-11T06:59:01Z', actor: 'Heng.Zhang', instance: 'tidb-prod-message-center',
  database: 'c66_msg_center', approvalNo: '', hash: 'deadbeef', clientIp: '10.54.5.72',
  risk: 'low', result: 'executed', eventType: 'exec', dataAction: 'read', ...r,
}))

async function openAudit(page: Page) {
  const urls: string[] = []
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/audit/instances'), (r) =>
    r.fulfill(envelope(['tidb-prod-message-center'])))
  await page.route((u) => u.pathname.endsWith('/api/v1/audit'), (r) => {
    const src = new URL(r.request().url()).searchParams.get('source') ?? ''
    urls.push(src)
    const want = src ? src.split(',') : []
    // 与服务端同一条语义:unknown = source 为空的旧行。
    const items = want.length
      ? ROWS.filter((x) => want.includes(x.source || 'unknown'))
      : ROWS
    return r.fulfill(envelope({ items, total: items.length, page: 1, pageSize: 50 }))
  })
  await page.goto('/#/admin/audit')
  await expect(page.locator('.tr').first()).toBeVisible()
  return urls
}

const sourceSelect = (page: Page) => page.getByTestId('audit-source-filter')
const search = (page: Page) => page.getByTestId('audit-search')

async function pick(page: Page, label: string) {
  const sel = sourceSelect(page)
  await sel.locator('.control').click()
  await sel.locator('.opt', { hasText: label }).first().click()
  await sel.locator('.control').click()
}

test.describe('审计日志 · 入口来源', () => {
  test('同样是 SELECT,数据查询与 Web 命令行一眼分开;旧行显示未知 / 旧记录', async ({ page }) => {
    await openAudit(page)
    const rows = page.locator('.tr')
    await expect(rows).toHaveCount(5)
    await expect(rows.nth(0).getByTestId('audit-source')).toHaveText('数据查询')
    await expect(rows.nth(1).getByTestId('audit-source')).toHaveText('Web 命令行')
    await expect(rows.nth(2).getByTestId('audit-source')).toHaveText('数据导出')
    await expect(rows.nth(3).getByTestId('audit-source')).toHaveText('登录认证')
    await expect(rows.nth(4).getByTestId('audit-source')).toHaveText('未知 / 旧记录')
    await expect(rows.nth(4).getByTestId('audit-source')).toHaveClass(/src-none/)
    // 表头有这一列
    await expect(page.locator('.thead')).toContainText('来源')
  })

  test('按来源筛:发到服务端,多选逗号分隔,按查询才生效', async ({ page }) => {
    const urls = await openAudit(page)
    expect(urls[0]).toBe('')
    await pick(page, '数据查询')
    expect(urls[urls.length - 1]).toBe('') // 没按查询,不发
    await expect(page.locator('.tr')).toHaveCount(5)
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('query')
    await expect(page.locator('.tr')).toHaveCount(1)

    await pick(page, 'Web 命令行')
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('query,terminal')
    await expect(page.locator('.tr')).toHaveCount(2)

    await page.getByRole('button', { name: /重置/ }).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('')
    await expect(page.locator('.tr')).toHaveCount(5)
  })

  test('旧记录也能单独筛出来', async ({ page }) => {
    const urls = await openAudit(page)
    await pick(page, '未知 / 旧记录')
    await search(page).click()
    await expect.poll(() => urls[urls.length - 1]).toBe('unknown')
    await expect(page.locator('.tr')).toHaveCount(1)
    await expect(page.locator('.tr').first()).toContainText('SELECT 1')
  })

  test('导出链接带上来源;详情里有来源', async ({ page }) => {
    await openAudit(page)
    await pick(page, '数据导出')
    await search(page).click()
    await expect(page.locator('.tr')).toHaveCount(1)
    let exportURL = ''
    await page.route((u) => u.pathname.endsWith('/api/v1/audit/export'), (r) => {
      exportURL = r.request().url()
      return r.fulfill({ status: 200, contentType: 'text/csv', body: 'time,actor\n' })
    })
    await page.getByRole('button', { name: /导出/ }).click()
    await expect.poll(() => exportURL).toContain('source=export')

    await page.locator('.tr').first().click()
    await expect(page.getByTestId('audit-detail-source')).toHaveText('数据导出')
  })
})
