import { test, expect, type Page } from '@playwright/test'
import { envelope, ME, stubCommon } from './helpers'

// 接口报错按界面语言显示(apiMsg,.scratch/api-msg-i18n)。
//
// 报错的 msg 永远是服务端的中文原句;msgRef 是同一句话的 code。api/http 的拦截器在英文界面
// 把 msg 换成译文 —— 所有读 e.message / env.msg 的地方因此一处改动全部生效。这里用登录页的
// 红字验证这条通路:英文界面显示英文,中文界面一字不差显示原句,认不出的 code 保留原句。

const MFA_WRONG = { code: 'mc39510f0' } // "MFA 验证码错误"

function fail(code: number, msg: string, msgRef?: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify({ code, msg, msgRef, data: null }) }
}

async function openSsoMfa(page: Page, lang: 'zh' | 'en', msgRef: unknown = MFA_WRONG) {
  await page.addInitScript((l) => localStorage.setItem('aegisdb_lang', l), lang)
  await page.route('**/api/v1/auth/sso/config', (r) => r.fulfill(envelope({ enabled: true, loginUrl: 'api/v1/auth/oidc/start' })))
  await page.route('**/api/v1/auth/sso', (r) => {
    const body = r.request().postDataJSON() ?? {}
    return r.fulfill(body.mfaCode
      ? fail(42800, 'MFA 验证码错误', msgRef)
      : fail(42800, '请输入 MFA 验证码', { code: 'm9c27b238' }))
  })
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.goto('/#/login?sso=1') // 从 SSO 回调回来:登录页自动换会话,管理员要输码
  await page.getByTestId('sso-mfa-code').fill('000000')
  await page.getByTestId('sso-mfa-submit').click()
}

test.describe('接口报错 · 多语言', () => {
  test('英文界面:服务端的中文报错显示为英文', async ({ page }) => {
    await openSsoMfa(page, 'en')
    await expect(page.getByTestId('login-error')).toHaveText('Incorrect MFA code')
  })

  test('中文界面:显示服务端原句,一字不差', async ({ page }) => {
    await openSsoMfa(page, 'zh')
    await expect(page.getByTestId('login-error')).toHaveText('MFA 验证码错误')
  })

  test('认不出的 code(新后端、旧前端):保留中文原句,不露键名', async ({ page }) => {
    await openSsoMfa(page, 'en', { code: 'mffffffff' })
    await expect(page.getByTestId('login-error')).toHaveText('MFA 验证码错误')
  })
})

// 第二批:存库的提示文本(站内通知)按 ref 显示;老通知没有 bodyRef,正文显示中文原句。
async function openNotifs(page: Page, lang: 'zh' | 'en') {
  await page.addInitScript((l) => localStorage.setItem('aegisdb_lang', l), lang)
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 50 })))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope({
    unread: 2,
    items: [
      {
        id: 1, userId: 1, type: 'approval-rejected', title: '审批被拒绝', refNo: 'AP-1', read: false, createdAt: '2026-10-30T10:00:00+08:00',
        body: 'Zhang Wei 驳回了你的命令：DELETE FROM t',
        titleRef: JSON.stringify({ code: 'maaba03ff' }),
        bodyRef: JSON.stringify({ code: 'm082ca5d0', args: { 0: 'Zhang Wei', 1: 'DELETE FROM t' } }),
      },
      {
        id: 2, userId: 1, type: 'export-failed', title: '导出失败', refNo: '9', read: false, createdAt: '2026-10-29T10:00:00+08:00',
        body: '订单 · 旧的错误', titleRef: JSON.stringify({ code: 'm67533812' }),
      },
    ],
  })))
  await page.goto('/#/releases')
  await page.locator('.notif .bell').click()
  await page.locator('.notif .pitem').first().waitFor({ state: 'visible' })
}

test.describe('站内通知 · 多语言', () => {
  test('英文界面:标题与正文按 ref 显示英文;老通知正文保留原句', async ({ page }) => {
    await openNotifs(page, 'en')
    const items = page.locator('.notif .pitem')
    await expect(items.nth(0).locator('.pt')).toContainText('Approval rejected')
    await expect(items.nth(0).locator('.pd')).toHaveText('Zhang Wei rejected your command: DELETE FROM t')
    await expect(items.nth(1).locator('.pt')).toContainText('Export failed')
    await expect(items.nth(1).locator('.pd')).toHaveText('订单 · 旧的错误')
  })

  test('中文界面:一字不差显示原句', async ({ page }) => {
    await openNotifs(page, 'zh')
    const items = page.locator('.notif .pitem')
    await expect(items.nth(0).locator('.pt')).toContainText('审批被拒绝')
    await expect(items.nth(0).locator('.pd')).toHaveText('Zhang Wei 驳回了你的命令：DELETE FROM t')
  })
})

// 第三批:规范审查发现的消息按 messageRef 显示。
async function openReview(page: Page, lang: 'zh' | 'en') {
  await page.addInitScript((l) => localStorage.setItem('aegisdb_lang', l), lang)
  await stubCommon(page)
  await page.route('**/api/v1/sql-review/rules', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/sql-review/catalog', (r) => r.fulfill(envelope({ rules: [], dialects: ['mysql'] })))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/sql-review/check', (r) => r.fulfill(envelope({
    result: {
      dialect: 'mysql', statements: 1, errors: 1, warnings: 0, infos: 0, passed: false,
      findings: [{
        code: 'dml.require.where', name: 'DML 必须带 WHERE', level: 'error', category: 'dml', stmt: 1, line: 1,
        sql: 'DELETE FROM t_order', message: 'DELETE 未带 WHERE 条件,将作用于全表',
        messageRef: { code: 'mcea43d4d', args: { 0: 'DELETE' } },
      }],
    },
  })))
  await page.goto('/#/sql-review')
  await page.locator('textarea.sqlbox').fill('DELETE FROM t_order')
  await page.locator('.runbtn').click()
  await page.locator('.find .fmsg').first().waitFor({ state: 'visible' })
}

test.describe('规范审查 · 多语言', () => {
  test('英文界面:审查消息显示英文', async ({ page }) => {
    await openReview(page, 'en')
    await expect(page.locator('.find .fmsg')).toHaveText('DELETE has no WHERE clause and will affect the whole table')
  })
  test('中文界面:原句一字不差', async ({ page }) => {
    await openReview(page, 'zh')
    await expect(page.locator('.find .fmsg')).toHaveText('DELETE 未带 WHERE 条件,将作用于全表')
  })
})

// 收尾批:异步执行日志里网关自己写的行按 logRefs 翻译,NOTICE(数据库原话)原样。
const ASYNC_JOB = {
  id: 7, userId: 1, connectionId: 1, instance: 'fat-mysql', database: 'yone', sql: 'CALL p()', reason: '',
  status: 'done', risk: 'low', rows: 3, error: '', createdAt: '2026-10-31T10:00:00+08:00', startedAt: null, finishedAt: null,
  log: '· 开始执行 @ 2026-10-31 10:00:00\nNOTICE: 处理第 1 批\n· 执行完成 @ 2026-10-31 10:00:05 · 影响 3 行\n',
  logRefs: JSON.stringify([
    { t: '· 开始执行 @ 2026-10-31 10:00:00', r: { code: 'm596b9c50', args: { 0: '2026-10-31 10:00:00' } } },
    { t: '· 执行完成 @ 2026-10-31 10:00:05 · 影响 3 行', r: { code: 'm4b336c05', args: { 0: '2026-10-31 10:00:05', 1: '3' } } },
  ]),
}

async function openAsync(page: Page, lang: 'zh' | 'en') {
  await page.addInitScript((l) => localStorage.setItem('aegisdb_lang', l), lang)
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/async-jobs', (r) => r.fulfill(envelope([ASYNC_JOB])))
  await page.route('**/api/v1/async-jobs/7', (r) => r.fulfill(envelope(ASYNC_JOB)))
  await page.goto('/#/async-jobs')
  await page.locator('.job').first().click()
  await page.locator('.logpre').waitFor({ state: 'visible' })
}

test.describe('异步执行日志 · 多语言', () => {
  test('英文界面:网关自己的行译出,NOTICE 原样', async ({ page }) => {
    await openAsync(page, 'en')
    await expect(page.locator('.logpre')).toHaveText(
      '· Started @ 2026-10-31 10:00:00\nNOTICE: 处理第 1 批\n· Completed @ 2026-10-31 10:00:05 · 3 rows affected\n')
  })
  test('中文界面:原文一字不差', async ({ page }) => {
    await openAsync(page, 'zh')
    await expect(page.locator('.logpre')).toHaveText(ASYNC_JOB.log)
  })
})
