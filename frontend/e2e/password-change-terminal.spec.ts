import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// 2026-10-26 回归:改完密码(store 换上新 token)后回到已开着的终端执行命令,旧 socket 被服务端按
// TokenVersion 回 session_revoked,前端当成会话失效直接登出 —— 而改密码的提示说"不必重新登录"。
// 现在:token 已换新时用新 token 重连,不登出;token 没变(管理员重置、被停用)照旧登出。

// 会话标记 "<uid>.<csrf>":前端只读其中的 uid 判断"是不是同一个人"
const jwtFor = (uid: number, nonce: string) => `${uid}.${nonce}`

const conn = { id: 1, name: 'app-dev', engine: 'MySQL 8.0', env: 'dev', tierCode: 'dev', status: 'online', host: 'h', port: 1, database: 'app', policy: 'audit-only', defaultRole: 'dba_l2', tags: '' }

async function openTerm(page: Page) {
  const sockets: number[] = []
  let execs = 0
  await stubCommon(page, {
    tiers: [{ code: 'dev', displayName: 'DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L3', defaultRole: 'dba_l2' }],
    envs: [{ code: 'dev', displayName: 'DEV', tierCode: 'dev', sortOrder: 0 }],
  })
  await page.addInitScript((tok) => localStorage.setItem('vela_session', tok), jwtFor(1, 'login'))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([conn])))
  await page.route(/\/api\/v1\/connections\/\d+\/schema/, (r) => r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/risk/check', (r) => r.fulfill(envelope({ action: 'allow', risk: 'low', requiresApproval: false })))
  await page.routeWebSocket(/\/terminal\/ws/, (ws) => {
    const n = sockets.length + 1
    sockets.push(n)
    ws.onMessage((raw) => {
      const m = JSON.parse(String(raw))
      if (m.type === 'ping') return ws.send(JSON.stringify({ type: 'pong' }))
      if (m.type !== 'exec') return
      execs++
      // 第一条 socket 上的命令一律按"版本对不上"拒;重连后的 socket 正常回
      if (n === 1) {
        ws.send(JSON.stringify({ type: 'session_revoked', message: '会话已失效，请重新登录' }))
        ws.close()
        return
      }
      ws.send(JSON.stringify({ type: 'error', message: 'reply-on-socket-' + n }))
    })
  })
  await gotoTerminal(page)
  await page.locator('.xterm:visible').waitFor()
  await expect.poll(() => sockets.length).toBe(1)
  await page.locator('.xterm:visible').click()
  return { sockets, execCount: () => execs }
}

const screen = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).join('\n') : ''
})

test('改密码换了 token:终端用新 token 重连,不登出', async ({ page }) => {
  const { sockets } = await openTerm(page)
  await page.evaluate((tok) => localStorage.setItem('vela_session', tok), jwtFor(1, 'after-password-change'))
  await page.keyboard.type('select 1;')
  await page.keyboard.press('Enter')
  await expect.poll(() => screen(page)).toContain('已用新凭据重新连接')
  await expect.poll(() => sockets.length).toBe(2)
  expect(page.url()).not.toContain('/login')
  await page.keyboard.type('select 2;')
  await page.keyboard.press('Enter')
  await expect.poll(() => screen(page)).toContain('reply-on-socket-2')
})

test('token 换成了另一个人的(另一个标签里换人登录):不重连,登出', async ({ page }) => {
  const { sockets } = await openTerm(page)
  await page.evaluate((tok) => localStorage.setItem('vela_session', tok), jwtFor(2, 'someone-else'))
  await page.keyboard.type('select 1;')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/login/)
  expect(sockets.length).toBe(1)
})

test('token 没变(被重置 / 停用):照旧登出', async ({ page }) => {
  await openTerm(page)
  await page.keyboard.type('select 1;')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/login/)
})
