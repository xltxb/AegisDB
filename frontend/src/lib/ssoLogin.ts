// SSO(OIDC)登录的前端两件事:拼发起地址,以及把回调带回来的失败原因认出来。
//
// 整个授权码流程在服务端:登录页只把浏览器送到网关的发起地址(GET api/v1/auth/oidc/start),
// 身份服务登录完成后网关跳回 #/login?sso=1(成功,登录页再换会话)或 #/login?sso_error=<原因>。

/** 回调失败时网关带回来的原因。与后端 service.OIDCErr* 一一对应。 */
export const SSO_ERRORS = ['disabled', 'state', 'denied', 'exchange', 'email'] as const
export type SsoError = (typeof SSO_ERRORS)[number]

/**
 * 发起地址。服务端给的是相对网关根的地址(api/v1/auth/oidc/start),按当前页面的位置解析 ——
 * 网关可能挂在子路径下(/gw/),不能当作站点根。
 */
export function ssoStartHref(loginUrl: string, loc: { origin: string; pathname: string }): string {
  return new URL(loginUrl, loc.origin + loc.pathname).href
}

/** 不认识的原因(新版后端、被人改过的地址)按"换取失败"说,不原样显示地址栏里的文字。 */
export function ssoErrorKey(raw: string | undefined): SsoError | null {
  if (!raw) return null
  return (SSO_ERRORS as readonly string[]).includes(raw) ? (raw as SsoError) : 'exchange'
}
