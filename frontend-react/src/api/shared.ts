import http, { ok, type Envelope } from '@/api/http'
import type { AuditQuery } from '@/types'

// auditQS 拼审计查询串,空过滤项不带上。绝对起止时间优先于相对范围。
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

// 脚本扫描/执行的上限由服务端定(32MB),这个只是浏览器愿意等多久。
export const SCRIPT_TIMEOUT_MS = 120000

export { http, ok }
export type { Envelope }
