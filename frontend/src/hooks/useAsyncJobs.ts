import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { asyncJobQueryOptions, asyncJobsQueryOptions, terminalApi } from '@/api/modules/terminal'
import { CODE_INTERCEPTED, CODE_OK } from '@/api/http'
import { useUIStore } from '@/stores/ui'

export function useAsyncJobs() {
  return useQuery(asyncJobsQueryOptions())
}

/** 只在人点开某一条时才拉详情 —— 日志是整份任务里最大的那个字段。 */
export function useAsyncJob(id: number) {
  return useQuery(asyncJobQueryOptions(id))
}

export interface ExecAsyncVars {
  connectionId: number
  sql: string
  database: string
  reason: string
}

/**
 * 提交一条后台任务。
 *
 * 刻意**不**在 mutationFn 里把非 0 的业务码变成异常:42200 是"命中高危,已经替你
 * 建了审批单",它带着单号,是正常流程的一部分。调用方拿整个信封,自己决定弹窗关
 * 不关 —— 被转审批的时候表单里的语句还有用,不该被关掉。
 */
export function useExecAsync() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (v: ExecAsyncVars) =>
      terminalApi.execAsync(v.connectionId, v.sql, v.database, v.reason),
    onSuccess: (env) => {
      qc.invalidateQueries({ queryKey: ['async-jobs'] })
      if (env.code === CODE_OK) notify(t('asyncSubmitted'), 'ok')
      else if (env.code === CODE_INTERCEPTED) {
        notify(`${t('asyncIntercepted')} ${env.data?.approvalNo ?? ''}`.trim(), 'info')
      } else notify(env.msg || t('asyncSubmitFailed'), 'error')
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
