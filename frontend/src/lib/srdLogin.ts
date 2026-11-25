// SRD Portal 授权登录的前端两件事:拼跳转地址,以及认出"刚从 SRD 回来却没拿到登录态"。

/** 跳去 SRD 之前在 sessionStorage 里记下时间,回来时据此判断是不是刚授权完。 */
export const SRD_REDIRECTED_KEY = 'vela_srd_redirected_at'

/**
 * {SRD}/auth/login?redirect=<网关登录页>。回跳地址整体 encodeURIComponent(文档要求);
 * 回到登录页(hash 路由 #/login),登录页一打开就会静默用 CS-Token 换网关会话。
 */
export function srdLoginHref(loginUrl: string, loc: { origin: string; pathname: string }): string {
  const back = `${loc.origin}${loc.pathname}#/login`
  const sep = loginUrl.includes('?') ? '&' : '?'
  return `${loginUrl}${sep}redirect=${encodeURIComponent(back)}`
}

/**
 * 刚从 SRD 授权回来(几分钟内)。这时网关若仍说"没有登录态",不是人没登录,而是浏览器没把
 * CS-Token 带过来 —— 多半是网关域名不在 SRD Cookie 的父域下。再让人点按钮只会在 SRD 与
 * 网关之间来回跳,要把原因说出来。
 */
export function justReturnedFromSrd(markedAt: string | null, now: number, windowMs = 5 * 60_000): boolean {
  const t = Number(markedAt)
  return Number.isFinite(t) && t > 0 && now - t >= 0 && now - t < windowMs
}
