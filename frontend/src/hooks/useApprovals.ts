import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { approvalsApi, approvalsQueryOptions, type ApprovalScope } from '@/api/modules/approvals'
import { useUIStore } from '@/stores/ui'
import { useTranslation } from 'react-i18next'

export function useApprovals(scope: ApprovalScope, page = 1) {
  return useQuery(approvalsQueryOptions(scope, page))
}

/**
 * 执行一张通过的单。
 *
 * 不做乐观更新。执行是一次真的下发,结果要以库那边收没收下为准 —— 先把行变成
 * "已执行"再回滚,会让人以为变更生效过。失败时必须仍然是原样,所以只在成功后
 * 失效查询,让服务端说最终状态。
 */
export function useExecuteApproval(scope: ApprovalScope) {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()

  return useMutation({
    mutationFn: ({ id, mfaCode }: { id: number; mfaCode?: string }) =>
      approvalsApi.execute(id, mfaCode),
    onSuccess: () => {
      notify(t('apExecDone'), 'ok')
      qc.invalidateQueries({ queryKey: ['approvals', scope] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
