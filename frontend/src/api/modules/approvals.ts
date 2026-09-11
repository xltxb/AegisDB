import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { Approval } from '@/types'

export type ApprovalScope = 'mine' | 'all'

export interface ApprovalPage {
  items: Approval[]
  total: number
  /** 当前 scope 下待审批的条数,由服务端数。列表是分页的,数一页会悄悄封顶。 */
  pending: number
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

/**
 * 顶栏那颗「待审批 n」的来源:只要计数,所以 pageSize=1,不把一整页工单拖回来。
 *
 * 单独一个 queryKey 而不是复用 `approvals` 那几个 —— 审批页的键带着页码与筛选,
 * 外壳蹭它就会在人翻页时跟着变数,而顶栏的数字与"我现在看的是第几页"无关。
 */
export const pendingApprovalsQueryOptions = () =>
  queryOptions({
    queryKey: ['approvals-pending'] as const,
    queryFn: () => approvalsApi.list('all', 1, 1).then((r) => r.pending),
    refetchInterval: 60_000,
    retry: false,
  })
