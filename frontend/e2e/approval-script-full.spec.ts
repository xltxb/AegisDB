import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 脚本工单的审批人要能看到脚本全文。工单里只有摘要(前 40 行 + 语句清单),一条建表
// 语句就几十行,后面的 DDL 摘要里看不全 —— 审批人却要为每一条签字。

const SCRIPT = 'CREATE TABLE t_a (\n  id INT\n);\nCREATE TABLE t_b (\n  id INT\n);\nCREATE TABLE t_c (\n  id INT\n);\n'
const ROW = {
  id: 7, apNo: 'AP-9201', type: 'cmd', scriptUploadId: 31, riskLevel: 'mid', keyword: '\i',
  command: '\i ddl.sql\n-- 共 3 条语句 · 高危 0 · 需审批 3\nCREATE TABLE t_a (\n  id INT\n-- …… 以下省略 6 行,全文见所引用的脚本文件 ……',
  connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders',
  initiatorId: 2, initiator: 'Chen Hao', reason: '建表', status: 'pending',
  createdAt: '2026-10-08T08:00:00Z', steps: [], canDecide: true, canExecute: false, canCancel: false,
}

async function openDetail(page: Page, script: (r: import('@playwright/test').Route) => Promise<void>) {
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [ROW], total: 1, pending: 1, page: 1, pageSize: 20 })))
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals/7/script'), script)
  await page.goto('/#/approvals')
  await page.locator('.tr', { hasText: 'AP-9201' }).click()
  await expect(page.locator('.dcmd').first()).toContainText('以下省略')
}

test('脚本工单:查看完整脚本,能看到摘要里没有的每一条 DDL', async ({ page }) => {
  await openDetail(page, (r) => r.fulfill(envelope({ filename: 'ddl.sql', content: SCRIPT })))
  await page.getByTestId('ap-full-script').click()
  const full = page.getByTestId('ap-script-content')
  await expect(full).toContainText('CREATE TABLE t_b')
  await expect(full).toContainText('CREATE TABLE t_c')
})

test('脚本审查后被改过:不展示,说出原因', async ({ page }) => {
  await openDetail(page, (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40001, msg: '脚本文件在审批期间被修改,内容与评审时不一致,已拒绝执行', data: null }) }))
  await page.getByTestId('ap-full-script').click()
  await expect(page.getByTestId('ap-script-error')).toContainText('被修改')
  await expect(page.getByTestId('ap-script-content')).toHaveCount(0)
})

// issue #101:拉取全文途中换了一张单,旧响应回来后盖在新单上 —— 审批人照着别人的 SQL 批。
const OTHER = { ...ROW, id: 8, apNo: 'AP-9202', scriptUploadId: 0, keyword: 'DELETE', command: 'DELETE FROM t_other WHERE id = 1', initiator: 'Wang Wu' }
const OTHER_SCRIPT = { ...ROW, id: 9, apNo: 'AP-9203', scriptUploadId: 32, initiator: 'Zhao Liu' }

async function slowScript(page: Page, reply: (r: import('@playwright/test').Route) => Promise<void>) {
  let release = () => {}
  const gate = new Promise<void>((res) => { release = res })
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items: [ROW, OTHER, OTHER_SCRIPT], total: 3, pending: 3, page: 1, pageSize: 20 })))
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals/7/script'), async (r) => { await gate; await reply(r) })
  await page.goto('/#/approvals')
  await page.locator('.tr', { hasText: 'AP-9201' }).click()
  await page.getByTestId('ap-full-script').click()
  const switchTo = async (apNo: string) => {
    await page.locator('.dx').click()
    await page.locator('.tr', { hasText: apNo }).click()
    await expect(page.locator('.dhead .apno')).toHaveText('#' + apNo)
  }
  // 放行旧响应,并等页面消化完
  const respond = async () => {
    const done = page.waitForResponse('**/approvals/7/script')
    release()
    await done
    await page.waitForTimeout(200)
  }
  return { switchTo, respond }
}

test('拉取全文途中换到普通 SQL 单:显示的仍是它自己的命令', async ({ page }) => {
  const { switchTo, respond } = await slowScript(page, (r) => r.fulfill(envelope({ filename: 'ddl.sql', content: SCRIPT })))
  await switchTo('AP-9202')
  await respond()
  await expect(page.getByTestId('ap-script-content')).toHaveCount(0)
  await expect(page.locator('.dcmd').first()).toHaveText('DELETE FROM t_other WHERE id = 1')
})

test('拉取全文途中换到另一张脚本单:不带过去内容、报错和加载状态', async ({ page }) => {
  const { switchTo, respond } = await slowScript(page, (r) => r.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 40001, msg: '旧单的报错', data: null }) }))
  await switchTo('AP-9203')
  await expect(page.getByTestId('ap-full-script')).toBeEnabled() // 旧请求还没回来,新单的按钮不该是「加载中」
  await respond()
  await expect(page.getByTestId('ap-script-error')).toHaveCount(0)
})
