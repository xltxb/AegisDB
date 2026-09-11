import { http, ok, type Envelope } from '@/api/shared'
import { SCRIPT_TIMEOUT_MS } from '@/api/shared'
import type {
  ReviewCatalog, ReviewCheckResp, ReviewRule,
} from '@/types'

export const reviewApi = {
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
}

// ---- TanStack Query 绑定 ----
import { queryOptions } from '@tanstack/react-query'

export const reviewRulesQueryOptions = () =>
  queryOptions({
    queryKey: ['review-rules'] as const,
    queryFn: reviewApi.reviewRules,
  })

/**
 * 方言 / 分类 / 级别 / 规范分级的词汇表。
 *
 * 它是服务端的词汇表而不是前端常量:规则可以引用一个这份构建从没听说过的方言,
 * 把它写死在前端只会让那条规则在筛选器里消失。
 */
export const reviewCatalogQueryOptions = () =>
  queryOptions({
    queryKey: ['review-catalog'] as const,
    queryFn: reviewApi.reviewCatalog,
    staleTime: 5 * 60_000,
  })
