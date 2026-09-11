import { keepPreviousData, queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import { auditQS } from '@/api/shared'
import type { AuditPage, AuditQuery } from '@/types'

export const auditApi = {
  audit: (q: AuditQuery) => http.get<any, Envelope<AuditPage>>(`/audit?${auditQS(q)}`).then(ok),

  /**
   * 导出走**同一个 http 客户端**,而不是自己拼 fetch。
   *
   * Vue 版在这里绕开了客户端:直接从 localStorage 取令牌、手写 Authorization 头
   * (issue #47)。绕过去的不只是一行请求头 —— 还有 baseURL、超时,以及 401 时清
   * 会话并送回登录页的那段逻辑。于是令牌过期时导出会静悄悄地下载下来一个内容是
   * 401 错误体的 .csv,而界面仍然显示已登录。
   *
   * responseType 为 blob 时响应拦截器交出来的 res.data 就是 Blob 本身(信封只用于
   * JSON 接口,CSV 不走信封)。
   */
  exportCsv: (q: AuditQuery) =>
    http.get<any, Blob>(`/audit/export?${auditQS(q)}`, { responseType: 'blob' }),
}

/**
 * 翻页时保留上一页(keepPreviousData)。
 *
 * 没有它,点"下一页"整张表会先空掉再填上,页脚的总数也跟着闪 —— 审计是用来一页
 * 一页往回翻的,那一下闪烁每翻一页就来一次。
 */
export const auditQueryOptions = (q: AuditQuery) =>
  queryOptions({
    queryKey: ['audit', q] as const,
    queryFn: () => auditApi.audit(q),
    placeholderData: keepPreviousData,
  })
