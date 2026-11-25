import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// SQL*Plus 绑定变量(sqlplus-compat 06):VARIABLE 记在标签页上,每次执行随请求带过去,
// 网关带回的新值 PRINT 得到;SET AUTOPRINT ON 自动打出来。

const CONN = {
  id: 1, name: 'ora-dev', engine: 'Oracle 19c', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'h', port: 1521, database: 'app', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})
async function type(page: Page, line: string) {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(200)
}

async function openTerm(page: Page) {
  const bodies: any[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    const b = r.request().postDataJSON()
    bodies.push(b)
    // 模拟网关:EXEC :n := 42 之后把 N 改成 42 带回来
    const binds = (b.binds || []).map((x: any) => (/:n\s*:=\s*42/i.test(b.sql) && x.name === 'N' ? { ...x, value: '42', null: false } : x))
    return r.fulfill(envelope({ intercepted: false, risk: 'low', output: '执行成功', ms: 1, binds }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  return bodies
}

test('VARIABLE → EXEC 赋值 → PRINT:变量随请求带过去,新值带回来', async ({ page }) => {
  const bodies = await openTerm(page)
  await type(page, 'variable n number')
  expect(bodies).toEqual([]) // 声明在本地
  await type(page, 'exec :n := 42')
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].binds).toEqual([{ name: 'N', type: 'NUMBER', value: '', null: true }])
  await type(page, 'print n')
  await expect.poll(() => screen(page)).toMatch(/\b42\b/)
  expect(bodies.length).toBe(1) // PRINT 不发请求
  await type(page, 'print zz')
  await expect.poll(() => screen(page)).toContain('SP2-0552')
  // 下一条执行带着新值
  await type(page, 'select :n from dual;')
  await expect.poll(() => bodies.length).toBe(2)
  expect(bodies[1].binds[0].value).toBe('42')
})

test('SET AUTOPRINT ON:执行完自动打出用到的变量;REFCURSOR 说不支持', async ({ page }) => {
  await openTerm(page)
  await type(page, 'var n number')
  await type(page, 'set autoprint on')
  await type(page, 'exec :n := 42')
  await expect.poll(() => screen(page)).toMatch(/\b42\b/)
  await type(page, 'variable rc refcursor')
  await expect.poll(() => screen(page)).toContain('REFCURSOR 暂不支持')
})
