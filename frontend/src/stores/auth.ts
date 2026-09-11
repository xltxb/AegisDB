import { create } from 'zustand'
import http, { ok, TOKEN_KEY, type Envelope } from '@/api/http'
import type { Me, LoginResp } from '@/types'

/** 能力矩阵的一格:能力 × 分层 → 档位。 */
export type CapLevel = 'allow' | 'approve' | 'deny'

/** 双入口(前端文档 v2.0):运维前台 / 管理后台。 */
export type Portal = 'frontend' | 'backend'

interface AuthState {
  token: string
  me: Me | null
  portal: Portal
  setPortal: (p: Portal) => void
  login: (email: string, password: string, mfaCode?: string, portal?: Portal) => Promise<Me>
  logout: () => void
  clearSession: () => void
}

const PORTAL_KEY = 'aegis_portal'

/**
 * auth 只存**客户端**状态:令牌、身份、选中的入口。
 *
 * 注意 `me` 在这里是一份缓存而非真相 —— 真相由 `meQueryOptions` 从服务端取,登录
 * 与守卫把结果写回这里,供同步读取(守卫在 loader 里跑,拿不到 hook)。
 */
export const useAuthStore = create<AuthState>()((set, get) => ({
  token: localStorage.getItem(TOKEN_KEY) || '',
  me: null,
  portal: (localStorage.getItem(PORTAL_KEY) as Portal) || 'frontend',

  setPortal(p) {
    localStorage.setItem(PORTAL_KEY, p)
    set({ portal: p })
  },

  async login(email, password, mfaCode = '', portal) {
    const data = await http
      .post<unknown, Envelope<LoginResp>>('/auth/login', { email, password, mfaCode })
      .then(ok)
    localStorage.setItem(TOKEN_KEY, data.token)
    if (portal) localStorage.setItem(PORTAL_KEY, portal)
    set({ token: data.token, me: data.user, ...(portal ? { portal } : {}) })
    return data.user
  },

  logout() {
    // 先把令牌取出来再清本地:请求拦截器读的是 localStorage,清早了这次注销
    // 就会不带凭据发出去,服务端那边的令牌代次永远不会 +1。
    const t = get().token
    get().clearSession()
    if (t) {
      http.post('/auth/logout', null, { headers: { Authorization: `Bearer ${t}` } }).catch(() => {})
    }
  },

  clearSession() {
    localStorage.removeItem(TOKEN_KEY)
    set({ token: '', me: null })
  },
}))

/** 所有角色的并集 —— 一个人可以挂多个角色,权限取并集,不能只看主角色。 */
export function roleCodesOf(me: Me | null): string[] {
  if (!me) return []
  return me.roleCodes?.length ? me.roleCodes : me.roleCode ? [me.roleCode] : []
}

export function isAdminOf(me: Me | null): boolean {
  return roleCodesOf(me).includes('admin')
}
