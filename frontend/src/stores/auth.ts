import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/api'
import { dropLegacyToken, sessionMarker, setSessionMarker, clearSessionMarker } from '@/lib/session'
import type { ApprovalTodo, LoginResp, Me } from '@/types'

// Route order used to pick the first visible menu after login.
// 总览不在这张表里,而这是有意的:这张表回答的是"这个人有没有任何一处能去",
// 用来决定被拒之后把他送到哪、以及一个菜单都没有的账户该被弹回登录页。总览人人
// 可见,写进来会让那个判断永远为真。
// admin:页面在管理后台(/admin),只有平台管理员进得去;auditor:审计员也可以(只到审计)。
const ROUTE_ORDER: { key: string; path: string; admin?: boolean; auditor?: boolean }[] = [
  { key: 'terminal', path: '/terminal' },
  { key: 'approve', path: '/approvals' },
  { key: 'db', path: '/admin/connections', admin: true },
  { key: 'rules', path: '/admin/risk-rules', admin: true },
  { key: 'envtier', path: '/admin/env-tiers', admin: true },
  { key: 'perms', path: '/admin/permissions', admin: true },
  { key: 'audit', path: '/audit' },
  { key: 'settings', path: '/admin/settings', admin: true },
  { key: 'query', path: '/query' },
  { key: 'schemadiff', path: '/schema-diff' },
  { key: 'inspection', path: '/inspection' },
]

// 主动登出后,本标签页不再自动 SSO 登录(见 logout)。
export const SSO_SKIP_KEY = 'aegisdb_sso_skip'

export const useAuthStore = defineStore('auth', () => {
  // 会话标记,不是凭据(lib/session.ts):真正的令牌在 HttpOnly Cookie 里,脚本读不到。
  dropLegacyToken()
  const token = ref<string>(sessionMarker())
  const me = ref<Me | null>(null)
  const loaded = ref(false)

  const menus = computed(() => me.value?.menus ?? {})
  const pendingCount = ref(0)

  // 右上角"待办":此刻在等我动手的工单。与 pendingCount 不是一回事 —— 那个是"我看得见的
  // 待审批",包括我自己发起、正在等别人审的单子;把它当待办,人点进去找不到任何能按的
  // 按钮,很快就学会无视那枚徽章。
  //
  // 放在 store 里是因为两处会改它:布局的轮询,以及审批页 / 总览页上刚做完的动作(批完、
  // 跑完立刻少一条,不等下一轮轮询)。
  const todo = ref<ApprovalTodo>({ decide: 0, execute: 0, confirm: 0, items: [] })
  const todoTotal = computed(() => todo.value.decide + todo.value.execute + todo.value.confirm)
  async function refreshTodo() {
    if (!token.value) return
    try {
      const r = await api.approvalTodo()
      todo.value = {
        decide: Number(r?.decide) || 0,
        execute: Number(r?.execute) || 0,
        confirm: Number(r?.confirm) || 0,
        items: Array.isArray(r?.items) ? r.items : [],
      }
    } catch { /* 网络抖一下不该把已有的数清零 */ }
  }

  // First route the user can actually see, or '' when they have no menus at all
  // (the router guard turns '' into a login redirect instead of looping — L12).
  // 只开了数据查询页的账户(业务方)。登录落地与总览都直接送去 /query。
  // 巡检菜单不算:迁移给 admin/owner/l2 回填了 inspection,一个被定制成只有「查询」的 DBA L2 会因为多出这一项
  // 不再算"只开查询"(2026-10-26 回归排查 19)。巡检入口照样按 menus.inspection 出现在导航里。
  const onlyQuery = computed(() => !!menus.value.query && Object.entries(menus.value).every(([k, v]) => !v || k === 'query' || k === 'inspection'))

  // 平台管理员 / 审计员:管理后台的门(工单 01 的后端校验是同一口径,这里只决定界面)。
  const roleCodes = computed(() => me.value?.roleCodes ?? (me.value?.roleCode ? [me.value.roleCode] : []))
  const isAdmin = computed(() => roleCodes.value.includes('admin'))
  const isAuditor = computed(() => roleCodes.value.includes('audit'))
  const canEnterAdmin = computed(() => isAdmin.value || isAuditor.value)
  // DBA 负责人(L1):进不了管理后台,但看得到所有人的审计 —— 在用户后台的「操作记录」与总览卡片上
  // (2026-10-14 用户确认;后端 service.auditActor 同一口径)。
  const isDBALead = computed(() => roleCodes.value.includes('owner'))
  const seesAllAudit = computed(() => canEnterAdmin.value || isDBALead.value)
  // 能看 AI 巡检报告:有菜单,且是平台管理员 / DBA 负责人 / DBA L2(后端 InspectionViewer 同一口径)
  const canViewInspection = computed(() => !!menus.value.inspection && roleCodes.value.some((c) => c === 'admin' || c === 'owner' || c === 'l2'))
  // 管理后台的落点:管理员进管理总览,审计员只有审计。
  const adminHome = computed(() => (isAdmin.value ? '/admin/overview' : isAuditor.value ? '/admin/audit' : ''))

  const firstVisibleRoute = computed(() => {
    const hit = ROUTE_ORDER.find((r) => menus.value[r.key] && (!r.admin || isAdmin.value))
    return hit?.path ?? ''
  })


  // 刚登录进来的那一次(不是刷新页面)。布局拉到第一份待办时消费掉它 —— 提示音只该在
  // "刚上班"时响一声,而带着 token 按 F5 不是刚上班。
  //
  // 顺带:浏览器的自动播放策略也要求有过用户手势,而登录那一下点击正是它;刷新出来的页面
  // 没有手势,就算想响也响不了(见 lib/chime.ts)。两条理由指向同一个做法。
  const justLoggedIn = ref(false)
  function consumeJustLoggedIn(): boolean {
    if (!justLoggedIn.value) return false
    justLoggedIn.value = false
    return true
  }

  function adopt(data: LoginResp) {
    token.value = data.session
    me.value = data.user
    loaded.value = true
    justLoggedIn.value = true
    setSessionMarker(data.session)
    try { sessionStorage.removeItem(SSO_SKIP_KEY) } catch { /* 隐私模式 */ }
  }

  async function login(email: string, password: string, mfaCode = '') {
    adopt(await api.login(email, password, mfaCode))
  }

  // SSO 登录:凭浏览器带上的 SSO Cookie 换网关会话。与口令登录落到同一个 adopt ——
  // 两种登录进来之后的状态不该有任何区别。
  async function loginSSO(mfaCode = '') {
    adopt(await api.ssoLogin(mfaCode))
  }

  // 改密码后服务端作废了旧 token、当场重签一枚:只换 token,身份与菜单不变。
  function replaceToken(t: string) {
    token.value = t
    setSessionMarker(t)
  }

  async function fetchMe() {
    if (!token.value) return
    me.value = await api.me()
    loaded.value = true
  }

  // clearSession wipes all local auth state WITHOUT calling the server. Used by
  // the 401 interceptor so an expired session doesn't leave a zombie UI showing
  // the previous user's menus/identity (R21).
  function clearSession() {
    token.value = ''
    me.value = null
    loaded.value = false
    todo.value = { decide: 0, execute: 0, confirm: 0, items: [] }
    clearSessionMarker()
  }

  function logout() {
    // Revoke the session server-side (bumps token version, clears the cookies).
    // Local state goes first; the request still carries the cookies (M1/R6).
    const t = token.value
    clearSession()
    if (t) api.logout().catch(() => {})
    // 主动登出 = 人要离开这台电脑:数据查询的 SQL 草稿一并清掉(2026-10 安全审计 L-FE1)。
    // 会话过期被踢回登录页(clearSession)不清 —— 同一个人重新登录还要接着写。
    // 收藏的常用查询(aegisdb_query_saved_*)不清:那是人主动存下的,不是随手的草稿。
    try {
      for (const k of Object.keys(localStorage)) if (k.startsWith('aegisdb_query_tabs_')) localStorage.removeItem(k)
    } catch { /* 隐私模式 */ }
    // 主动登出之后,回到登录页不要自动再用 SSO 登进来 —— SSO 的 Cookie 还在,不拦这一下,
    // "登出"按下去的结果是一眨眼又登录了。只在本标签页内有效:关掉重开就恢复自动登录。
    try { sessionStorage.setItem(SSO_SKIP_KEY, '1') } catch { /* 隐私模式 */ }
  }

  return { isAdmin, isAuditor, canEnterAdmin, isDBALead, seesAllAudit, canViewInspection, adminHome, loginSSO, token, me, loaded, menus, pendingCount, todo, todoTotal, refreshTodo, firstVisibleRoute, onlyQuery, login, fetchMe, replaceToken, logout, clearSession, consumeJustLoggedIn }
})
