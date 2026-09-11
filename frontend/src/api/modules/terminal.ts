import { http, ok, type Envelope } from '@/api/shared'
import { SCRIPT_TIMEOUT_MS } from '@/api/shared'
import type {
  AsyncJob, ExecResp, ExportJob, RiskCheckResp, ScriptScanResp, ScriptUpload, SnippetLimits, TerminalSnippet,
} from '@/types'

export const terminalApi = {
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
}

// ---- TanStack Query 绑定 ----
// 三个前台页面(后台执行 / 导出 / 脚本库)共用这一组 key。列表与详情分开成两个
// key,是因为它们的轮询节奏不一样:列表一直在页面上,详情只在人点开某一条时才有。
import { queryOptions } from '@tanstack/react-query'

/** 终态的任务不会再变,`refetchInterval` 据此收手。 */
const ASYNC_ACTIVE = new Set(['pending', 'running'])
export const isAsyncJobActive = (j: AsyncJob) => ASYNC_ACTIVE.has(j.status)

/** 导出任务里 `awaiting` 等的是人,不是机器 —— 它不该让页面一直轮询。 */
const EXPORT_ACTIVE = new Set(['pending', 'running'])
export const isExportJobActive = (j: ExportJob) => EXPORT_ACTIVE.has(j.status)

export const asyncJobsQueryOptions = () =>
  queryOptions({
    queryKey: ['async-jobs'] as const,
    queryFn: terminalApi.asyncJobs,
    // 只要还有没跑完的任务就 2s 拉一次;全部终态后返回 false,轮询自己停下 ——
    // 一张全是历史记录的列表不该每两秒打一次网关。
    refetchInterval: (q) => ((q.state.data ?? []).some(isAsyncJobActive) ? 2000 : false),
  })

export const asyncJobQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['async-job', id] as const,
    queryFn: () => terminalApi.asyncJob(id),
    enabled: id > 0,
    // 日志是一行行追加的,所以详情比列表更需要跟着跑;同样在终态停。
    refetchInterval: (q) => (q.state.data && isAsyncJobActive(q.state.data) ? 2000 : false),
  })

export const exportJobsQueryOptions = () =>
  queryOptions({
    queryKey: ['export-jobs'] as const,
    queryFn: terminalApi.exportJobs,
    refetchInterval: (q) => ((q.state.data ?? []).some(isExportJobActive) ? 2000 : false),
  })

/** 归档保留天数由服务端设置决定,不给默认值 —— 取不到就不提保留期,别编一个。 */
export const exportConfigQueryOptions = () =>
  queryOptions({
    queryKey: ['export-config'] as const,
    queryFn: terminalApi.exportConfig,
    staleTime: 5 * 60_000,
    retry: false,
  })

/**
 * 本人存下的快捷脚本,以及它们的上限。
 *
 * 两个 key 分开:清单随增删改变,上限是服务端的配置 —— 一天里不会变第二次,所以
 * 给它一个长 `staleTime`,而不是每次打开管理弹窗都再问一遍同一个数字。
 */
export const snippetsQueryOptions = () =>
  queryOptions({
    queryKey: ['snippets'] as const,
    queryFn: terminalApi.snippets,
  })

export const snippetLimitsQueryOptions = () =>
  queryOptions({
    queryKey: ['snippet-limits'] as const,
    queryFn: terminalApi.snippetLimits,
    staleTime: 10 * 60_000,
  })

export const scriptUploadsQueryOptions = () =>
  queryOptions({
    queryKey: ['script-uploads'] as const,
    queryFn: terminalApi.scriptUploads,
  })

export const scriptUploadContentQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['script-upload-content', id] as const,
    queryFn: () => terminalApi.scriptUploadContent(id),
    enabled: id > 0,
    staleTime: Infinity,
  })

/**
 * 扫描一份**已经上传**的脚本。
 *
 * `content` 传空、只给 uploadId:文件在服务器上,网关自己读它。把正文一起发回去
 * 就多出一份可能和它对不上的副本(Vue 版终端里的同一条约定)。
 *
 * `staleTime: Infinity` —— 上传的文件不会自己变,同一份扫两次只是白等十几秒。
 */
export const scriptScanQueryOptions = (id: number, filename: string) =>
  queryOptions({
    queryKey: ['script-scan', id] as const,
    queryFn: () => terminalApi.scriptScan('', filename, 0, id),
    enabled: id > 0,
    staleTime: Infinity,
    retry: false,
  })
