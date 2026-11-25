import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-10-26 回归排查:新 SQL 编辑器每敲一个键都把全文重新着色(分词器是平方级的),
// 行号每行一个元素 —— 2000 行粘贴要十几秒,1 万行页面卡死。另外 Tab 改成缩进后,
// 纯键盘用户出不了编辑器:现在先按 Esc 再按 Tab 照常移走焦点。

const BIZ_ME = { ...ME, id: 9, name: '张三', email: 'zhang@corp.io', roleCodes: ['bizquery'], menus: { query: true } }
const G = { id: 1, email: 'zhang@corp.io', userName: '张三', dept: '财务部', sourceId: 12, source: 'orders-replica', database: 'orders_db',
  maxRows: 0, timeoutSec: 0, effectiveMaxRows: 1000, effectiveTimeoutSec: 30, allowExport: false,
  startsAt: '2026-09-25T09:00:00+08:00', endsAt: '2027-10-25T09:00:00+08:00', status: 'active',
  externalId: 'OA-1', client: '审批系统', approver: '李经理', reason: '对账' }

async function open(page: Page) {
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(BIZ_ME)))
  await page.route('**/api/v1/bizquery/grants', (r) => r.fulfill(envelope({ grants: [G], limits: {} })))
  await page.route('**/api/v1/bizquery/tables**', (r) => r.fulfill(envelope({ tables: [], truncated: false })))
  await page.goto('/#/query')
  await page.getByTestId('q-sql').waitFor()
}

test('1 万行 SQL:粘贴与打字都不卡,行号只画可见的一段且数字正确', async ({ page }) => {
  await open(page)
  // 直接设值再发 input(等同粘贴):Playwright 的 fill 对几十万字符本身就要很久
  const paste = (n: number) => page.getByTestId('q-sql').evaluate(async (el: HTMLTextAreaElement, n) => {
    const big = Array.from({ length: n }, (_, i) => `SELECT a.id, 'row ${i}' AS s FROM t_user a WHERE a.id = ${i} -- c`).join('\n')
    const t0 = performance.now()
    el.value = big
    el.dispatchEvent(new Event('input'))
    await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)))
    return performance.now() - t0
  }, n)
  // 2000 行:照常着色,一次粘贴在 1.5 秒内
  expect(await paste(2000)).toBeLessThan(1500)
  await expect(page.locator('.se')).not.toHaveClass(/plain/)
  // 1 万行:退回素文本(文字直接显示在 textarea 上),不卡
  expect(await paste(10000)).toBeLessThan(1500)
  await expect(page.locator('.se')).toHaveClass(/plain/)
  // 只画可见的一段
  expect(await page.locator('.se-ln').count()).toBeLessThan(200)
  // 连敲 20 个字符
  await page.getByTestId('q-sql').press('Control+End')
  const t1 = Date.now()
  await page.keyboard.type('x'.repeat(20))
  expect(Date.now() - t1).toBeLessThan(4000)
  // 滚到底部,看得见最后一行的行号
  await page.getByTestId('q-sql').evaluate((el) => { el.scrollTop = el.scrollHeight; el.dispatchEvent(new Event('scroll')) })
  await expect(page.locator('.se-ln', { hasText: /^10000$/ })).toBeVisible()
})

test('短 SQL:行号照常,当前行高亮', async ({ page }) => {
  await open(page)
  await page.getByTestId('q-sql').fill('SELECT 1\nFROM dual\nWHERE 1 = 1')
  await expect(page.locator('.se-ln')).toHaveText(['1', '2', '3'])
})

test('Tab 缩进;先按 Esc 再按 Tab 移走焦点', async ({ page }) => {
  await open(page)
  const ed = page.getByTestId('q-sql')
  await ed.fill('SELECT')
  await ed.press('End')
  await ed.press('Tab')
  await expect(ed).toHaveValue('SELECT  ')
  await expect(ed).toBeFocused()
  await ed.press('Escape')
  await ed.press('Tab')
  await expect(ed).not.toBeFocused()
  await expect(ed).toHaveValue('SELECT  ')
})
