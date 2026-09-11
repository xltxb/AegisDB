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
