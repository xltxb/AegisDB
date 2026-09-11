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
