import { http, ok, type Envelope } from '../shared'
import { auditQS } from '../shared'
import type {
  AuditPage, AuditQuery,
} from '@/types'

export const auditApi = {
  // ---- audit ----
  audit: (q: AuditQuery) => http.get<any, Envelope<AuditPage>>(`/audit?${auditQS(q)}`).then(ok),
  auditExportUrl: (q: AuditQuery) => `${import.meta.env.VITE_API_BASE || '/api/v1'}/audit/export?${auditQS(q)}`,
}
