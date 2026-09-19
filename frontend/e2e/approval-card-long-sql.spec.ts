import { test, expect, type Page } from '@playwright/test'
import { envelope, stubTerminal } from './fixtures'
import { installWsFake } from './wsFake'

// 高危拦截卡:命令再长,"为什么被拦"和"在哪填原因"也必须在卡片打开的那一刻就
// 看得见。
//
// 这张卡片只做三件事:说清命中了什么规则、收一个原因、把单子提上去。命令块没有
// 高度上限时,一条 30 行的批量 DROP 就有 622px —— 比整张卡的可视区(615px)还高,
// 于是规则与原因框双双落在折叠之下。打开卡片看到的是满屏 SQL,而人恰恰是为了
// 另外两件事才打开它的。
//
// 卡片自己是能滚的(.c-modal-body overflow:auto,实测需滚 336px),所以这不是
// "滚不动",是"要滚才知道下面还有东西" —— 在一个必须填字才能提交的卡片上,
// 这个差别就是提不提得上去。

const LONG_SQL = Array.from({ length: 30 }, (_, i) =>
  `DROP TABLE IF EXISTS archive_orders_2024_${String(i).padStart(2, '0')}_partition_by_region;`,
).join('\n')

/** 这一条命中高危,要走审批。 */
async function stubIntercept(page: Page) {
  await page.route('**/api/v1/risk/check', (r) =>
    r.fulfill(envelope({
      risk: 'high', action: 'approve', requiresApproval: true,
      matchedRule: '高危命令字典 · PROD 禁止直接执行', matchedRuleRef: null,
      command: 'DROP', approvalNo: '', auditId: '',
    })))
}

/** 元素完整落在滚动容器的可视矩形里 —— 不用滚就看得见。 */
async function visibleWithoutScrolling(page: Page, sel: string) {
  return page.evaluate((s) => {
    const body = document.querySelector('.c-modal-body')
    const el = document.querySelector(s)
    if (!body || !el) return null
    const b = body.getBoundingClientRect(), r = el.getBoundingClientRect()
    return r.top >= b.top - 1 && r.bottom <= b.bottom + 1
  }, sel)
}

test('命令很长时,拦截卡的命中规则与原因框仍在打开时就可见', async ({ page }) => {
  await installWsFake(page)
  await stubTerminal(page)
  await stubIntercept(page)
  await page.goto('/terminal')
  await expect(page.locator('.tv-tree')).toBeVisible()
  await expect(page.locator('.xterm-rows')).toContainText('# 语句以 ; 结束并执行', { timeout: 10_000 })

  // 走粘贴弹窗提交:30 行一次性贴进去,正是这张卡片会遇到的长命令。
  await page.getByRole('button', { name: '粘贴 SQL' }).click()
  await page.locator('textarea.paste-box').fill(LONG_SQL)
  await page.locator('.c-modal button.c-btn.v-primary').click()

  const modal = page.locator('.c-modal')
  await expect(modal).toContainText('提交审批')
  await expect(modal.locator('.notice.danger')).toContainText('高危命令字典')

  // 命令全文仍然拿得到 —— 限高不等于砍掉内容,它只是把滚动挪进命令块自己。
  const cmdScrollable = await page.evaluate(() => {
    const c = document.querySelector('.c-modal .cmd')
    return c ? c.scrollHeight > c.clientHeight : false
  })
  expect(cmdScrollable).toBe(true)

  // 这两条是这张卡片存在的理由,不该要滚一下才发现。
  expect(await visibleWithoutScrolling(page, '.c-modal .notice.danger')).toBe(true)
  expect(await visibleWithoutScrolling(page, '.c-modal textarea')).toBe(true)

  // 卡片整体不再需要滚:该滚的是命令块,不是这张卡。
  const bodyOverflow = await page.evaluate(() => {
    const b = document.querySelector('.c-modal-body')!
    return b.scrollHeight - b.clientHeight
  })
  expect(bodyOverflow).toBeLessThanOrEqual(1)
})
