import type { Page } from '@playwright/test'

// Shared scaffolding for the e2e specs. Every spec seeds a session and stubs the
// APIs its screen touches, so none of them need the Go backend running — the
// behaviours under test are about layout and the cascade, not about the server.

/** The gateway's response envelope: every handler returns `{code, msg, data}`. */
export const envelope = (data: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify({ code: 0, msg: 'ok', data }),
})

/** A non-zero code — the shape the UI must read a server-side refusal out of. */
export const failure = (code: number, msg: string) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify({ code, msg, data: null }),
})

export const ALL_MENUS = {
  terminal: true, export: true, approve: true, db: true, rules: true,
  envtier: true, perms: true, audit: true, settings: true,
  pipeline: true, execwindow: true,
}

/**
 * An admin signed in at the back-office portal.
 *
 * `roleCodes` must contain `admin` and the portal must be `backend`, or the
 * `adminOnly` routes bounce to the first visible front-office page — see
 * `router/guards.ts`.
 */
export const ADMIN = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, mfaEnabled: false,
  menus: ALL_MENUS,
  capabilities: {},
}

/**
 * Put a token and a portal in localStorage before the app boots.
 *
 * The portal matters: the guards check it *and* the role, so a session seeded
 * without it lands on the front office and every back-office spec times out
 * looking for a page it was redirected away from.
 */
export async function seedSession(page: Page, portal: 'backend' | 'frontend' = 'backend') {
  await page.addInitScript((p) => {
    localStorage.setItem('aegis_token', 'e2e-token')
    localStorage.setItem('aegis_portal', p)
  }, portal)
}

/**
 * Stub the calls the app shell makes on every authenticated page (identity, the
 * notification bell, the gateway health pill), so a spec only has to describe
 * the APIs of the screen it actually cares about.
 */
export async function stubShell(page: Page, me: unknown = ADMIN) {
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/notifications**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/gateway/stats**', (r) =>
    r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))
  // The sidebar's pending-work badges and the shell's settings read. They fire on
  // every page, so leaving them to hit the dev proxy makes each spec depend on
  // how the app copes with a 500 from an API it is not testing.
  await page.route('**/api/v1/approvals?**', (r) =>
    r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 1 })))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({})))
}

/** 两个门户各自能到的页面。门户选错不会报错,只会被静默送回 /terminal。 */
export const OPS_ROUTES = [
  'dashboard', 'terminal', 'approvals', 'inbox', 'changes',
  'scripts', 'export', 'async-jobs', 'exec-windows', 'catalog',
]
export const ADMIN_ROUTES = [
  'connections', 'osc', 'risk-rules', 'sql-review', 'gov',
  'permissions', 'pipelines', 'users', 'audit', 'settings',
]

// ---------------------------------------------------------------- 终端会话

const conn = (id: number, name: string, env: string, engine = 'mysql', database = 'appdb') => ({
  id, name, env, engine, host: '10.0.0.1', port: 3306, policy: 'strict',
  defaultRole: 'ro', layer: 'core', tags: '', database, status: 'online',
})

export const DEV_TIERS = [
  { code: 'dev', displayName: '测试 · DEV', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false, connLayer: 'L4', defaultRole: 'developer' },
]
export const DEV_ENVS = [{ code: 'dev', displayName: '测试 · DEV', tierCode: 'dev', sortOrder: 0 }]
/** 两台实例:切实例那条规格要有地方可切。 */
export const DEV_CONNS = [conn(1, 'sandbox', 'dev'), conn(2, 'staging', 'dev', 'mysql', 'shopdb')]

/**
 * 终端页开一条会话要喂的全部 HTTP。
 *
 * e2e 不起后端,所以这些全部由规格自己描述。WebSocket 不在这里 —— 它由
 * `wsFake.ts` 的替身顶掉。
 */
export async function stubTerminal(
  page: Page,
  conns: unknown[] = DEV_CONNS,
  tiers: unknown[] = DEV_TIERS,
  envs: unknown[] = DEV_ENVS,
) {
  await seedSession(page)
  await stubShell(page, ADMIN)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope(conns)))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(tiers)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(envs)))
  await page.route('**/api/v1/environments/usage', (r) => r.fulfill(envelope({})))
  await page.route('**/api/v1/connections/*/schema**', (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [] })))
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [] })))
  await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/snippets**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/script-uploads**', (r) => r.fulfill(envelope([])))
  // 预检默认放行。要验拦截的规格自己覆盖这一条。
  await page.route('**/api/v1/risk/check', (r) =>
    r.fulfill(envelope({ action: 'allow', requiresApproval: false, matchedRule: '', matchedRuleRef: null })))
  // 补全的词典与导出日志的审计。不打桩它们会去打真实后端,在 e2e 里就是一条
  // ECONNREFUSED,让规格的失败信息里混进一堆与它无关的噪声。
  await page.route('**/api/v1/risk-commands**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/terminal/transcript-export', (r) => r.fulfill(envelope({})))
}
