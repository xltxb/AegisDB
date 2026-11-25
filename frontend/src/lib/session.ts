// 控制台会话的前端这一半(2026-11 审计 V-16)。
//
// 真正的凭据是 HttpOnly Cookie(aegisdb_session),脚本读不到,页面上就算出现 XSS 也拿不走。前端手里只有两样
// 不是凭据的东西:
//   - localStorage 的 aegisdb_session:会话标记 "<uid>.<csrf>",用来知道已登录、分辨"还是不是同一个人";
//   - Cookie 的 aegisdb_csrf:写操作放进 X-CSRF-Token,WebSocket 握手放进子协议。

const SESSION_KEY = 'aegisdb_session'

/** 升级前存的是 JWT 本身。留着它就等于没有迁移,读到就删 —— 代价是升级后要重新登录一次。 */
export function dropLegacyToken() {
  try { localStorage.removeItem('aegisdb_token') } catch { /* 隐私模式 */ }
}

export const sessionMarker = () => localStorage.getItem(SESSION_KEY) || ''
export const setSessionMarker = (m: string) => localStorage.setItem(SESSION_KEY, m)
export const clearSessionMarker = () => localStorage.removeItem(SESSION_KEY)

/** 标记里的用户 id;认不出返回 0。 */
export function markerUid(marker: string): number {
  return Number(marker.split('.')[0]) || 0
}

export function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)aegisdb_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

/** 每个控制台请求都带的头。走 fetch 的地方用它,axios 由拦截器统一加。 */
export function sessionHeaders(): Record<string, string> {
  const csrf = csrfToken()
  return { 'X-AegisDB-Client': 'web', ...(csrf ? { 'X-CSRF-Token': csrf } : {}) }
}
