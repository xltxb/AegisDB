import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Oracle 终端里的 SQL*Plus 客户端命令(sqlplus-compat 第一批):
//   · 绕开网关的命令(CONNECT / HOST …)拒绝并说明原因,不发请求
//   · PROMPT / REM / COLUMN 本地处理
//   · DEFINE / & / && / VERIFY:先展开再判定 —— 发给 risk/check 与执行的都是展开后的文本
//   · / 重跑上一条;SHOW ERRORS 补上一次 CREATE 的对象;SET TIMING OFF 不打耗时
//   · 别的引擎里 & 不是替换变量

const conn = (id: number, name: string, engine: string) => ({
  id, name, engine, env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 1, database: 'app',
  policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
})

async function openTerm(page: Page, c: any) {
  const execs: string[] = []
  const checks: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([c])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: c.id, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => {
    checks.push(r.request().postDataJSON().sql)
    return r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false }))
  })
  await page.route('**/api/v1/terminal/exec', (r) => {
    const sql = r.request().postDataJSON().sql as string
    execs.push(sql)
    if (/^SHOW ERRORS/i.test(sql)) return r.fulfill(envelope({ intercepted: false, risk: 'low', columns: ['LINE/COL', 'ERROR'], data: [], rows: 0, ms: 2 }))
    return r.fulfill(envelope({ intercepted: false, risk: 'low', output: '执行成功', ms: 3 }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  return { execs, checks }
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

const ORA = () => conn(1, 'ora-dev', 'Oracle 19c')

test('绕开网关的命令:拒绝并说明原因,不发请求', async ({ page }) => {
  const { execs, checks } = await openTerm(page, ORA())
  await type(page, 'conn scott/tiger@orcl')
  await expect.poll(() => screen(page)).toContain('CONNECT 是 SQL*Plus 命令')
  expect(await screen(page)).toContain('绕过平台的授权与审计')
  await type(page, 'host ls')
  await expect.poll(() => screen(page)).toContain('HOST 是 SQL*Plus 命令')
  await type(page, 'exit')
  await expect.poll(() => screen(page)).toContain('关闭标签页')
  expect(execs).toEqual([])
  expect(checks).toEqual([])
})

test('PROMPT 打文字、REM 什么都不打、BREAK 暂不支持,都不发请求', async ({ page }) => {
  const { execs, checks } = await openTerm(page, ORA())
  await type(page, 'prompt 开始建表')
  await expect.poll(() => screen(page)).toContain('开始建表')
  await type(page, 'rem 这是注释')
  await type(page, 'break on deptno')
  await expect.poll(() => screen(page)).toContain('BREAK 暂不支持')
  expect(execs).toEqual([])
  expect(checks).toEqual([])
})

test('DEFINE 之后 & 展开再判定,VERIFY 打出 old / new', async ({ page }) => {
  const { execs, checks } = await openTerm(page, ORA())
  await type(page, 'define tbl = emp')
  expect(execs).toEqual([])
  await type(page, 'select * from &tbl;')
  await expect.poll(() => execs.length).toBe(1)
  expect(checks).toEqual(['select * from emp'])
  expect(execs).toEqual(['select * from emp'])
  const s = await screen(page)
  expect(s).toContain('old   1: select * from &tbl')
  expect(s).toContain('new   1: select * from emp')
  await type(page, 'define tbl')
  await expect.poll(() => screen(page)).toContain('DEFINE TBL')
})

// 安全:判定看到的必须是展开后的文本,否则 DEFINE x = DROP 就绕过了高危字典
test('DEFINE x = DROP 之后 &x TABLE t:送去判定的是 DROP TABLE t', async ({ page }) => {
  const { checks } = await openTerm(page, ORA())
  await type(page, 'define x = DROP')
  await type(page, '&x TABLE t;')
  await expect.poll(() => checks.length).toBe(1)
  expect(checks[0]).toBe('DROP TABLE t')
})

test('未定义的 & 在终端里问值;Ctrl+C 取消不执行', async ({ page }) => {
  const { execs } = await openTerm(page, ORA())
  await type(page, 'select &col from dual;')
  await expect.poll(() => screen(page)).toContain('Enter value for col:')
  await type(page, 'ename')
  await expect.poll(() => execs.length).toBe(1)
  expect(execs[0]).toBe('select ename from dual')

  await type(page, 'select &col from dual;')
  await expect.poll(() => (screen(page).then((s) => s.split('Enter value for col:').length - 1))).toBe(2)
  await page.keyboard.press('Control+C')
  await page.waitForTimeout(300)
  expect(execs.length).toBe(1)
})

test('/ 重跑上一条;SHOW ERRORS 补上一次 CREATE 的对象', async ({ page }) => {
  const { execs } = await openTerm(page, ORA())
  await type(page, 'select 1 from dual;')
  await expect.poll(() => execs.length).toBe(1)
  await type(page, '/')
  await expect.poll(() => execs.length).toBe(2)
  expect(execs[1]).toBe('select 1 from dual')

  await page.keyboard.type('create or replace procedure p_x is')
  await page.keyboard.press('Enter')
  await type(page, 'begin null; end;')
  await expect.poll(() => execs.length).toBe(3)
  await type(page, 'show errors')
  await expect.poll(() => execs.length).toBe(4)
  expect(execs[3]).toBe('SHOW ERRORS PROCEDURE P_X')
  await expect.poll(() => screen(page)).toContain('No errors.')
})

test('SET TIMING OFF 之后不打耗时', async ({ page }) => {
  await openTerm(page, ORA())
  await type(page, 'select 1 from dual;')
  await expect.poll(() => screen(page)).toContain('耗时 3ms')
  await page.keyboard.press('Control+L')
  await type(page, 'set timing off')
  await type(page, 'select 2 from dual;')
  await expect.poll(() => screen(page)).toContain('执行成功')
  expect(await screen(page)).not.toContain('耗时')
})

test('MySQL:& 不是替换变量,原样下发', async ({ page }) => {
  const { execs } = await openTerm(page, conn(2, 'mysql-dev', 'MySQL 8.0'))
  await type(page, 'select 1 & 2;')
  await expect.poll(() => execs.length).toBe(1)
  expect(execs[0]).toBe('select 1 & 2')
  expect(await screen(page)).not.toContain('Enter value')
})
