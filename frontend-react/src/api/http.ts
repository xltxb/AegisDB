import axios, { type AxiosInstance } from 'axios'

/** 统一响应信封:后端一律 HTTP 200,业务结果在包体里(后端文档 §06)。 */
export interface Envelope<T = unknown> {
  code: number
  msg: string
  data: T
}

export {
  CODE_OK, CODE_INTERCEPTED, CODE_SCRIPT_PATH_UNSET, CODE_EXPORT_PATH_UNSET,
  CODE_MFA_REQUIRED, CODE_LOGIN_THROTTLED, CODE_FORBIDDEN, CODE_IP_NOT_ALLOWED,
} from './codes'
import { CODE_OK } from './codes'

export const TOKEN_KEY = 'aegis_token'

const http: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api/v1',
  timeout: 15000,
})

http.interceptors.request.use((cfg) => {
  const token = localStorage.getItem(TOKEN_KEY)
  if (token) cfg.headers.Authorization = `Bearer ${token}`
  return cfg
})

/**
 * 响应拦截器只做两件事:把信封原样交出去,以及在 401 时清掉本地会话。
 *
 * 刻意**不**在这里对 `code !== 0` 抛错。有几个业务码是正常流程的一部分 ——
 * 42200 是"命中高危,已经给你建了审批单",42800 是"再输一次动态码",它们都带着
 * 调用方要用的数据。统一抛错会把这些信息碾平成一个 Error,于是每个调用点又得
 * 把它拆回来。解包交给 `ok()`,需要看码的地方自己看。
 */
http.interceptors.response.use(
  (res) => res.data,
  (err) => {
    if (err.response?.status === 401) {
      localStorage.removeItem(TOKEN_KEY)
      // 动态取,避免 http ↔ store 的循环依赖。
      import('@/stores/auth').then((m) => m.useAuthStore.getState().clearSession()).catch(() => {})
      if (!location.pathname.startsWith('/login')) location.assign('/login')
    }
    return Promise.reject(err)
  },
)

/** ok 解开信封;业务失败时抛出带 code 的错误,交给 TanStack Query 的错误通道。 */
export function ok<T>(env: Envelope<T>): T {
  if (env.code !== CODE_OK) {
    throw Object.assign(new Error(env.msg || '请求失败'), { code: env.code, data: env.data })
  }
  return env.data
}

export default http
