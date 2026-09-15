import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { AuditRow, ExecWindow } from '@/types'

export const execWindowsApi = {
  list: () => http.get<unknown, Envelope<ExecWindow[]>>('/exec-windows').then(ok),
  create: (body: Partial<ExecWindow>) =>
    http.post<unknown, Envelope<ExecWindow>>('/exec-windows', body).then(ok),
  update: (id: number, body: Partial<ExecWindow>) =>
    http.put<unknown, Envelope<ExecWindow>>(`/exec-windows/${id}`, body).then(ok),
  remove: (id: number) =>
    http.delete<unknown, Envelope<unknown>>(`/exec-windows/${id}`).then(ok),
  // 这扇门开着的时候放行了什么。权限与审计列表同一把尺子 —— 后端判,
  // 没有审计权限的人拿到 403 而不是空列表(空列表会被读成"什么也没放行过")。
  audit: (id: number) =>
    http.get<unknown, Envelope<AuditRow[]>>(`/exec-windows/${id}/audit`).then(ok),
}

export const execWindowsQueryOptions = () =>
  queryOptions({
    queryKey: ['exec-windows'] as const,
    queryFn: execWindowsApi.list,
  })

// 放行记录只在展开某扇窗口时才拉 —— 列表页有几十扇门,一次性全拉是几十次全表扫。
export const execWindowAuditQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['exec-windows', id, 'audit'] as const,
    queryFn: () => execWindowsApi.audit(id),
    enabled: id > 0,
  })
