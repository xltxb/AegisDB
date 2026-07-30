import axios, { type AxiosInstance } from 'axios'

// Unified envelope { code, msg, data }.
export interface Envelope<T = any> {
  code: number
  msg: string
  data: T
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
import { CODE_OK } from './codes'

const http: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api/v1',
  timeout: 15000,
})

// Attach Bearer token from localStorage.
http.interceptors.request.use((cfg) => {
  const token = localStorage.getItem('vela_token')
  if (token) cfg.headers.Authorization = `Bearer ${token}`
  return cfg
})

// Return the envelope; on HTTP 401 reset the whole auth store (not just the
// token) so the app doesn't linger in a zombie state, then redirect to login.
http.interceptors.response.use(
  (res) => res.data,
  (err) => {
    if (err.response?.status === 401) {
      // Lazy import breaks the http ↔ auth-store dependency cycle (R21).
      import('@/stores/auth')
        .then((m) => m.useAuthStore().clearSession())
        .catch(() => localStorage.removeItem('vela_token'))
      if (location.hash !== '#/login') location.hash = '#/login'
    }
    return Promise.reject(err)
  },
)

// ok unwraps an envelope, throwing on business errors (except the caller checks code itself).
export function ok<T>(env: Envelope<T>): T {
  if (env.code !== CODE_OK) throw Object.assign(new Error(env.msg), { code: env.code, data: env.data })
  return env.data
}

export default http
