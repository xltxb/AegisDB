import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ok } from '@/api/http'
import { reviewApi, reviewCatalogQueryOptions, reviewRulesQueryOptions } from '@/api/modules/review'
import { useUIStore } from '@/stores/ui'
import type { ReviewRule } from '@/types'

export function useReviewRules() {
  return useQuery(reviewRulesQueryOptions())
}

export function useReviewCatalog() {
  return useQuery(reviewCatalogQueryOptions())
}

function useRuleWrite() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['review-rules'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  }
}

/**
 * 新建(id=0)与保存是同一个 mutation。
 *
 * `reviewApi.saveReviewRule` 交出来的是原始信封 —— 它被写成这样是为了让调用方能
 * 看业务码;这一页不需要看,所以在这里就 `ok()` 解开,让失败走 Query 的错误通道。
 */
export function useSaveReviewRule() {
  const h = useRuleWrite()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Partial<ReviewRule> }) =>
      reviewApi.saveReviewRule(id, body).then(ok),
    ...h,
  })
}

export function useDeleteReviewRule() {
  const h = useRuleWrite()
  return useMutation({
    mutationFn: (id: number) => reviewApi.deleteReviewRule(id).then(ok),
    ...h,
  })
}

/**
 * 上线前自查:把一段 SQL 丢给服务端跑一次规则。
 *
 * 它不是写操作,所以不报「已保存」,也不失效任何缓存 —— 结果只属于刚才那一次点击,
 * 留在 mutation 自己的 data 里。超时 120s 由 api 层给(脚本可以很长)。
 */
export function useReviewCheck() {
  const notify = useUIStore((s) => s.notify)
  return useMutation({
    mutationFn: (b: { connectionId?: number; dialect?: string; sql: string }) =>
      reviewApi.reviewCheck(b),
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
