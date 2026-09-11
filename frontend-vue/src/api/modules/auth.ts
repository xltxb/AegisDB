import { http, ok, type Envelope } from '../shared'
import type {
  LoginResp, Me, Member, Notification,
} from '@/types'

export const authApi = {
  // ---- auth ----
  login: (email: string, password: string, mfaCode = '') =>
    http.post<any, Envelope<LoginResp>>('/auth/login', { email, password, mfaCode }).then(ok),
  me: () => http.get<any, Envelope<Me>>('/auth/me').then(ok),
  // Carry the token explicitly: the caller clears it from localStorage right
  // after, so the request interceptor can't attach it in time (R6).
  logout: (token?: string) =>
    http.post('/auth/logout', null, token ? { headers: { Authorization: `Bearer ${token}` } } : undefined),
  approvalChain: () => http.get<any, Envelope<{ chain: Member[] }>>('/approval-chain').then(ok),

  // ---- MFA (TOTP) ----
  mfaSetup: () =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>('/auth/mfa/setup').then(ok),
  mfaEnable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/enable', { code }).then(ok),
  mfaDisable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/disable', { code }).then(ok),

  // ---- notifications ----
  notifications: (limit = 30) =>
    http.get<any, Envelope<{ items: Notification[]; unread: number }>>(`/notifications?limit=${limit}`).then(ok),
  markNotificationsRead: (ids: number[] = []) =>
    http.post<any, Envelope<any>>('/notifications/read', { ids }).then(ok),
}
