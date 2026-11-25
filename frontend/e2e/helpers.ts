import type { Page } from '@playwright/test'

// e2e 共用的登录态桩。
//
// 审查时数出六份逐行相同的脚手架(envelope / ME / TIERS / ENVS / 一组 page.route)。
// 少一份意味着:AppLayout 启动时多拉一个接口,就得改六个文件;哪个忘了,那个 spec 就
// 开始打真后端。这里只放**每个页面都要**的那几条;页面自己的数据(connections /
// releases …)由各 spec 在 stubCommon 之后再注册 —— Playwright 后注册的先匹配。

export const envelope = (data: unknown) => ({
  status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 0, msg: 'ok', data }),
})

export const failure = (msg: string) => ({
  status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 1001, msg, data: null }),
})

// 平台管理员。菜单里连发布那一组(pipeline / releases / pipelines)也带上:发布页的
// 路由闸是 meta.gate = 'pipeline',少了它守卫会静静地把访问重定向到总览。
export const ME = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, canSeeAllReleases: true, mfaEnabled: false,
  menus: {
    terminal: true, export: true, approve: true, db: true, rules: true, envtier: true,
    perms: true, audit: true, settings: true, pipeline: true, releases: true, pipelines: true,
  },
  capabilities: {},
}

export const TIERS = [{
  code: 'prod', displayName: '生产环境 · PROD', sortOrder: 0,
  requireMfa: true, dangerBanner: true, countsInPending: true, scanBaseline: true,
  connLayer: 'L1', defaultRole: 'dba_l2',
}]

export const ENVS = [{ code: 'prod', displayName: '生产环境 · PROD', tierCode: 'prod', sortOrder: 0 }]

/** 登录态 + 每个页面启动都会拉的那几个接口。 */
export async function stubCommon(page: Page, opts: { tiers?: unknown; envs?: unknown } = {}) {
  // 兜底:没打桩的接口一律 502,不让它经 vite 代理打到本机真后端。真后端不认 e2e-token,
  // 回 40100,拦截器随即清会话跳登录 —— 同一组 spec 在后端开着时全挂、关着时全过。502
  // 正是后端没开时代理给的结果,各 spec 一直是在这个前提下写的。最先注册 = 最后匹配。
  // 只认接口路径:'**/api/**' 会把 vite 上的源码模块 /src/api/*.ts 一起拦掉,整个应用起不来。
  await page.route((u) => u.pathname.startsWith('/api/'), (r) => r.fulfill({ status: 502, body: 'e2e: unstubbed endpoint' }))
  await page.addInitScript(() => {
    localStorage.setItem('vela_session', '1.e2e-token')
    document.cookie = 'vela_csrf=e2e-csrf; path=/'
  })
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  // 系统名称与 LOGO:默认未配置(各 spec 要测品牌时自己再注册一条盖掉它)
  await page.route('**/api/v1/auth/branding', (r) => r.fulfill(envelope({ name: '', nameEn: '', logo: '' })))
  await page.route('**/api/v1/env-tiers', (r) => r.fulfill(envelope(opts.tiers ?? TIERS)))
  await page.route('**/api/v1/environments', (r) => r.fulfill(envelope(opts.envs ?? ENVS)))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/approvals/todo', (r) => r.fulfill(envelope({ decide: 0, execute: 0, items: [] })))
  await page.route('**/api/v1/gateway/stats', (r) =>
    r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))
  await page.route('**/api/v1/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/approval-chain', (r) => r.fulfill(envelope({ chain: [] })))
  await page.route('**/api/v1/pipelines', (r) => r.fulfill(envelope([])))
}

// 终端页进来不再自动打开标签(2026-10 用户要求:进入只是进页面,不替人挑实例、不建连)。
// 用例要一个会话时,像人一样在左侧树上点一个实例:给了名字就先搜到它,没给就点第一个可见的
// (排在第一层级的环境默认展开,第一个通常就是 prod —— 与从前默认打开的那个一致)。
export async function gotoTerminal(page: Page, name?: string) {
  await page.goto('/#/terminal')
  if (name) {
    await page.locator('.searchbox input').fill(name)
    await page.locator('.inst', { hasText: name }).first().click()
    await page.locator('.searchbox input').fill('')
  } else {
    await page.locator('.inst').first().click()
  }
}

// 确认弹窗(ConfirmDialog,替代了浏览器原生 confirm):一出现就点「确定」。原先各 spec 用
// page.on('dialog', d => d.accept()) 一路点掉原生框,这里是它的等价物 —— 在 goto 之前调用。
export async function autoConfirm(page: Page) {
  await page.addInitScript(() => {
    new MutationObserver(() => {
      (document.querySelector('[data-testid="confirm-ok"]') as HTMLElement | null)?.click()
    }).observe(document, { childList: true, subtree: true })
  })
}
