import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import { isLive, type OscJob, type OscStatus } from '@/lib/osc'

export interface OscStartBody {
  connectionId: number
  schema: string
  table: string
  alter: string
}

export const oscApi = {
  status: () => http.get<unknown, Envelope<OscStatus>>('/osc/status').then(ok),
  list: () => http.get<unknown, Envelope<OscJob[]>>('/osc/jobs').then(ok),
  start: (body: OscStartBody) => http.post<unknown, Envelope<OscJob>>('/osc/jobs', body).then(ok),
  abort: (id: number) => http.post<unknown, Envelope<unknown>>(`/osc/jobs/${id}/abort`, {}).then(ok),
}

// 状态几乎不变,但也不能永不过期:管理员改了配置以后,不该要求人刷新整页才看得到
// 按钮解锁。
export const oscStatusQueryOptions = () =>
  queryOptions({
    queryKey: ['osc', 'status'] as const,
    queryFn: oscApi.status,
    staleTime: 60_000,
  })

/**
 * 任务列表。**只在有任务还在跑的时候轮询**。
 *
 * 无条件每两秒拉一次的话,一个开着这张页面过夜的浏览器会往网关打上万次请求,
 * 而列表在那之后一个字都不会变。这里让轮询间隔自己看数据:还有 live 的任务就
 * 两秒一次,全是终态就停下来。
 */
export const oscJobsQueryOptions = () =>
  queryOptions({
    queryKey: ['osc', 'jobs'] as const,
    queryFn: oscApi.list,
    refetchInterval: (q) => (q.state.data?.some(isLive) ? 2000 : false),
  })
