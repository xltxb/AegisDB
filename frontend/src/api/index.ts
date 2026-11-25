import http, { ok, type Envelope } from './http'
import { sessionHeaders, clearSessionMarker } from '@/lib/session'
import { filenameFromDisposition } from '@/lib/download'
import type { Branding } from '@/lib/brand'
import { bizClientTimeoutMs } from '@/lib/bizResultView'
import type {
  APIClient, Approval, AsyncJob, AuditPage, AuditQuery, AuditRow, Connection, ConnectionSchema, DbObjects, EnvTier, Environment, ExecResp, ExecWindow,
  ExportJob, SchemaDiffConn, SchemaDiffOptions, SchemaDiffResp, QueryGrant, BizQueryResult, BizQueryLimits, InvalidObject, LoginResp, SsoConfig, Me, Member, MetaSync, MetaSyncStatus, MetaTable, Notification, ObjectSource, Pipeline, Project, RecompileReport, Release, ReleasePage, ReviewCatalog,
  SensitiveColumn, ServiceAccount,
  ReviewCheckResp, ReviewRule, RiskCheckResp, RuleRef, RiskCommandView, RoleBrief, RoleDetail,
  ScriptProgress, ScriptScanResp, ScriptUpload, SettingsResp, SnippetLimits, TerminalSnippet, UserView, WebhookConfig, WebhookDelivery, OnlineSession, OSCJobView,
  ApprovalTodo, WindowStatements, AuditChainReport, InspectionLatestRow, InspectionReport, ChatBinding, ChatBindCode, ChatStatus,
} from '@/types'

// auditQS builds the audit query string, omitting empty filters. An absolute
// from/to window takes precedence over the relative range on the backend.
function auditQS(q: AuditQuery): string {
  const p = new URLSearchParams()
  p.set('risk', q.risk || 'all')
  // 事件类型和 risk 一样在**服务端**筛:列表是分页的,只筛当前页会对一条躺在第三页的
  // 记录回答"没有"。空串不发,免得和 'all' 两种写法都要在后端认。
  // 多选发成逗号分隔的一串(服务端 auditMultiQuery 逐个过白名单)。空数组不发 ——
  // 发一个空串等于让后端再认一种"不筛"的写法。
  if (q.event?.length) p.set('event', q.event.join(','))
  if (q.action?.length) p.set('action', q.action.join(','))
  if (q.source?.length) p.set('source', q.source.join(','))
  if (q.instance) p.set('instance', q.instance)
  if (q.q) p.set('q', q.q)
  if (q.from) p.set('from', q.from)
  if (q.to) p.set('to', q.to)
  if (!q.from && q.range) p.set('range', q.range)
  if (q.group) p.set('group', q.group)
  if (q.mine) p.set('mine', '1')
  if (q.full) p.set('full', '1')
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
  ssoConfig: () => http.get<any, Envelope<SsoConfig>>('/auth/sso/config').then(ok),
  // 系统名称与 LOGO(公开,登录页也要)。改走 saveSettings 的 brand.* 三项。
  branding: () => http.get<any, Envelope<Branding>>('/auth/branding').then(ok),
  // token 不经过前端:SSO 的 Cookie 由浏览器随同源请求带上,服务端直接读。
  // mfaCode:已绑定 TOTP 的管理员第二步带上(服务端答 42800 时)
  ssoLogin: (mfaCode = '') => http.post<any, Envelope<LoginResp>>('/auth/sso', mfaCode ? { mfaCode } : {}).then(ok),
  me: () => http.get<any, Envelope<Me>>('/auth/me').then(ok),
  logout: () => http.post('/auth/logout', null),
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
  // sessionId:终端标签页的会话标识,同一标签的语句落在网关为它独占的一条连接上(lib/termSession.ts)
  exec: (connectionId: number, sql: string, reason = '', mfaCode = '', database = '', serverOutput = false, sessionId = '', binds: { name: string; type: string; value: string; null: boolean }[] = []) =>
    http.post<any, Envelope<ExecResp>>('/terminal/exec', { connectionId, sql, reason, mfaCode, database, serverOutput, sessionId, binds }),
  // ---- async (background) long-running SQL exec ----
  // 加载更多:取暂存结果的下一批(offset = 整个结果里的行位置),不会重新执行 SQL
  terminalResultPage: (id: string, offset: number) =>
    http.get<any, Envelope<{ columns: string[]; data: string[][]; offset: number; more: boolean }>>(`/terminal/results/${encodeURIComponent(id)}?offset=${offset}`).then(ok),
  execAsync: (connectionId: number, sql: string, database = '', reason = '') =>
    http.post<any, Envelope<{ jobId?: number; intercepted?: boolean; approvalNo?: string; risk?: string; rule?: string; output?: string; outputRef?: RuleRef }>>('/terminal/exec-async', { connectionId, sql, database, reason }),
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
  // format: csv(默认)| sql。targetTable 留空 = 服务端从查询的 FROM 识别
  exportData: (connectionId: number, sql: string, name = '', database = '', includeSensitive = false, format: 'csv' | 'sql' = 'csv', targetTable = '') =>
    http.post<any, Envelope<ExportJob>>('/export', { connectionId, sql, name, database, includeSensitive, format, targetTable }),
  exportJobs: () => http.get<any, Envelope<ExportJob[]>>('/export/jobs').then(ok),
  // 终止排队中 / 待审批 / 执行中的导出;执行中的同时终止目标库上的查询。已结束的回业务错误。
  cancelExport: (id: number) => http.post<any, Envelope<ExportJob>>(`/export/jobs/${id}/cancel`).then(ok),
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
  // database:扫描与执行要按同一个库判(执行窗口按库开)
  // progressId:带上时服务端逐条记扫描进度(scriptProgress 轮询);onUpload:请求体上传的百分比;signal:取消
  scriptScan: (content: string, filename: string, connectionId = 0, uploadId = 0, database = '',
    progressId = '', onUpload?: (pct: number) => void, signal?: AbortSignal) =>
    http.post<any, Envelope<ScriptScanResp>>('/scripts/scan', { content, filename, connectionId, uploadId, database, progressId },
      {
        timeout: SCRIPT_TIMEOUT_MS, signal,
        onUploadProgress: onUpload ? (e) => onUpload(Math.min(100, Math.round((e.loaded * 100) / (e.total || e.loaded || 1)))) : undefined,
      }).then(ok),
  scriptExecute: (content: string, filename: string, connectionId: number, mfaCode = '', uploadId = 0, database = '', sessionId = '', progressId = '') =>
    http.post<any, Envelope<any>>('/scripts/execute', { content, filename, connectionId, mfaCode, uploadId, database, sessionId, progressId },
      { timeout: SCRIPT_TIMEOUT_MS }),
  /** 扫描 / 执行进行中的进度;只能查自己的,查不到(40400)会抛错 */
  scriptProgress: (id: string) => http.get<any, Envelope<ScriptProgress>>('/scripts/progress/' + encodeURIComponent(id)).then(ok),
  // ---- 执行窗口(「班车」) ----
  // 默认只列自己申请的;all = 管理后台的全局列表(只给平台管理员,带申请人姓名)
  execWindows: (all = false) => http.get<any, Envelope<ExecWindow[]>>(`/exec-windows${all ? '?scope=all' : ''}`).then(ok),
  // 用户后台班车页「全部窗口」页签:有回看权限的人看得到的窗口(含已过期,只读;2026-10-26 回归排查 08)
  execWindowsReviewable: () => http.get<any, Envelope<ExecWindow[]>>('/exec-windows?scope=reviewable').then(ok),
  /** 此刻开着的窗口,所有人的(带申请人姓名):总览的生效面板用 */
  activeExecWindows: () => http.get<any, Envelope<ExecWindow[]>>('/exec-windows?scope=active').then(ok),
  /** 已批准、还没到点的窗口,所有人的(带申请人姓名):总览的排期用 */
  scheduledExecWindows: () => http.get<any, Envelope<ExecWindow[]>>('/exec-windows?scope=scheduled').then(ok),
  // 停用:立即不再放行,不走审批(申请人或平台管理员)
  disableExecWindow: (id: number) => http.post<any, Envelope<ExecWindow>>(`/exec-windows/${id}/disable`),
  createExecWindow: (body: Partial<ExecWindow>) =>
    http.post<any, Envelope<ExecWindow>>('/exec-windows', body),
  updateExecWindow: (id: number, body: Partial<ExecWindow>) =>
    http.put<any, Envelope<ExecWindow>>(`/exec-windows/${id}`, body),
  deleteExecWindow: (id: number) => http.delete<any, Envelope<any>>(`/exec-windows/${id}`),
  // 回看这扇门开着的那几个小时里跑过什么 —— 所有人跑的,不只是自己的(后端不按人
  // 收窄,理由见 service.WindowStatements)。前端不另做过滤。
  execWindowStatements: (id: number) =>
    http.get<any, Envelope<WindowStatements>>(`/exec-windows/${id}/statements`).then(ok),

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
  // password:有本地口令的账号要出示当前登录密码(服务端答 42801 时)
  mfaSetup: (password = '') =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>('/auth/mfa/setup', password ? { password } : {}).then(ok),
  mfaEnable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/enable', { code }).then(ok),
  mfaDisable: (code: string) => http.post<any, Envelope<any>>('/auth/mfa/disable', { code }).then(ok),
  // ---- 审批机器人(聊天审批卡片)----
  /** 本人在各渠道上的绑定;只列开着的渠道,一个都没开是空数组 */
  chatBindings: () => http.get<any, Envelope<ChatBinding[]>>('/auth/chat-bindings').then(ok),
  /** 生成一次性绑定码;启用了 MFA 的账号要带验证码,没带回 42800 */
  chatBindCode: (channel: string, mfaCode = '') =>
    http.post<any, Envelope<ChatBindCode>>(`/auth/chat-bindings/${channel}/code`, mfaCode ? { mfaCode } : {}).then(ok),
  chatUnbind: (channel: string) => http.delete<any, Envelope<any>>(`/auth/chat-bindings/${channel}`).then(ok),
  // 自助修改密码:成功后其它会话作废,返回当前会话重签的 token(调用方换上)
  changePassword: (oldPassword: string, newPassword: string) =>
    http.post<any, Envelope<{ session: string; expiresAt: string }>>('/auth/password', { oldPassword, newPassword }).then(ok),

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
  // 业务方数据查询:我的授权 + 执行(判定拒绝回 40300,执行失败回 0 + data.error)
  myQueryGrants: () => http.get<any, Envelope<{ grants: QueryGrant[]; limits: BizQueryLimits }>>('/bizquery/grants').then(ok),
  // ---- AI 巡检报告(.scratch/inspection-report)----
  inspectionLatest: () => http.get<any, Envelope<{ today: string; items: InspectionLatestRow[] }>>('/inspection-reports/latest').then(ok),
  inspectionHistory: (instanceId: number, page = 1, pageSize = 30) =>
    http.get<any, Envelope<{ items: InspectionReport[]; total: number }>>('/inspection-reports', { params: { instanceId, page, pageSize } }).then(ok),
  inspectionReport: (id: number) => http.get<any, Envelope<InspectionReport>>(`/inspection-reports/${id}`).then(ok),
  // 完整 HTML:成功时是纯文本(不是 JSON 信封),出错时才是信封 —— 取回后先认一下是不是错误信封。
  // 巡检页 iframe 的地址:会话 Cookie 由浏览器带上,响应自带沙箱 CSP(见 handler.InspectionReportView)。
  inspectionViewUrl: (id: number) => `${import.meta.env.VITE_API_BASE || '/api/v1'}/inspection-reports/${id}/view`,
  inspectionHtml: async (id: number): Promise<string> => {
    const text = await http.get<any, string>(`/inspection-reports/${id}/html`, { responseType: 'text', transformResponse: [(d) => d] })
    if (typeof text === 'string' && text.startsWith('{"code":')) {
      try { const env = JSON.parse(text); if (typeof env.code === 'number') return ok(env) as unknown as string } catch { /* 不是信封,就是报告本身 */ }
    }
    return text
  },
  // 跨环境表结构对比。读失败不是拒绝:回 0 + data.error(见 .scratch/schema-diff)。
  schemaDiffConnections: () => http.get<any, Envelope<SchemaDiffConn[]>>('/schema-diff/connections').then(ok),
  // 一个实例上可以比的库(库名下拉);读不到库列表回 0 + error,页面退回手填。
  schemaDiffDatabases: (id: number) =>
    http.get<any, Envelope<{ databases: string[]; error?: string }>>(`/schema-diff/connections/${id}/databases`).then(ok),
  // PG / DWS 一个库里可以比的 schema(schema 下拉);非 PG 回空列表。
  schemaDiffSchemas: (id: number, database: string) =>
    http.get<any, Envelope<{ schemas: string[]; error?: string }>>(`/schema-diff/connections/${id}/schemas`, { params: { database } }).then(ok),
  schemaDiff: (body: { left: { connectionId: number; database: string; schema?: string }; right: { connectionId: number; database: string; schema?: string }; options: SchemaDiffOptions }) =>
    http.post<any, Envelope<SchemaDiffResp>>('/schema-diff', body, { timeout: 150000 }).then(ok),
  // 授权库里的表(「表」下拉)。判定拒绝回 40300 + data.reasonRef;读不到目录回 0 + data.error。
  bizTables: (sourceId: number, database: string) =>
    http.get<any, Envelope<{ tables: string[]; truncated: boolean; error?: string; quote?: string }>>('/bizquery/tables', { params: { sourceId, database } }),
  // 授权库里一张表的列(数据查询页的自动补全)。判定同上;读不到回 0 + data.error
  bizColumns: (sourceId: number, database: string, table: string) =>
    http.get<any, Envelope<{ columns: string[]; error?: string }>>('/bizquery/columns', { params: { sourceId, database, table } }),
  // 浏览器等多久跟着这条授权的时限走(lib/bizResultView 的 bizClientTimeoutMs):原先写死 330 秒,
  // 管理员把超时上限调过 330 秒后,查询还在库上跑、浏览器先断了,用户只看到网络错误。
  bizQueryExec: (body: { sourceId: number; database: string; sql: string }, timeoutSec?: number) =>
    http.post<any, Envelope<BizQueryResult>>('/bizquery/exec', body, { timeout: bizClientTimeoutMs(timeoutSec) }),
  // 业务方导出:成功是 CSV 本体,失败是 JSON 信封(按 Content-Type 区分;axios 拦截器拿不到响应头,用 fetch)。
  // 信封有两种:判定拒绝 / 限流是非 0 的 code(ok() 抛出 msg);查询本身失败与 bizQueryExec 一样是
  // code 0 + data.error —— 也要抛出来,不能当成文件存下。
  bizQueryExport: async (body: { sourceId: number; database: string; sql: string }) => {
    const res = await fetch(`${import.meta.env.VITE_API_BASE || '/api/v1'}/bizquery/export`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...sessionHeaders() },
      body: JSON.stringify(body),
    })
    if (res.status === 401) {
      // 与 http 拦截器同一个处理:会话失效就整份清掉再回登录页。
      import('@/stores/auth').then((m) => m.useAuthStore.getState().clearSession()).catch(() => clearSessionMarker())
      if (location.hash !== '#/login') location.hash = '#/login'
      throw new Error('HTTP 401')
    }
    if ((res.headers.get('Content-Type') || '').includes('application/json')) {
      const data = ok<{ error?: string; errorRef?: RuleRef } | null>(await res.json())
      throw Object.assign(new Error(data?.error || `HTTP ${res.status}`), { data: { reasonRef: data?.errorRef } })
    }
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return { blob: await res.blob(), filename: filenameFromDisposition(res.headers.get('Content-Disposition'), `${body.database}.csv`) }
  },
  // 查询授权总表(控制台)与管理员撤销
  queryGrants: (q: Record<string, string | number | undefined> = {}) => {
    const qs = Object.entries(q).filter(([, v]) => v !== undefined && v !== '').map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join('&')
    return http.get<any, Envelope<{ items: QueryGrant[]; limits: BizQueryLimits }>>(`/query-grants${qs ? '?' + qs : ''}`).then(ok)
  },
  revokeQueryGrant: (id: number, reason: string) => http.post<any, Envelope<QueryGrant>>(`/query-grants/${id}/revoke`, { reason }).then(ok),
  // 业务查询源标记(查询授权只能引用打了这个标记的实例)
  setConnectionBizQuery: (id: number, bizQuery: boolean) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { bizQuery }).then(ok),
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
  // mfaCode:生产实例上编译要二次验证(编译会把库上对象改成 VALID/INVALID,与执行
  // 一条 DDL 同级)。后端答 42800 时由调用方亮出输码框重试 —— 与 exec 同一个路子。
  compileObject: (id: number, body: { scope: string; type: string; name: string; database?: string; mfaCode?: string }) =>
    http.post<any, Envelope<{ ok: boolean; report: any }>>(`/connections/${id}/objects/compile`, body),
  // Oracle 无效对象:清点是只读,批量重编译是一批 DDL(整批一起判定,判不过整批不执行)。
  invalidObjects: (id: number, scope = '', database = '') =>
    http.get<any, Envelope<{ items: InvalidObject[]; total: number }>>(
      `/connections/${id}/objects/invalid?scope=${encodeURIComponent(scope)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  recompileInvalid: (id: number, body: { scope: string; database?: string; names?: string[]; mfaCode?: string }) =>
    http.post<any, Envelope<{ ok: boolean; report: RecompileReport }>>(`/connections/${id}/objects/recompile`, body),
  // Oracle 程序包下载:一个包是 <包名>.pck,多个是一个 zip。成功时响应体是文件本身,
  // 失败时仍是 JSON 信封 —— 按 Content-Type 区分。文件名以服务端的 Content-Disposition 为准。
  // 用 fetch 而不是 http:axios 的拦截器只交回 data,拿不到响应头。
  downloadPackages: async (id: number, body: { scope: string; database?: string; names: string[] }) => {
    const res = await fetch(`${import.meta.env.VITE_API_BASE || '/api/v1'}/connections/${id}/packages/download`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...sessionHeaders() },
      body: JSON.stringify(body),
    })
    if (res.status === 401) {
      // 与 http 拦截器同一个处理:会话失效就整份清掉再回登录页。
      import('@/stores/auth').then((m) => m.useAuthStore.getState().clearSession()).catch(() => clearSessionMarker())
      if (location.hash !== '#/login') location.hash = '#/login'
      throw new Error('HTTP 401')
    }
    if ((res.headers.get('Content-Type') || '').includes('application/json')) {
      ok(await res.json()) // 业务失败:抛出服务端的原话
    }
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const fallback = body.names.length === 1 ? `${body.names[0]}.pck` : `${body.scope}_packages.zip`
    return { blob: await res.blob(), filename: filenameFromDisposition(res.headers.get('Content-Disposition'), fallback) }
  },
  objectSource: (id: number, scope: string, type: string, name: string, database = '') =>
    http.get<any, Envelope<ObjectSource>>(
      `/connections/${id}/object-source?scope=${encodeURIComponent(scope)}&type=${encodeURIComponent(type)}&name=${encodeURIComponent(name)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),

  // ---- 元数据缓存 ----
  // 立刻同步一台实例。它**真的会登录那台库**,所以是一次明确的动作(管理员),
  // 而不是定时任务的一部分 —— 定时开关关着的时候这个接口照样可用。
  syncConnectionMetadata: (id: number) =>
    http.post<any, Envelope<{ tables: number; sync: MetaSync | null }>>(
      `/connections/${id}/metadata/sync`,
    ).then(ok),
  // 后台版:立刻返回 running,再用 metadataSyncStatus 轮询到 running=false。
  // 大实例一次同步十几秒,不该让人对着转圈等一个 HTTP 请求。
  startConnectionMetadataSync: (id: number) =>
    http.post<any, Envelope<MetaSyncStatus>>(`/connections/${id}/metadata/sync?async=1`).then(ok),
  metadataSyncStatus: (id: number) =>
    http.get<any, Envelope<MetaSyncStatus>>(`/connections/${id}/metadata/sync`).then(ok),
  // sync 为 null 表示这台实例从没同步过 —— 和"同步过但一张表都没有"不是一回事。
  // 一张表的列(终端 Tab 补全):实时查,按建表顺序、原始大小写。table 可带限定名。
  tableColumns: (id: number, database: string, table: string) =>
    http.get<any, Envelope<{ columns: string[] }>>(
      `/connections/${id}/columns?database=${encodeURIComponent(database)}&table=${encodeURIComponent(table)}`,
    ).then(ok).then((r) => r.columns || []),
  connectionMetadata: (id: number, database = '') =>
    http.get<any, Envelope<{ tables: MetaTable[]; sync: MetaSync | null }>>(
      `/connections/${id}/metadata${database ? `?database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),

  // ---- roles ----
  roles: () => http.get<any, Envelope<RoleBrief[]>>('/roles').then(ok),
  role: (id: number) => http.get<any, Envelope<RoleDetail>>(`/roles/${id}`).then(ok),
  // 新建角色:服务端生成 code,矩阵全 deny、不带菜单(见 service.CreateRole)
  createRole: (body: { name: string; layer?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }) =>
    http.post<any, Envelope<RoleDetail>>('/roles', body).then(ok),
  // 删除自定义角色:有成员、被流程引用、内置角色都会被拒,原因在 msg 里
  deleteRole: (id: number) => http.delete<any, Envelope<{ ok: boolean }>>(`/roles/${id}`).then(ok),
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
  // 读 SSO 名单用的是调用者自己的 SSO 登录态(浏览器随请求带上的父域 Cookie)
  ssoUnbind: (id: number) => http.post<any, Envelope<any>>(`/users/${id}/sso-unbind`, {}).then(ok),
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
  chatUnbindUser: (id: number) => http.delete<any, Envelope<any>>(`/users/${id}/chat-bindings`).then(ok),
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
  releases: (scope: 'mine' | 'all' = 'mine', status = '', page = 1, pageSize = 20, projectId = 0, q = '', env = '') =>
    http.get<any, Envelope<ReleasePage>>(
      `/releases?scope=${scope}&status=${encodeURIComponent(status)}&page=${page}&pageSize=${pageSize}&projectId=${projectId}`
        + `${q ? `&q=${encodeURIComponent(q)}` : ''}${env ? `&env=${encodeURIComponent(env)}` : ''}`,
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
  // type 按工单类型筛(cmd|release|window|export),和 status 一样在**服务端**筛:
  // 列表是分页的,只筛当前页会对一张躺在第三页的工单回答"没有"。
  // scope=chain:与本人相关的那一份(平台管理员的 all 是全量),总览的待办用它
  approvals: (scope: 'mine' | 'all' | 'chain', page = 1, pageSize = 50, status = '', q = '', type = '') =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number; pending: number }>>(
        `/approvals?scope=${scope}&page=${page}&pageSize=${pageSize}`
        + `&status=${encodeURIComponent(status)}&q=${encodeURIComponent(q)}`
        + `&type=${encodeURIComponent(type)}`,
      )
      .then(ok),
  // Look one ticket up by number, independent of paging — the audit log links
  // tickets that may sit on any page.
  approvalByNo: (apNo: string) =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number }>>(`/approvals?ap=${encodeURIComponent(apNo)}`)
      .then(ok)
      .then((r) => r.items[0] || null),
  // 右上角"待办":此刻在等我动手的工单。服务端逐张按 DecideBlockFor / CanExecuteApproved
  // 判过 —— 不是 pending(那个把我自己发起、等别人审的也算进去)。
  approvalTodo: () => http.get<any, Envelope<ApprovalTodo>>('/approvals/todo').then(ok),
  // 脚本工单所引用脚本的全文 —— 工单里只有摘要;可见范围与工单相同,文件被改过即拒绝
  approvalScript: (id: number) =>
    http.get<any, Envelope<{ filename: string; content: string }>>(`/approvals/${id}/script`).then(ok),
  // 返回信封而不是 .then(ok):调用方要拿 msg 原样显示给人看 —— 服务端拒绝的
  // 理由(不能自审 / 不在审批链 / 已被处理)各自要人做的事完全不同。
  approve: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/approve`).then(ok),
  reject: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/reject`).then(ok),
  // 撤回:发起人自己的动作,和 approve/reject 是两码事(见后端 approval_cancel.go)。
  // 走 ok():服务端的拒绝理由会被抛成 Error(msg),原样进 toast —— 而那三种理由
  // 要人去做的事完全不同。
  cancelApproval: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/cancel`).then(ok),
  // 执行是发起人的动作,不是审批的副作用 —— 所以它是独立的一个调用。
  // 执行要等命令在库上跑完才回,可能远超默认的 15 秒 —— 不设客户端超时。中途断了也不要紧:
  // 工单上的执行状态(running → success / failed)才是结论,页面按它轮询。
  executeApproval: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/execute`, undefined, { timeout: 0 }).then(ok),

  // ---- audit ----
  audit: (q: AuditQuery) => http.get<any, Envelope<AuditPage>>(`/audit?${auditQS(q)}`).then(ok),
  // 筛选下拉里的实例清单:审计里**真实出现过**的那些,不是连接表的全部 —— 一个筛出来
  // 永远是空的选项,读起来像"这台机器上什么都没发生过"。
  // mine:只要自己的;full:管理后台的全量口径(空闲 / 白名单不过就拒)。两者都不带:用户后台按这个人的
  // 可见范围给(能看全量的人看全员)—— 2026-10-26 回归排查 09。
  auditInstances: (mine = false, full = !mine) =>
    http.get<any, Envelope<string[]>>(`/audit/instances${mine ? '?mine=1' : full ? '?full=1' : ''}`).then(ok),
  // 一张审批单的全部操作记录,按时间先后(可见范围与列表相同)
  auditTrail: (apNo: string) => http.get<any, Envelope<AuditRow[]>>(`/audit/approval/${encodeURIComponent(apNo)}`).then(ok),
  // 重算哈希链,回答"这些行被改过吗"。界面这一下只验最近一段(整表校验走命令行的
  // `aegisdb verify-audit`)。限监督角色 —— 它扫的是全表。
  verifyAudit: () =>
    http.get<any, Envelope<{ report: AuditChainReport; reasonText: string; reasonRef?: RuleRef | null }>>('/audit/verify').then(ok),
  auditExportUrl: (q: AuditQuery) => `${import.meta.env.VITE_API_BASE || '/api/v1'}/audit/export?${auditQS(q)}`,

  // ---- settings ----
  // 会话策略(空闲锁定):所有登录用户可读;/settings 只给管理员
  sessionPolicy: () => http.get<any, Envelope<{ idleLock: boolean; idleMinutes: number; sessionTtlMinutes: number; adminIdleMinutes?: number }>>('/auth/session-policy').then(ok),
  // 管理后台空闲超时(工单 09):人主动进入时重新计时;人在管理后台有操作时报心跳
  adminEnter: () => http.post<any, Envelope<any>>('/admin/enter', null, { quietAdminGate: true }),
  adminHeartbeat: () => http.post<any, Envelope<any>>('/admin/heartbeat'),
  settings: () => http.get<any, Envelope<SettingsResp>>('/settings').then(ok),
  // 必须过 ok():服务端会拒绝(例如 SSO 配置不合法,回 40001),不认业务码的话,被拒的保存
  // 也会弹"设置已保存" —— 人以为 SSO 开了,其实一项都没存。
  saveSettings: (body: Record<string, any>) => http.put<any, Envelope<any>>('/settings', body).then(ok),
  // 在线变更任务的进度与控制(生产大表加 / 删索引)
  oscJob: (id: number) => http.get<any, Envelope<OSCJobView>>(`/osc/jobs/${id}`).then(ok),
  oscControl: (id: number, action: 'pause' | 'resume' | 'abort' | 'continue') =>
    http.post<any, Envelope<{ ok: boolean }>>(`/osc/jobs/${id}/${action}`).then(ok),
  // 此刻在线的登录会话(管理员)
  onlineUsers: () =>
    http.get<any, Envelope<{ sessions: OnlineSession[]; users: number; terminals: number; windowSec: number }>>('/online-users').then(ok),
  saveWebhook: (body: Partial<WebhookConfig>) =>
    http.put<any, Envelope<WebhookConfig>>('/settings/webhook', body).then(ok),
  testWebhook: () => http.post<any, Envelope<any>>('/settings/webhook/test').then(ok),
  chatStatus: () => http.get<any, Envelope<Record<string, ChatStatus>>>('/settings/chat-status').then(ok),
  chatTest: (channel: string) => http.post<any, Envelope<any>>('/settings/chat-test', { channel }).then(ok),
  testLark: () => http.post<any, Envelope<{ ok: boolean; message: string }>>('/settings/lark/test').then(ok),
  webhookDeliveries: (limit = 50) =>
    http.get<any, Envelope<WebhookDelivery[]>>(`/settings/webhook/deliveries?limit=${limit}`).then(ok),
}

export default api
