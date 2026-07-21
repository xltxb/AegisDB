import http, { ok, type Envelope } from './http'
import type {
  Approval, AuditRow, Connection, ConnectionSchema, ExecResp, ExportJob, LoginResp, Me, Member,
  Notification, RiskCheckResp, RiskCommandView, RoleBrief, RoleDetail, ScriptScanResp, ScriptUpload,
  SettingsResp, UserView, WebhookConfig, WebhookDelivery,
} from '@/types'

export const api = {
  // ---- auth ----
  login: (email: string, password: string) =>
    http.post<any, Envelope<LoginResp>>('/auth/login', { email, password }).then(ok),
  me: () => http.get<any, Envelope<Me>>('/auth/me').then(ok),
  // Carry the token explicitly: the caller clears it from localStorage right
  // after, so the request interceptor can't attach it in time (R6).
  logout: (token?: string) =>
    http.post('/auth/logout', null, token ? { headers: { Authorization: `Bearer ${token}` } } : undefined),
  approvalChain: () => http.get<any, Envelope<{ chain: Member[] }>>('/approval-chain').then(ok),

  // ---- notifications ----
  notifications: (limit = 30) =>
    http.get<any, Envelope<{ items: Notification[]; unread: number }>>(`/notifications?limit=${limit}`).then(ok),
  markNotificationsRead: (ids: number[] = []) =>
    http.post<any, Envelope<any>>('/notifications/read', { ids }).then(ok),

  // ---- terminal ----
  riskCheck: (connectionId: number, sql: string) =>
    http.post<any, Envelope<RiskCheckResp>>('/risk/check', { connectionId, sql }).then(ok),
  // exec returns the raw envelope so callers can detect 42200 (intercept) / 42800 (MFA).
  exec: (connectionId: number, sql: string, reason = '', mfaCode = '', database = '') =>
    http.post<any, Envelope<ExecResp>>('/terminal/exec', { connectionId, sql, reason, mfaCode, database }),
  gatewayStats: () =>
    http.get<any, Envelope<{ online: boolean; p50Ms: number; p95Ms: number; samples: number }>>('/gateway/stats').then(ok),
  scriptConfig: () =>
    http.get<any, Envelope<{ enabled: boolean; savePath: string }>>('/scripts/config').then(ok),

  // ---- data export ----
  exportConfig: () =>
    http.get<any, Envelope<{ enabled: boolean; savePath: string }>>('/export/config').then(ok),
  // raw envelope so callers can detect 42601 (path unset); returns the new job.
  exportData: (connectionId: number, sql: string, name = '', database = '') =>
    http.post<any, Envelope<ExportJob>>('/export', { connectionId, sql, name, database }),
  exportJobs: () => http.get<any, Envelope<ExportJob[]>>('/export/jobs').then(ok),
  exportDownload: (file: string) =>
    http.get<any, Blob>(`/export/download?file=${encodeURIComponent(file)}`, { responseType: 'blob' }),
  scriptScan: (content: string, filename: string, connectionId = 0) =>
    http.post<any, Envelope<ScriptScanResp>>('/scripts/scan', { content, filename, connectionId }).then(ok),
  scriptExecute: (content: string, filename: string, connectionId: number, mfaCode = '', uploadId = 0, database = '') =>
    http.post<any, Envelope<any>>('/scripts/execute', { content, filename, connectionId, mfaCode, uploadId, database }),
  // ---- uploaded script files (per-user) ----
  scriptUploads: () => http.get<any, Envelope<ScriptUpload[]>>('/scripts/uploads').then(ok),
  // raw envelope so callers can detect 42600 (path unset).
  scriptUpload: (content: string, filename: string) =>
    http.post<any, Envelope<ScriptUpload>>('/scripts/upload', { content, filename }),
  scriptUploadDelete: (id: number) => http.delete<any, Envelope<any>>(`/scripts/uploads/${id}`).then(ok),
  scriptUploadDownload: (id: number) =>
    http.get<any, Blob>(`/scripts/uploads/${id}/download`, { responseType: 'blob' }),
  scriptUploadContent: (id: number) =>
    http.get<any, Envelope<{ content: string; filename: string }>>(`/scripts/uploads/${id}/content`).then(ok),

  // ---- MFA (TOTP) ----
  mfaSetup: () =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>('/auth/mfa/setup').then(ok),
  mfaEnable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/enable', { code }).then(ok),
  mfaDisable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/disable', { code }).then(ok),

  // ---- connections ----
  connections: () => http.get<any, Envelope<Connection[]>>('/connections').then(ok),
  createConnection: (body: Partial<Connection>) =>
    http.post<any, Envelope<Connection>>('/connections', body).then(ok),
  toggleConnection: (id: number, status?: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { status }).then(ok),
  setConnectionTags: (id: number, tags: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { tags }).then(ok),
  tags: () => http.get<any, Envelope<string[]>>('/tags').then(ok),
  testConnection: (id: number) => http.post<any, Envelope<any>>(`/connections/${id}/test`).then(ok),
  connectionSchema: (id: number) =>
    http.get<any, Envelope<ConnectionSchema>>(`/connections/${id}/schema`).then(ok),

  // ---- roles ----
  roles: () => http.get<any, Envelope<RoleBrief[]>>('/roles').then(ok),
  role: (id: number) => http.get<any, Envelope<RoleDetail>>(`/roles/${id}`).then(ok),
  updateRole: (id: number, body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }) =>
    http.patch<any, Envelope<RoleDetail>>(`/roles/${id}`, body).then(ok),
  setMenus: (id: number, menus: Record<string, boolean>) =>
    http.put(`/roles/${id}/menus`, { menus }),
  setCapabilities: (id: number, matrix: Record<string, Record<string, string>>) =>
    http.put(`/roles/${id}/capabilities`, { matrix }),
  setRoleTags: (id: number, tags: string[]) =>
    http.put<any, Envelope<RoleDetail>>(`/roles/${id}/tags`, { tags }).then(ok),
  addMember: (id: number, userId: number) =>
    http.post<any, Envelope<RoleDetail>>(`/roles/${id}/members`, { userId }).then(ok),
  removeMember: (id: number, userId: number) =>
    http.delete<any, Envelope<RoleDetail>>(`/roles/${id}/members/${userId}`).then(ok),

  // ---- users ----
  users: () => http.get<any, Envelope<UserView[]>>('/users').then(ok),
  patchUser: (id: number, body: { status?: string; roleId?: number; roleIds?: number[] }) =>
    http.patch(`/users/${id}`, body),
  invite: (email: string, roleId: number) => http.post('/users/invite', { email, roleId }),
  createUser: (body: { email: string; name?: string; password: string; roleIds: number[] }) =>
    http.post<any, Envelope<any>>('/users', body).then(ok),
  setUserRoles: (id: number, roleIds: number[]) => http.patch(`/users/${id}`, { roleIds }),
  // admin user management: password reset + OTP binding
  setUserPassword: (id: number, password: string) =>
    http.post<any, Envelope<any>>(`/users/${id}/password`, { password }).then(ok),
  resetUserMfa: (id: number) => http.post<any, Envelope<any>>(`/users/${id}/mfa/reset`).then(ok),
  bindUserMfa: (id: number) =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>(`/users/${id}/mfa/bind`).then(ok),

  // ---- risk commands ----
  riskCommands: () => http.get<any, Envelope<RiskCommandView[]>>('/risk-commands').then(ok),
  upsertRiskCommand: (command: string, env: Record<string, string>) =>
    http.post<any, Envelope<RiskCommandView[]>>('/risk-commands', { command, env }).then(ok),
  patchRiskCommand: (name: string, env: string, level: string) =>
    http.patch<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`, { env, level }).then(ok),
  deleteRiskCommand: (name: string) =>
    http.delete<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`).then(ok),

  // ---- approvals ----
  approvals: (scope: 'mine' | 'all') =>
    http.get<any, Envelope<Approval[]>>(`/approvals?scope=${scope}`).then(ok),
  approve: (id: number) => http.post(`/approvals/${id}/approve`),
  reject: (id: number) => http.post(`/approvals/${id}/reject`),

  // ---- audit ----
  audit: (risk: string, range = '') => http.get<any, Envelope<AuditRow[]>>(`/audit?risk=${risk}&range=${range}`).then(ok),
  auditExportUrl: (risk: string, range = '') => `${import.meta.env.VITE_API_BASE || '/api/v1'}/audit/export?risk=${risk}&range=${range}`,

  // ---- settings ----
  settings: () => http.get<any, Envelope<SettingsResp>>('/settings').then(ok),
  saveSettings: (body: Record<string, any>) => http.put('/settings', body),
  saveWebhook: (body: Partial<WebhookConfig>) =>
    http.put<any, Envelope<WebhookConfig>>('/settings/webhook', body).then(ok),
  testWebhook: () => http.post<any, Envelope<any>>('/settings/webhook/test').then(ok),
  testLark: () => http.post<any, Envelope<{ ok: boolean; message: string }>>('/settings/lark/test').then(ok),
  webhookDeliveries: (limit = 50) =>
    http.get<any, Envelope<WebhookDelivery[]>>(`/settings/webhook/deliveries?limit=${limit}`).then(ok),
}

export default api
