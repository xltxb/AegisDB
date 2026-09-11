import http, { ok, type Envelope } from './http'
import type { AuditQuery } from '@/types'

// auditQS builds the audit query string, omitting empty filters. An absolute
// from/to window takes precedence over the relative range on the backend.
export function auditQS(q: AuditQuery): string {
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
export const SCRIPT_TIMEOUT_MS = 120000

export { http, ok }
export type { Envelope }
