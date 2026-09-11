import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { Approval } from '@/types'

export type ApprovalScope = 'mine' | 'all'

/** 只认后端认的四个状态;空串表示不筛。拼错的值后端会直接回 400。 */
export type ApprovalStatus = '' | 'pending' | 'approved' | 'rejected' | 'expired'

export interface ApprovalPage {
  items: Approval[]
  total: number
  /** 当前 scope 下待审批的条数,由服务端数。列表是分页的,数一页会悄悄封顶。 */
  pending: number
}

export const approvalsApi = {
  /**
   * status 在**服务端**筛,不是取一页回来自己过滤。
   *
   * 列表是分页的:一张躺在第三页的待审单,客户端筛只会得到"没有" —— 而收件箱
   * 顶上那个「待审 · N」正是靠它数数,漏报等于让人以为自己没有待办。
   */
  list: (scope: ApprovalScope, page = 1, pageSize = 20, status: ApprovalStatus = '') =>
    http
      .get<unknown, Envelope<ApprovalPage>>(
        `/approvals?scope=${scope}&page=${page}&pageSize=${pageSize}` +
          (status ? `&status=${status}` : ''),
      )
      .then(ok),

  /**
   * 通过 / 驳回。
   *
   * 两个接口而不是一个带布尔参数的:后端就是两条路由,而"批准"和"驳回"在审计里
   * 是两件不同的事,不该在传输层被合并成同一个动作的两种取值。
   */
  approve: (id: number) =>
    http.post<unknown, Envelope<unknown>>(`/approvals/${id}/approve`).then(ok),

  reject: (id: number) =>
    http.post<unknown, Envelope<unknown>>(`/approvals/${id}/reject`).then(ok),

  /** 执行一张已通过的单。通过 ≠ 执行(ADR 0010),这一步是另一个动作。 */
  execute: (id: number, mfaCode = '') =>
    http.post<unknown, Envelope<unknown>>(`/approvals/${id}/execute`, { mfaCode }).then(ok),
}

export const approvalsQueryOptions = (
  scope: ApprovalScope,
  page = 1,
  status: ApprovalStatus = '',
) =>
  queryOptions({
    queryKey: ['approvals', scope, page, status] as const,
    queryFn: () => approvalsApi.list(scope, page, 20, status),
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

/**
 * 「审批待办」那颗红点的来源 —— **只数轮到我签字的**。
 *
 * 与顶栏那颗(pendingApprovalsQueryOptions)不是一个口径,所以不能共用:后者走
 * scope=all,数的是「我发起的待审 + 我要签的待审」两类之和。把它挂在待办上,
 * 会让一个提了单还没人批的人看到一颗永远消不掉的红点,而他其实无事可做。
 */
export const inboxPendingQueryOptions = () =>
  queryOptions({
    queryKey: ['approvals-inbox-pending'] as const,
    queryFn: () => approvalsApi.list('mine', 1, 1, 'pending').then((r) => r.pending),
    refetchInterval: 60_000,
    retry: false,
  })
