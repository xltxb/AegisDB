import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/api'
import type { Me } from '@/types'

// Route order used to pick the first visible menu after login.
// 总览不在这张表里,而这是有意的:这张表回答的是"这个人有没有任何一处能去",
// 用来决定被拒之后把他送到哪、以及一个菜单都没有的账户该被弹回登录页。总览人人
// 可见,写进来会让那个判断永远为真。
const ROUTE_ORDER: { key: string; path: string }[] = [
  { key: 'terminal', path: '/terminal' },
  { key: 'approve', path: '/approvals' },
  { key: 'db', path: '/connections' },
  { key: 'rules', path: '/risk-rules' },
  { key: 'envtier', path: '/env-tiers' },
  { key: 'perms', path: '/permissions' },
  { key: 'audit', path: '/audit' },
  { key: 'settings', path: '/settings' },
]

/** Capability levels as the server stores them (`cap -> tier -> level`). */
export type CapLevel = 'allow' | 'approve' | 'deny'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem('vela_token') || '')
  const me = ref<Me | null>(null)
  const loaded = ref(false)

  const menus = computed(() => me.value?.menus ?? {})
  const pendingCount = ref(0)

  // 一个用户可以挂多个角色,权限取**所有角色的并集** —— 所以任何"我是不是管理员"
  // 的判断都必须看 roleCodes,不能只看 roleCode(那只是主角色,用于显示)。
  // 这在页面里曾经有十份各写各的实现、三种公式,其中一份只看主角色,于是副角色
  // 是 admin 的人在那一页是只读的。判断收在这里一处,页面只读 `auth.isAdmin`。
  const roleCodes = computed<string[]>(() => {
    const m = me.value
    if (!m) return []
    const codes = m.roleCodes?.length ? m.roleCodes : m.roleCode ? [m.roleCode] : []
    return codes
  })

  const isAdmin = computed(() => roleCodes.value.includes('admin'))

  /**
   * levelOf 读能力矩阵的一格:`能力 × 分层 → allow | approve | deny`。
   *
   * 矩阵还没到手时返回 ''(未知)。调用方按未知处理,不要当成 deny —— 见 `can`。
   */
  function levelOf(capability: string, tier: string): CapLevel | '' {
    const cell = me.value?.capabilities?.[capability]?.[tier]
    return (cell as CapLevel) || ''
  }

  /**
   * can 回答"这一格该不该在界面上灰掉",接受 `能力:分层`(如 `ddl:prod`)。
   *
   * 两条刻意的取舍:
   *
   * - **只有 `deny` 才算不能**。`approve` 是"可以做,但要走审批",把它也灰掉等于
   *   让人根本提不出那张单 —— 而提单正是审批流程的入口。
   * - **未知一律放行**。这是展示层收敛,不是闸门:真正的拦截在服务端,每一条命令
   *   下发前还要再判一次。矩阵没加载完就把整屏按钮灰掉,只会让人以为自己没权限。
   */
  function can(expr: string): boolean {
    const [capability, tier] = expr.split(':')
    if (!capability || !tier) return true
    return levelOf(capability, tier) !== 'deny'
  }

  // First route the user can actually see, or '' when they have no menus at all
  // (the router guard turns '' into a login redirect instead of looping — L12).
  const firstVisibleRoute = computed(() => {
    const hit = ROUTE_ORDER.find((r) => menus.value[r.key])
    return hit?.path ?? ''
  })

  async function login(email: string, password: string, mfaCode = '') {
    const data = await api.login(email, password, mfaCode)
    token.value = data.token
    me.value = data.user
    loaded.value = true
    localStorage.setItem('vela_token', data.token)
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
    localStorage.removeItem('vela_token')
  }

  function logout() {
    // Revoke the token server-side (bumps token version). Pass the token to the
    // request explicitly and clear local state after — otherwise the request
    // interceptor reads an already-emptied localStorage and the call goes out
    // without credentials, so the server never revokes it (M1/R6).
    const t = token.value
    clearSession()
    if (t) api.logout(t).catch(() => {})
  }

  return {
    token, me, loaded, menus, pendingCount, firstVisibleRoute,
    roleCodes, isAdmin, levelOf, can,
    login, fetchMe, logout, clearSession,
  }
})
