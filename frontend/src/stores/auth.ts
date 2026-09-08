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

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem('vela_token') || '')
  const me = ref<Me | null>(null)
  const loaded = ref(false)

  const menus = computed(() => me.value?.menus ?? {})
  const pendingCount = ref(0)

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

  return { token, me, loaded, menus, pendingCount, firstVisibleRoute, login, fetchMe, logout, clearSession }
})
