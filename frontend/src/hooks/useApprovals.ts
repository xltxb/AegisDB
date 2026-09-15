import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import {
  approvalsApi, approvalsQueryOptions,
  type ApprovalScope, type ApprovalStatus,
} from '@/api/modules/approvals'
import { APPROVAL_QUERY_KEYS } from '@/lib/approvals'
import { useUIStore } from '@/stores/ui'
import { useTranslation } from 'react-i18next'

export function useApprovals(
  scope: ApprovalScope, page = 1, status: ApprovalStatus = '', q = '',
) {
  return useQuery(approvalsQueryOptions(scope, page, status, q))
}

/**
 * 把 APPROVAL_QUERY_KEYS 一次性失效。决定、批量、执行都走这一条,不各写各的 ——
 * 两条路径各列一份键,正是侧栏那颗红点被漏掉的成因。
 */
export function invalidateApprovals(qc: QueryClient) {
  for (const queryKey of APPROVAL_QUERY_KEYS) qc.invalidateQueries({ queryKey })
}

/**
 * 执行一张通过的单。
 *
 * 不做乐观更新。执行是一次真的下发,结果要以库那边收没收下为准 —— 先把行变成
 * "已执行"再回滚,会让人以为变更生效过。失败时必须仍然是原样,所以只在成功后
 * 失效查询,让服务端说最终状态。
 */
export function useExecuteApproval() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()

  return useMutation({
    mutationFn: ({ id, mfaCode }: { id: number; mfaCode?: string }) =>
      approvalsApi.execute(id, mfaCode),
    onSuccess: () => {
      notify(t('apExecDone'), 'ok')
      invalidateApprovals(qc)
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/**
 * 通过 / 驳回一张待审工单。
 *
 * 不做乐观更新,理由和执行那一条一样:决定是一次真的状态迁移,后端还会在这一步
 * 上再判一遍可决定性(service.DecideBlockFor)。抢先把行画成「已通过」,再因为
 * 「该工单已被处理,请刷新」回滚,会让人以为自己批过又被撤销了。
 *
 * 成功后走 invalidateApprovals ——收件箱的几个分段、「我的申请」页、顶栏与侧栏
 * 那两颗计数读的是不同的键,少失效哪一个,那一处就会继续显示一张已经不在了的单子。
 * 这里刻意不自己列键:这份清单原来就是在这里被写漏的(侧栏那颗从来没进过),
 * 而漏掉的那一行看起来和写全了一模一样。
 */
export function useDecideApproval() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()

  return useMutation({
    mutationFn: ({ id, approve }: { id: number; approve: boolean }) =>
      approve ? approvalsApi.approve(id) : approvalsApi.reject(id),
    onSuccess: (_d, v) => {
      notify(t(v.approve ? 'ibApproved' : 'ibRejected'), 'ok')
      invalidateApprovals(qc)
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
