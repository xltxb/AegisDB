import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Web 命令行上传脚本的进度(2026-10 用户要求):
//   1. 选好文件之后有进度页:读取文件 → 上传 → 服务端扫描(第 i / N 条),扫完再出扫描报告;
//   2. 点执行之后,弹窗的遮罩是进度卡片:第 i / N 条、百分比、正在跑哪条。

const CONN = {
  id: 1, name: 'shop-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online',
  host: 'mysql.internal', port: 3306, database: 'shop', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}
const SCAN = {
  filename: 'two.sql', total: 2, high: 0, mid: 0, safe: 2, hasRisky: false,
  statements: [
    { index: 1, sql: 'INSERT INTO t VALUES (1), (2), (3)', command: 'INSERT', risk: 'safe', noWhere: false, action: 'allow' },
    { index: 2, sql: 'SELECT id FROM t', command: 'SELECT', risk: 'safe', noWhere: false, action: 'allow' },
  ],
}
const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})

/** 一个可以由测试放行的闸:扫描 / 执行的响应等它开了才回,期间界面停在进度上。 */
function gate() {
  let open!: () => void
  const opened = new Promise<void>((r) => { open = r })
  return { open, opened }
}

async function setup(page: Page, opts: { scanGate?: Promise<void>; execGate?: Promise<void>; progress: () => object; seen?: string[] }) {
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'shop', tables: [] }] })))
  await page.route('**/api/v1/scripts/config', (r) => r.fulfill(envelope({ enabled: true, savePath: 'uploads' })))
  await page.route('**/api/v1/scripts/scan', async (r) => {
    opts.seen?.push(JSON.parse(r.request().postData() || '{}').progressId || '')
    await opts.scanGate
    await r.fulfill(envelope(SCAN)).catch(() => {}) // 取消后浏览器已放弃这个请求
  })
  await page.route('**/api/v1/scripts/execute', async (r) => {
    opts.seen?.push(JSON.parse(r.request().postData() || '{}').progressId || '')
    await opts.execGate
    await r.fulfill(envelope({ executed: true, statements: 2, results: [
      { pos: 1, sql: 'INSERT INTO t VALUES (1), (2), (3)', output: '执行成功 · 3 行受影响', rows: 3, ms: 4, risk: 'low' },
      { pos: 2, sql: 'SELECT id FROM t', columns: ['id'], data: [['1']], rows: 1, ms: 2, risk: 'low' },
    ] }))
  })
  await page.route('**/api/v1/scripts/progress/**', (r) => r.fulfill(envelope(opts.progress())))
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
}

const pickFile = (page: Page) => page.locator('input[type="file"].hidden-file').setInputFiles({
  name: 'two.sql', mimeType: 'text/plain', buffer: Buffer.from('INSERT INTO t VALUES (1), (2), (3);\nSELECT id FROM t;\n'),
})

test('选好文件:进度页依次显示读取、上传、逐条扫描,扫完换成扫描报告', async ({ page }) => {
  const g = gate()
  const seen: string[] = []
  await setup(page, { scanGate: g.opened, seen, progress: () => ({ phase: 'judge', done: 1, total: 2, current: 'SELECT id FROM t', startedAt: new Date().toISOString(), finished: false }) })
  await pickFile(page)
  const prep = page.getByTestId('script-prep')
  await expect(prep).toBeVisible()
  await expect(prep).toContainText('two.sql')
  await expect(page.getByTestId('sp-val-read')).toHaveText('完成')
  await expect(page.getByTestId('sp-val-upload')).toHaveText('完成')
  await expect(page.getByTestId('sp-val-scan')).toHaveText('逐条判定 1 / 2')
  await expect(prep).toContainText('SELECT id FROM t')
  expect(seen[0]).toMatch(/^[A-Za-z0-9-]{8,64}$/) // 扫描请求带上了进度标识
  g.open()
  await expect(prep).toHaveCount(0)
  await expect(page.getByRole('button', { name: '执行脚本' })).toBeVisible()
})

test('点执行:弹窗里是进度卡片 —— 第几条、百分比、正在跑哪条;跑完照常输出结果', async ({ page }) => {
  const g = gate()
  await setup(page, { execGate: g.opened, progress: () => ({ phase: 'run', done: 1, total: 2, current: 'SELECT id FROM t', startedAt: new Date(Date.now() - 3000).toISOString(), finished: false }) })
  await pickFile(page)
  await page.getByRole('button', { name: '执行脚本' }).click()
  const card = page.getByTestId('sc-progress')
  await expect(card).toBeVisible()
  await expect(card).toContainText('正在执行 第 1 / 2 条')
  await expect(card).toContainText('50%')
  await expect(card).toContainText('#2 SELECT id FROM t')
  await expect(card).toContainText(/已用 \d+ 秒/)
  g.open()
  await expect(card).toHaveCount(0)
  await expect.poll(() => screen(page)).toContain('脚本执行成功')
})

test('扫描期间可以取消:进度页关掉,终端说明已取消,不弹扫描报告', async ({ page }) => {
  const g = gate() // 永不放行:模拟扫描很慢
  await setup(page, { scanGate: g.opened, progress: () => ({ phase: 'scan', done: 0, total: 2, startedAt: new Date().toISOString(), finished: false }) })
  await pickFile(page)
  await expect(page.getByTestId('script-prep')).toBeVisible()
  await page.getByTestId('sp-cancel').click()
  await expect(page.getByTestId('script-prep')).toHaveCount(0)
  await expect.poll(() => screen(page)).toContain('已取消脚本扫描')
  await expect(page.getByRole('button', { name: '执行脚本' })).toHaveCount(0)
})
