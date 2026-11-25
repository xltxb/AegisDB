import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Oracle 终端里的 SQL*Plus SET:
//   · SET SERVEROUTPUT ON 不用分号,回车就生效;本地处理,不发请求
//   · 之后的执行带着 serverOutput=true,读回的 DBMS_OUTPUT 打在结果后面
//   · SHOW SERVEROUTPUT 本地回答;其它 SET 说"已忽略"
//   · 别的引擎的 SET 是真正的 SQL,照常下发

const conn = (id: number, name: string, engine: string) => ({
  id, name, engine, env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 1, database: 'app',
  policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
})

async function openTerm(page: Page, c: any) {
  const bodies: any[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([c])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: c.id, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    const b = r.request().postDataJSON()
    bodies.push(b)
    const out = b.serverOutput ? ['hello from plsql', '第二行'] : []
    return r.fulfill(envelope({ intercepted: false, risk: 'low', output: 'PL/SQL 执行成功', ms: 3, serverOutput: out }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  return bodies
}

const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})

async function type(page: Page, line: string) {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(250)
}

test('Oracle:SET SERVEROUTPUT ON 回车即生效,之后执行带回 DBMS_OUTPUT', async ({ page }) => {
  const bodies = await openTerm(page, conn(1, 'ora-dev', 'Oracle 19c'))
  await type(page, 'set serveroutput on')
  await expect.poll(() => screen(page)).toContain('SERVEROUTPUT 已开启')
  expect(bodies).toEqual([]) // 本地处理,不发请求

  await type(page, 'show serveroutput')
  await expect.poll(() => screen(page)).toContain('serveroutput ON')

  // 网关终端不用的 SET 选项:说"已忽略",不发请求
  await type(page, 'set echo on')
  await expect.poll(() => screen(page)).toContain('SET ECHO')
  // LINESIZE 第三批起真正生效(sqlplus-compat 10),与 SQL*Plus 一样不回话 —— 也不发请求
  await type(page, 'set linesize 200')
  expect(bodies).toEqual([])

  await type(page, "begin dbms_output.put_line('x'); end;")
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].serverOutput).toBe(true)
  await expect.poll(() => screen(page)).toContain('hello from plsql')
  expect(await screen(page)).toContain('第二行')

  await type(page, 'set serveroutput off')
  await type(page, "begin dbms_output.put_line('y'); end;")
  await expect.poll(() => bodies.length).toBe(2)
  expect(bodies[1].serverOutput).toBe(false)
})

test('MySQL:SET 是真正的 SQL,照常下发', async ({ page }) => {
  const bodies = await openTerm(page, conn(2, 'mysql-dev', 'MySQL 8.0'))
  await type(page, 'SET NAMES utf8mb4;')
  await expect.poll(() => bodies.length).toBe(1)
  expect(bodies[0].sql).toBe('SET NAMES utf8mb4')
})
