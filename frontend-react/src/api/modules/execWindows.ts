import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { ExecWindow } from '@/types'

export const execWindowsApi = {
  list: () => http.get<unknown, Envelope<ExecWindow[]>>('/exec-windows').then(ok),
  create: (body: Partial<ExecWindow>) =>
    http.post<unknown, Envelope<ExecWindow>>('/exec-windows', body).then(ok),
  update: (id: number, body: Partial<ExecWindow>) =>
    http.put<unknown, Envelope<ExecWindow>>(`/exec-windows/${id}`, body).then(ok),
  remove: (id: number) =>
    http.delete<unknown, Envelope<unknown>>(`/exec-windows/${id}`).then(ok),
}

export const execWindowsQueryOptions = () =>
  queryOptions({
    queryKey: ['exec-windows'] as const,
    queryFn: execWindowsApi.list,
  })
