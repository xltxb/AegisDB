import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// SQL*Plus 显示设置与行编辑器(sqlplus-compat 第三批):
//   · COLUMN FORMAT / HEADING / NOPRINT 落到结果上;SET COLSEP 换成纯文本;SET FEEDBACK OFF 不打行数
//   · LIST / CHANGE 改缓冲区,/ 发出去的是改过的那条
//   · SAVE 下载缓冲区;GET 读进缓冲区(不执行)
//   · HELP、SHOW ALL 本地回答
//   · MySQL 不受影响

const conn = (id: number, name: string, engine: string) => ({
  id, name, engine, env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 1, database: 'app',
  policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
})
const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})
async function type(page: Page, line: string) {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(200)
}

async function openTerm(page: Page, c: any) {
  const execs: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([c])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: c.id, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    execs.push(r.request().postDataJSON().sql)
    return r.fulfill(envelope({ intercepted: false, risk: 'low', columns: ['ENAME', 'SAL', 'SECRET'], data: [['SMITH', '800', 'x'], ['ALLEN', '1600.5', 'y']], rows: 2, ms: 2 }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  // 终端里画表格(不是网格面板):按钮在工具栏上
  return execs
}

const ORA = () => conn(1, 'ora-dev', 'Oracle 19c')

test('COLUMN FORMAT / HEADING / NOPRINT 落到结果上;FEEDBACK OFF 不打行数', async ({ page }) => {
  await openTerm(page, ORA())
  await type(page, "column sal format 99,999.99 heading 'Salary'")
  await type(page, 'col secret noprint')
  await type(page, 'set feedback off')
  await type(page, 'set colsep " | "')
  await type(page, 'select ename, sal, secret from emp;')
  await expect.poll(() => screen(page)).toContain('Salary')
  const s = await screen(page)
  expect(s).toContain('1,600.50')
  expect(s).toContain('800.00')
  expect(s).not.toContain('SECRET')
  expect(s).toMatch(/ENAME \| +Salary/)
  expect(s).not.toMatch(/\(2 行/)
  await type(page, 'show feedback')
  await expect.poll(() => screen(page)).toContain('feedback OFF')
  await type(page, 'column sal')
  await expect.poll(() => screen(page)).toContain('FORMAT   99,999.99')
})

test('LIST / CHANGE 改缓冲区,/ 发出去的是改过的那条', async ({ page }) => {
  const execs = await openTerm(page, ORA())
  await page.keyboard.type('select ename')
  await page.keyboard.press('Enter')
  await type(page, 'from emp;')
  await expect.poll(() => execs.length).toBe(1)
  await type(page, 'l')
  await expect.poll(() => screen(page)).toContain('2* from emp')
  await type(page, 'c /emp/dept/')
  await expect.poll(() => screen(page)).toContain('2* from dept')
  await type(page, 'a  d')
  await type(page, '/')
  await expect.poll(() => execs.length).toBe(2)
  expect(execs[1]).toBe('select ename\nfrom dept d')
  await type(page, 'c /zzz/x/')
  await expect.poll(() => screen(page)).toContain('SP2-0023')
})

test('SAVE 下载缓冲区;GET 读进缓冲区不执行', async ({ page }) => {
  const execs = await openTerm(page, ORA())
  await type(page, 'select 1 from dual;')
  await expect.poll(() => execs.length).toBe(1)
  const dl = page.waitForEvent('download')
  await type(page, 'save myq')
  const d = await dl
  expect(d.suggestedFilename()).toBe('myq.sql')
  const body = Buffer.concat(await (await d.createReadStream())!.toArray()).toString('utf8')
  expect(body).toBe('select 1 from dual\n/\n')

  const chooser = page.waitForEvent('filechooser')
  await page.keyboard.type('get other.sql')
  await page.keyboard.press('Enter')
  await (await chooser).setFiles({ name: 'other.sql', mimeType: 'text/plain', buffer: Buffer.from('select 2\nfrom dual\n/\n') })
  await expect.poll(() => screen(page)).toContain('2* from dual')
  expect(execs.length).toBe(1) // GET 不执行
  await type(page, '/')
  await expect.poll(() => execs.length).toBe(2)
  expect(execs[1]).toBe('select 2\nfrom dual')
})

test('HELP 与 SHOW ALL 本地回答', async ({ page }) => {
  const execs = await openTerm(page, ORA())
  await type(page, 'help')
  await expect.poll(() => screen(page)).toContain('会绕开网关的命令不支持')
  await page.keyboard.press('Control+L')
  await type(page, 'set linesize 120')
  await type(page, 'show all')
  await expect.poll(() => screen(page)).toContain('linesize 120')
  expect(await screen(page)).toContain('verify ON')
  expect(execs).toEqual([])
})

test('MySQL:没有这些命令,结果照旧', async ({ page }) => {
  const execs = await openTerm(page, conn(2, 'mysql-dev', 'MySQL 8.0'))
  await type(page, 'select ename, sal, secret from emp;')
  await expect.poll(() => execs.length).toBe(1)
  await expect.poll(() => screen(page)).toContain('SECRET')
})
