import http, { ok, type Envelope } from './http'
import type {
  APIClient, Approval, AsyncJob, AuditPage, AuditQuery, Connection, ConnectionSchema, DbObjects, EnvTier, Environment, ExecResp, ExecWindow,
  ExportJob, LoginResp, Me, Member, Notification, ObjectSource, Pipeline, Project, Release, ReleasePage, ReviewCatalog, SensitiveColumn, ServiceAccount,
  ReviewCheckResp, ReviewRule, RiskCheckResp, RiskCommandView, RoleBrief, RoleDetail,
  ScriptScanResp, ScriptUpload, SettingsResp, SnippetLimits, TerminalSnippet, UserView, WebhookConfig, WebhookDelivery,
} from '@/types'

// auditQS builds the audit query string, omitting empty filters. An absolute
// from/to window takes precedence over the relative range on the backend.
function auditQS(q: AuditQuery): string {
  const p = new URLSearchParams()
  p.set('risk', q.risk || 'all')
  if (q.from) p.set('from', q.from)
  if (q.to) p.set('to', q.to)
  if (!q.from && q.range) p.set('range', q.range)
  if (q.page) p.set('page', String(q.page))
  if (q.pageSize) p.set('pageSize', String(q.pageSize))
  return p.toString()
}

// Script scan/execute are bounded by the server side cap on script size (32MB),
// not by this. It is the ceiling on how long the browser will wait for work it
// knows is slow.
const SCRIPT_TIMEOUT_MS = 120000

export const api = {
  // ---- auth ----
  login: (email: string, password: string, mfaCode = '') =>
    http.post<any, Envelope<LoginResp>>('/auth/login', { email, password, mfaCode }).then(ok),
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
  // database 要传:执行窗口按库开,预检不带库名就判不出窗口,会比执行更严 ——
  // 终端先弹一个多余的审批理由框,提交后才发现根本不用审批。
  riskCheck: (connectionId: number, sql: string, database = '') =>
    http.post<any, Envelope<RiskCheckResp>>('/risk/check', { connectionId, sql, database }).then(ok),
  // exec returns the raw envelope so callers can detect 42200 (intercept) / 42800 (MFA).
  exec: (connectionId: number, sql: string, reason = '', mfaCode = '', database = '') =>
    http.post<any, Envelope<ExecResp>>('/terminal/exec', { connectionId, sql, reason, mfaCode, database }),
  // ---- async (background) long-running SQL exec ----
  execAsync: (connectionId: number, sql: string, database = '', reason = '') =>
    http.post<any, Envelope<{ jobId?: number; intercepted?: boolean; approvalNo?: string; risk?: string; rule?: string }>>('/terminal/exec-async', { connectionId, sql, database, reason }),
  asyncJobs: () => http.get<any, Envelope<AsyncJob[]>>('/async-jobs').then(ok),
  asyncJob: (id: number) => http.get<any, Envelope<AsyncJob>>(`/async-jobs/${id}`).then(ok),
  gatewayStats: () =>
    http.get<any, Envelope<{ online: boolean; p50Ms: number; p95Ms: number; samples: number; intercepts: number }>>('/gateway/stats').then(ok),
  scriptConfig: () =>
    http.get<any, Envelope<{ enabled: boolean; savePath: string }>>('/scripts/config').then(ok),

  // ---- data export ----
  exportConfig: () =>
    http.get<any, Envelope<{ enabled: boolean; savePath: string; retentionDays: number }>>('/export/config').then(ok),
  // raw envelope so callers can detect 42601 (path unset); returns the new job.
  // includeSensitive 要的是敏感字段的原值。勾上它的任务不会直接进队列，先去等审批。
  exportData: (connectionId: number, sql: string, name = '', database = '', includeSensitive = false) =>
    http.post<any, Envelope<ExportJob>>('/export', { connectionId, sql, name, database, includeSensitive }),
  exportJobs: () => http.get<any, Envelope<ExportJob[]>>('/export/jobs').then(ok),
  exportDownload: (file: string) =>
    http.get<any, Blob>(`/export/download?file=${encodeURIComponent(file)}`, { responseType: 'blob' }),
  // Records that a terminal session log was saved to a file. The file is built in
  // the browser from lines already displayed, so this call carries a DESCRIPTION
  // of the export and never the transcript — sending the session back to be
  // stored would create the very second copy the audit row exists to track.
  recordTranscriptExport: (body: { connectionId: number; filename: string; lines: number; dropped: number; database?: string }) =>
    http.post<any, Envelope<any>>('/terminal/transcript-export', body).then(ok),
  // Scanning is linear in the script: every statement is split, matched against
  // the dictionary and judged. A 6MB migration takes ~16s server-side, which the
  // default 15s client timeout aborts — leaving the operator with a network error
  // while the approval ticket it created goes on existing. These two get room to
  // finish; everything else keeps the shorter timeout, where a slow response is a
  // symptom rather than the expected cost.
  scriptScan: (content: string, filename: string, connectionId = 0, uploadId = 0) =>
    http.post<any, Envelope<ScriptScanResp>>('/scripts/scan', { content, filename, connectionId, uploadId },
      { timeout: SCRIPT_TIMEOUT_MS }).then(ok),
  scriptExecute: (content: string, filename: string, connectionId: number, mfaCode = '', uploadId = 0, database = '') =>
    http.post<any, Envelope<any>>('/scripts/execute', { content, filename, connectionId, mfaCode, uploadId, database },
      { timeout: SCRIPT_TIMEOUT_MS }),
  // ---- 执行窗口(「班车」) ----
  execWindows: () => http.get<any, Envelope<ExecWindow[]>>('/exec-windows').then(ok),
  createExecWindow: (body: Partial<ExecWindow>) =>
    http.post<any, Envelope<ExecWindow>>('/exec-windows', body),
  updateExecWindow: (id: number, body: Partial<ExecWindow>) =>
    http.put<any, Envelope<ExecWindow>>(`/exec-windows/${id}`, body),
  deleteExecWindow: (id: number) => http.delete<any, Envelope<any>>(`/exec-windows/${id}`),

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

  // ---- terminal snippets (per-user, hotkeys Alt+1…9) ----
  // No execute endpoint: a hotkey feeds the snippet's text to the line editor,
  // which submits it through riskCheck + exec like anything typed. Adding a
  // "run this snippet" route would be adding a second way into the gateway that
  // the first one's judgement doesn't cover.
  snippets: () => http.get<any, Envelope<TerminalSnippet[]>>('/snippets').then(ok),
  snippetLimits: () => http.get<any, Envelope<SnippetLimits>>('/snippets/limits').then(ok),
  // raw envelope: the caller shows the server's message, which names the actual
  // problem (name too long, body over the byte cap, hotkey out of range).
  snippetSave: (id: number, body: { name: string; body: string; slot: number }) =>
    id > 0
      ? http.put<any, Envelope<TerminalSnippet>>(`/snippets/${id}`, body)
      : http.post<any, Envelope<TerminalSnippet>>('/snippets', body),
  snippetDelete: (id: number) => http.delete<any, Envelope<any>>(`/snippets/${id}`).then(ok),

  // ---- MFA (TOTP) ----
  mfaSetup: () =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>('/auth/mfa/setup').then(ok),
  mfaEnable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/enable', { code }).then(ok),
  mfaDisable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/disable', { code }).then(ok),

  // ---- control tiers & environments ----
  // Reads are open to any signed-in user: the instance tree, connection form and
  // rule tables all render from them. Writes need the envtier menu + admin.
  envTiers: () => http.get<any, Envelope<EnvTier[]>>('/env-tiers').then(ok),
  // A tier MUST be cloned from an existing one — a tier with no rule rows is an
  // environment where every lookup falls through to allowed.
  createEnvTier: (body: Partial<EnvTier> & { templateCode: string }) =>
    http.post<any, Envelope<EnvTier>>('/env-tiers', body).then(ok),
  updateEnvTier: (code: string, body: Partial<EnvTier>) =>
    http.put<any, Envelope<EnvTier>>(`/env-tiers/${encodeURIComponent(code)}`, body).then(ok),
  deleteEnvTier: (code: string) =>
    http.delete<any, Envelope<any>>(`/env-tiers/${encodeURIComponent(code)}`).then(ok),

  environments: () => http.get<any, Envelope<Environment[]>>('/environments').then(ok),
  /** instance count per environment — what a delete is about to move. */
  environmentUsage: () =>
    http.get<any, Envelope<Record<string, number>>>('/environments/usage').then(ok),
  createEnvironment: (body: Partial<Environment>) =>
    http.post<any, Envelope<Environment>>('/environments', body).then(ok),
  updateEnvironment: (code: string, body: Partial<Environment>) =>
    http.put<any, Envelope<Environment>>(`/environments/${encodeURIComponent(code)}`, body).then(ok),
  /** moveTo is mandatory: instances are reassigned, never left dangling. */
  deleteEnvironment: (code: string, moveTo: string) =>
    http.delete<any, Envelope<any>>(`/environments/${encodeURIComponent(code)}`, {
      data: { moveTo },
    }).then(ok),

  // ---- connections ----
  connections: () => http.get<any, Envelope<Connection[]>>('/connections').then(ok),
  createConnection: (body: Partial<Connection>) =>
    http.post<any, Envelope<Connection>>('/connections', body).then(ok),
  updateConnection: (id: number, body: Partial<Connection>) =>
    http.put<any, Envelope<Connection>>(`/connections/${id}`, body).then(ok),
  toggleConnection: (id: number, status?: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { status }).then(ok),
  setConnectionTags: (id: number, tags: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { tags }).then(ok),
  setConnectionPolicy: (id: number, policy: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { policy }).then(ok),
  tags: () => http.get<any, Envelope<string[]>>('/tags').then(ok),
  testConnection: (id: number) => http.post<any, Envelope<any>>(`/connections/${id}/test`).then(ok),
  connectionSchema: (id: number, database = '') =>
    http.get<any, Envelope<ConnectionSchema>>(
      `/connections/${id}/schema${database ? `?database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  // database targets a specific database on the connection — REQUIRED for the
  // PostgreSQL family whenever the browsed database isn't the connection's
  // default one: its catalogs are per database, so omitting it makes the server
  // introspect the wrong catalog and answer "对象不存在" for everything.
  connectionObjects: (id: number, scope = '', database = '') =>
    http.get<any, Envelope<DbObjects>>(
      `/connections/${id}/objects?scope=${encodeURIComponent(scope)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  // Oracle 存储程序重新编译。它是一次 DDL,后端按终端同一套闸门判定并记审计。
  compileObject: (id: number, body: { scope: string; type: string; name: string; database?: string }) =>
    http.post<any, Envelope<{ ok: boolean; report: any }>>(`/connections/${id}/objects/compile`, body),
  objectSource: (id: number, scope: string, type: string, name: string, database = '') =>
    http.get<any, Envelope<ObjectSource>>(
      `/connections/${id}/object-source?scope=${encodeURIComponent(scope)}&type=${encodeURIComponent(type)}&name=${encodeURIComponent(name)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),

  // ---- roles ----
  roles: () => http.get<any, Envelope<RoleBrief[]>>('/roles').then(ok),
  role: (id: number) => http.get<any, Envelope<RoleDetail>>(`/roles/${id}`).then(ok),
  updateRole: (id: number, body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }) =>
    http.patch<any, Envelope<RoleDetail>>(`/roles/${id}`, body).then(ok),
  setMenus: (id: number, menus: Record<string, boolean>) =>
    http.put<any, Envelope<any>>(`/roles/${id}/menus`, { menus }).then(ok),
  setCapabilities: (id: number, matrix: Record<string, Record<string, string>>) =>
    http.put<any, Envelope<any>>(`/roles/${id}/capabilities`, { matrix }).then(ok),
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
  setUserRoles: (id: number, roleIds: number[]) =>
    http.patch<any, Envelope<any>>(`/users/${id}`, { roleIds }).then(ok),
  // Per-user data-access scope. An empty list clears it and the user falls back
  // to the scope their roles grant.
  userTags: (id: number) => http.get<any, Envelope<string[]>>(`/users/${id}/tags`).then(ok),
  setUserTags: (id: number, tags: string[]) =>
    http.put<any, Envelope<any>>(`/users/${id}/tags`, { tags }).then(ok),
  // admin user management: password reset + OTP binding
  setUserPassword: (id: number, password: string) =>
    http.post<any, Envelope<any>>(`/users/${id}/password`, { password }).then(ok),
  resetUserMfa: (id: number) => http.post<any, Envelope<any>>(`/users/${id}/mfa/reset`).then(ok),
  bindUserMfa: (id: number) =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>(`/users/${id}/mfa/bind`).then(ok),

  // ---- risk commands ----
  riskCommands: () => http.get<any, Envelope<RiskCommandView[]>>('/risk-commands').then(ok),
  upsertRiskCommand: (command: string, tiers: Record<string, string>) =>
    http.post<any, Envelope<RiskCommandView[]>>('/risk-commands', { command, tiers }).then(ok),
  patchRiskCommand: (name: string, tier: string, level: string) =>
    http.patch<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`, { tier, level }).then(ok),
  deleteRiskCommand: (name: string) =>
    http.delete<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`).then(ok),

  // ---- 数据库规范审查 (SQL review) ----
  // The rule list and the check are open to terminal operators: self-checking a
  // change before submitting it is the point. Editing the library is admin-only
  // on the server, so the console hides the controls rather than guessing.
  reviewRules: () => http.get<any, Envelope<ReviewRule[]>>('/sql-review/rules').then(ok),
  reviewCatalog: () => http.get<any, Envelope<ReviewCatalog>>('/sql-review/catalog').then(ok),
  saveReviewRule: (id: number, body: Partial<ReviewRule>) =>
    id > 0
      ? http.put<any, Envelope<ReviewRule>>(`/sql-review/rules/${id}`, body)
      : http.post<any, Envelope<ReviewRule>>('/sql-review/rules', body),
  deleteReviewRule: (id: number) => http.delete<any, Envelope<any>>(`/sql-review/rules/${id}`),
  // connectionId wins over dialect when both are sent: the target instance
  // decides which rules can possibly apply.
  reviewCheck: (body: { connectionId?: number; dialect?: string; sql: string }) =>
    http.post<any, Envelope<ReviewCheckResp>>('/sql-review/check', body, { timeout: SCRIPT_TIMEOUT_MS }).then(ok),

  // ---- 发布流程 (CI/CD) ----
  pipelines: () => http.get<any, Envelope<Pipeline[]>>('/pipelines').then(ok),
  savePipeline: (id: number, body: Partial<Pipeline>) =>
    id > 0
      ? http.put<any, Envelope<Pipeline>>(`/pipelines/${id}`, body)
      : http.post<any, Envelope<Pipeline>>('/pipelines', body),
  deletePipeline: (id: number) => http.delete<any, Envelope<any>>(`/pipelines/${id}`),
  releases: (scope: 'mine' | 'all' = 'mine', status = '', page = 1, pageSize = 20, projectId = 0) =>
    http.get<any, Envelope<ReleasePage>>(
      `/releases?scope=${scope}&status=${encodeURIComponent(status)}&page=${page}&pageSize=${pageSize}&projectId=${projectId}`,
    ).then(ok),

  // ---- 项目(数据库与升级单的归属) ----
  // 敏感字段:读开放给能进终端的人 —— 被脱敏的列是一串星号,"为什么看不到"
  // 要有个自己查得到的答案。增删改仅管理员(服务端也这么判)。
  sensitiveColumns: () => http.get<any, Envelope<SensitiveColumn[]>>('/sensitive-columns').then(ok),
  saveSensitiveColumn: (id: number, body: Partial<SensitiveColumn>) =>
    id
      ? http.put<any, Envelope<SensitiveColumn>>(`/sensitive-columns/${id}`, body)
      : http.post<any, Envelope<SensitiveColumn>>('/sensitive-columns', body),
  deleteSensitiveColumn: (id: number) => http.delete<any, Envelope<any>>(`/sensitive-columns/${id}`),
  projects: () => http.get<any, Envelope<Project[]>>('/projects').then(ok),
  createProject: (body: { name: string; owner?: string; description?: string }) =>
    http.post<any, Envelope<Project>>('/projects', body),
  updateProject: (id: number, body: Partial<{ name: string; owner: string; description: string }>) =>
    http.patch<any, Envelope<Project>>(`/projects/${id}`, body),
  deleteProject: (id: number) => http.delete<any, Envelope<any>>(`/projects/${id}`),
  /** 把实例下的一个库归到项目(projectId 0 = 取消归属)。归属的单位是库,不是实例。 */
  setDatabaseProject: (id: number, database: string, projectId: number) =>
    http.put<any, Envelope<any>>(`/connections/${id}/database-project`, { database, projectId }),
  release: (id: number) => http.get<any, Envelope<Release>>(`/releases/${id}`).then(ok),
  // raw envelope so the caller can detect 42800 (MFA step-up) and 40300 (denied
  // by the capability matrix), which mean different things to the operator.
  createRelease: (body: {
    title: string; pipelineId: number; connectionId: number; database?: string
    sql?: string; changeType?: string; scriptUploadId?: number; reason?: string; mfaCode?: string
  }) => http.post<any, Envelope<Release>>('/releases', body),
  abortRelease: (id: number) => http.post<any, Envelope<any>>(`/releases/${id}/abort`),
  continueStage: (releaseId: number, stageId: number) =>
    http.post<any, Envelope<any>>(`/releases/${releaseId}/stages/${stageId}/continue`),

  // ---- 开放接口凭据 (external API clients) ----
  // The create call returns the ONLY plaintext copy of the secret; there is no
  // "fetch it again" endpoint because the server keeps a bcrypt hash.
  // 服务账号:凭据背后的机器主体。创建是 admin 行为;停用/角色走常规用户管理。
  serviceAccounts: () => http.get<any, Envelope<ServiceAccount[]>>('/service-accounts').then(ok),
  createServiceAccount: (body: { name: string; roleIds: number[]; tags?: string[]; dept?: string }) =>
    http.post<any, Envelope<UserView>>('/service-accounts', body),
  apiClients: () => http.get<any, Envelope<APIClient[]>>('/api-clients').then(ok),
  createApiClient: (body: { name: string; userId: number; allowIps?: string; scopes?: string[]; pipelineId?: number; enabled?: boolean }) =>
    http.post<any, Envelope<{ client: APIClient; token: string }>>('/api-clients', body),
  updateApiClient: (id: number, body: Partial<{ name: string; userId: number; allowIps: string; scopes: string[]; pipelineId: number; enabled: boolean }>) =>
    http.put<any, Envelope<APIClient>>(`/api-clients/${id}`, body),
  deleteApiClient: (id: number) => http.delete<any, Envelope<any>>(`/api-clients/${id}`),

  // ---- approvals ----
  // Paged listing {items,total,page,pageSize}.
  // status 按状态筛,空串是不筛。总览的"待执行"必须靠它:通过了的工单不会过期,
  // 一张等着执行的单子可以停很久,早就被新工单挤出了任何一页 —— 取一页回来自己筛
  // 会漏报,而那张卡片漏报等于没有。
  // q 是控制台的搜索框(单号/实例/库/命令/发起人),和 status 一样在**服务端**筛:
  // 列表是分页的,只搜当前页的搜索框会对一张躺在第三页的工单回答"没有"。
  approvals: (scope: 'mine' | 'all', page = 1, pageSize = 50, status = '', q = '') =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number; pending: number }>>(
        `/approvals?scope=${scope}&page=${page}&pageSize=${pageSize}`
        + `&status=${encodeURIComponent(status)}&q=${encodeURIComponent(q)}`,
      )
      .then(ok),
  // Look one ticket up by number, independent of paging — the audit log links
  // tickets that may sit on any page.
  approvalByNo: (apNo: string) =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number }>>(`/approvals?ap=${encodeURIComponent(apNo)}`)
      .then(ok)
      .then((r) => r.items[0] || null),
  // 返回信封而不是 .then(ok):调用方要拿 msg 原样显示给人看 —— 服务端拒绝的
  // 理由(不能自审 / 不在审批链 / 已被处理)各自要人做的事完全不同。
  approve: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/approve`).then(ok),
  reject: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/reject`).then(ok),
  // 执行是发起人的动作,不是审批的副作用 —— 所以它是独立的一个调用。
  executeApproval: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/execute`).then(ok),

  // ---- audit ----
  audit: (q: AuditQuery) => http.get<any, Envelope<AuditPage>>(`/audit?${auditQS(q)}`).then(ok),
  auditExportUrl: (q: AuditQuery) => `${import.meta.env.VITE_API_BASE || '/api/v1'}/audit/export?${auditQS(q)}`,

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
