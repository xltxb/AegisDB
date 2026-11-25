import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// SQL*Plus 的 @ / @@ / START 与 SPOOL / WHENEVER(sqlplus-compat 第二批):
//   · @main.sql 弹出选文件;连同 @@ 引用的文件一起选,终端展开成一份再交给扫描
//   · 扫描、执行收到的是展开后的文本(位置参数、DEFINE 都已替换)
//   · 缺被引用的文件:当场说缺哪个,不扫描
//   · 交互里的 WHENEVER 加在 @ 脚本开头
//   · SPOOL 文件 / SPOOL OFF 与 tee 一样,关掉时下载

const CONN = {
  id: 1, name: 'ora-dev', engine: 'Oracle 19c', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'h', port: 1521, database: 'app', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})
// 像人一样:敲下 @ 命令,文件选择框弹出来,选好文件
async function startWith(page: Page, line: string, files: { name: string; mimeType: string; buffer: Buffer }[]) {
  const chooser = page.waitForEvent('filechooser')
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await (await chooser).setFiles(files)
  await page.waitForTimeout(200)
}
async function type(page: Page, line: string) {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(200)
}

async function openTerm(page: Page) {
  const scans: string[] = []
  const execs: string[] = []
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) => r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/scripts/config', (r) => r.fulfill(envelope({ enabled: true, savePath: 'uploads' })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.route('**/api/v1/terminal/exec', (r) => {
    execs.push(r.request().postDataJSON().sql)
    return r.fulfill(envelope({ intercepted: false, risk: 'low', output: '执行成功', ms: 1 }))
  })
  await page.route('**/api/v1/scripts/scan', (r) => {
    const content = r.request().postDataJSON().content as string
    scans.push(content)
    const stmts = content.split('\n').filter(Boolean).map((sql, i) => ({ index: i + 1, sql, command: 'X', risk: 'safe', noWhere: false, action: 'allow' }))
    return r.fulfill(envelope({ filename: 'main.sql', total: stmts.length, high: 0, mid: 0, safe: stmts.length, hasRisky: false, statements: stmts }))
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await page.locator('.xterm:visible').click()
  return { scans, execs }
}

const file = (name: string, text: string) => ({ name, mimeType: 'text/plain', buffer: Buffer.from(text) })

test('@main.sql:连同 @@ 引用的文件一起选,展开后交给扫描(位置参数、DEFINE 已替换)', async ({ page }) => {
  const { scans } = await openTerm(page)
  await type(page, 'define owner = APP')
  await startWith(page, '@main.sql emp', [
    file('main.sql', 'PROMPT 开始\n@@b.sql\nSELECT * FROM &owner..&1;'),
    file('b.sql', 'INSERT INTO &owner..log VALUES (1);'),
  ])
  expect(await screen(page)).toContain('请选择 main.sql')
  await expect.poll(() => scans.length).toBe(1)
  expect(scans[0]).toBe('PROMPT 开始\nINSERT INTO APP.log VALUES (1);\nSELECT * FROM APP.emp;')
  // 扫描弹窗出来了(展开后的每条都在里面)
  await expect(page.getByRole('button', { name: '执行脚本' })).toBeVisible()
})

test('缺被引用的文件:当场说缺哪个,不扫描', async ({ page }) => {
  const { scans } = await openTerm(page)
  await startWith(page, '@main.sql', [file('main.sql', '@@sub.sql\nSELECT 1 FROM dual;')])
  await expect.poll(() => screen(page)).toContain('缺少被引用的文件:sub.sql')
  expect(scans).toEqual([])
  // 编辑器放开了,还能接着敲
  await type(page, 'prompt still alive')
  await expect.poll(() => screen(page)).toContain('still alive')
})

test('交互里的 WHENEVER SQLERROR CONTINUE 加在 @ 脚本开头', async ({ page }) => {
  const { scans } = await openTerm(page)
  await type(page, 'whenever sqlerror continue')
  await expect.poll(() => screen(page)).toContain('已记下')
  await startWith(page, 'start main', [file('main.sql', 'SELECT 1 FROM dual;')])
  await expect.poll(() => scans.length).toBe(1)
  expect(scans[0]).toBe('WHENEVER sqlerror continue\nSELECT 1 FROM dual;')
})

test('SPOOL 文件 … SPOOL OFF:关掉时下载,内容含命令与结果', async ({ page }) => {
  await openTerm(page)
  await type(page, 'spool deploy.log')
  await type(page, 'select 1 from dual;')
  await expect.poll(() => screen(page)).toContain('执行成功')
  await type(page, 'spool')
  await expect.poll(() => screen(page)).toContain('deploy.log')
  const dl = page.waitForEvent('download')
  await type(page, 'spool off')
  const d = await dl
  expect(d.suggestedFilename()).toBe('deploy.log')
  const body = await (await d.createReadStream())!.toArray().then((c) => Buffer.concat(c).toString('utf8'))
  expect(body).toContain('select 1 from dual')
  expect(body).toContain('执行成功')
})
