import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { Approval } from '@/types'

export type ApprovalScope = 'mine' | 'all'

export interface ApprovalPage {
  items: Approval[]
  total: number
}

export const approvalsApi = {
  list: (scope: ApprovalScope, page = 1, pageSize = 20) =>
    http
      .get<unknown, Envelope<ApprovalPage>>(
        `/approvals?scope=${scope}&page=${page}&pageSize=${pageSize}`,
      )
      .then(ok),

  /** 执行一张已通过的单。通过 ≠ 执行(ADR 0010),这一步是另一个动作。 */
  execute: (id: number, mfaCode = '') =>
    http.post<unknown, Envelope<unknown>>(`/approvals/${id}/execute`, { mfaCode }).then(ok),
}

export const approvalsQueryOptions = (scope: ApprovalScope, page = 1) =>
  queryOptions({
    queryKey: ['approvals', scope, page] as const,
    queryFn: () => approvalsApi.list(scope, page),
  })
