import axios, { type AxiosInstance } from 'axios'
import { i18n } from '@/locales'
import { renderApiMsg, parseRef } from '@/lib/apiMsg'
import { renderOutText, renderRule } from '@/lib/ruleText'
import type { RuleRef } from '@/types'

// Unified envelope { code, msg, data }. msgRef: see lib/apiMsg.
export interface Envelope<T = any> {
  code: number
  msg: string
  msgRef?: RuleRef | null
  data: T
}

const globalI18n = {
  t: (k: string, args?: unknown) => i18n.global.t(k, args as never),
  te: (k: string) => i18n.global.te(k),
}

// 嵌在一句话里的子句可能来自别的命名空间:终端输出(outText)或拦截规则(ruleText)。
const foreign = (r: RuleRef) => renderOutText(r, '', globalI18n) || renderRule(r, '', globalI18n)

// localizeMsg 把服务端的中文报错按当前界面语言重讲一遍(认不出就原样返回)。
// 中文界面直接用原句:中文用户看到的仍是服务端那句话,一字不差。
export function localizeMsg(msg: string, ref?: RuleRef | null): string {
  if (!ref || i18n.global.locale.value === 'zh') return msg
  return renderApiMsg(ref, msg, globalI18n, foreign)
}

// localizeText:接口数据里带着 ref 的中文(通知、任务错误、审批执行结果、各种提示)按界面语言显示。
// ref 可以是对象,也可以是存库的 JSON 串;apiMsg 与终端输出(outText)两种 code 都认。
export function localizeText(text: string | null | undefined, ref?: RuleRef | string | null): string {
  const t = text || ''
  const r = typeof ref === 'string' ? parseRef(ref) : ref
  if (!r || i18n.global.locale.value === 'zh') return t
  return renderOutText(r, t, globalI18n)
}

// localizeLines:多行文本(异步执行日志)里网关自己写的那几行按界面语言显示。lineRefs 是
// [{t: 原文, r: ref}] 的 JSON(apimsg.LineRef);逐行比对原文,对得上的换成译文,其余(数据库的
// NOTICE 原话)原样。不按行号对位:日志只留末尾时行号会变。
export function localizeLines(text: string | null | undefined, lineRefs?: string | null): string {
  const t = text || ''
  if (!lineRefs || i18n.global.locale.value === 'zh') return t
  let refs: Array<{ t: string; r: RuleRef }>
  try { refs = JSON.parse(lineRefs) } catch { return t }
  if (!Array.isArray(refs) || !refs.length) return t
  const byText = new Map(refs.map((x) => [x.t, x.r]))
  return t.split('\n').map((line) => (byText.has(line) ? localizeText(line, byText.get(line)) : line)).join('\n')
}

// Re-exported from api/codes so pure logic can import the codes without pulling
// in this axios client (which needs bundler-provided import.meta.env).
export {
  CODE_OK,
  CODE_INTERCEPTED,
  CODE_SCRIPT_PATH_UNSET,
  CODE_EXPORT_PATH_UNSET,
  CODE_MFA_REQUIRED,
} from './codes'
import { CODE_OK, CODE_UNAUTHORIZED, CODE_ADMIN_IP_BLOCKED, CODE_ADMIN_IDLE } from './codes'

import { sessionHeaders, clearSessionMarker } from '@/lib/session'

const http: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api/v1',
  timeout: 15000,
})

// 会话在 HttpOnly Cookie 里,浏览器自己带;这里只加声明与 CSRF 头(lib/session.ts)。
http.interceptors.request.use((cfg) => {
  for (const [k, v] of Object.entries(sessionHeaders())) cfg.headers[k] = v
  return cfg
})

// Reset the whole auth store (not just the token) so the app doesn't linger in a
// zombie state, then redirect to login.
function endSession() {
  // 标记先同步清掉,再跳登录页:store 是懒加载的(异步),只靠它清,地址会先变、标记后清,
  // 登录页挂载时可能还读到旧标记。
  clearSessionMarker()
  // Lazy import breaks the http ↔ auth-store dependency cycle (R21).
  import('@/stores/auth')
    .then((m) => m.useAuthStore.getState().clearSession())
    .catch(() => clearSessionMarker())
  if (location.hash !== '#/login') location.hash = '#/login'
}

declare module 'axios' {
  interface AxiosRequestConfig {
    /** 40303 由调用方自己提示,拦截器不发 aegisdb:admin-ip-blocked 事件 */
    quietAdminGate?: boolean
  }
}

// Return the envelope. Auth failures arrive as HTTP 200 + code 40100 (the
// backend's resp.Abort never sets 401), so the envelope code is what counts;
// HTTP 401 is still honoured in case a proxy in front answers with it.
http.interceptors.response.use(
  (res) => {
    // 报错的 msg 换成读者语言:所有 toast、表单错误条、直接读 env.msg 的页面都经过这里
    if (res.data?.msgRef && typeof res.data.msg === 'string') res.data.msg = localizeMsg(res.data.msg, res.data.msgRef)
    if (res.data?.code === CODE_UNAUTHORIZED) endSession()
    // 管理后台 IP 白名单拒绝:交给布局去提示并退回用户后台(这里不引路由,免得循环依赖)
    // quietAdminGate:调用方自己处理(路由守卫里的进入管理后台),不再发事件,免得提示两遍
    if (res.data?.code === CODE_ADMIN_IP_BLOCKED && !res.config.quietAdminGate) {
      window.dispatchEvent(new CustomEvent('aegisdb:admin-ip-blocked', { detail: res.data?.msg || '' }))
    }
    // 管理后台空闲超时:同样交给布局,退回用户后台(工单 09)
    if (res.data?.code === CODE_ADMIN_IDLE) {
      window.dispatchEvent(new CustomEvent('aegisdb:admin-idle', { detail: res.data?.msg || '' }))
    }
    return res.data
  },
  (err) => {
    if (err.response?.status === 401) endSession()
    return Promise.reject(err)
  },
)

// ok unwraps an envelope, throwing on business errors (except the caller checks code itself).
export function ok<T>(env: Envelope<T>): T {
  // 绕过 axios 的 fetch 下载(导出、程序包)也走这里,所以这里再翻一次(已翻过的同样结果)
  if (env.code !== CODE_OK) throw Object.assign(new Error(localizeMsg(env.msg, env.msgRef)), { code: env.code, data: env.data })
  return env.data
}

// envError:页面自己检查 env.code 时用它包成错误,带着 code —— ui.notifyError 据此认出 40303 / 40304
// (布局已统一提示过)不再报第二遍。从前写 new Error(env.msg),code 丢了,提示就弹两遍。
export function envError(env: { code: number; msg: string; msgRef?: RuleRef | null; data?: unknown }): Error {
  return Object.assign(new Error(localizeMsg(env.msg, env.msgRef)), { code: env.code, data: env.data })
}

export default http
