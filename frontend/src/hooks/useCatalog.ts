import { queryOptions, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  connectionMetadataQueryOptions, connectionsApi, metadataSearchQueryOptions,
} from '@/api/modules/connections'
import { pipelineApi } from '@/api/modules/pipeline'
import { useUIStore } from '@/stores/ui'

export function useConnectionMetadata(connectionId: number, database = '') {
  return useQuery(connectionMetadataQueryOptions(connectionId, database))
}

export function useMetadataSearch(q: string) {
  return useQuery(metadataSearchQueryOptions(q))
}

/**
 * 敏感字段规则。数据资产页用它来标 PII —— 后端的 MetaColumn 里**没有** PII 这个
 * 字段,凭空造一个会变成一份看起来权威、实际没人维护的清单。规则表是现成的、有人
 * 维护的事实:命中脱敏规则的列,就是这套网关眼里的敏感列。
 *
 * 它只是展示:真正的打码在服务端做,与这里标不标无关。
 */
export const sensitiveColumnsQueryOptions = () =>
  queryOptions({
    queryKey: ['sensitive-columns'] as const,
    queryFn: pipelineApi.sensitiveColumns,
    staleTime: 5 * 60_000,
  })

export function useSensitiveColumns() {
  return useQuery(sensitiveColumnsQueryOptions())
}

/**
 * 立刻同步一台实例。
 *
 * 它**真的会登录那台库**,所以是一次明确的动作,不是打开页面的副作用。成功后失效
 * 这台实例的缓存 key,让副本的年龄(syncedAt)当场更新 —— 一份不刷新的缓存界面,
 * 人按完同步会以为什么都没发生。
 */
export function useSyncMetadata() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => connectionsApi.syncConnectionMetadata(id),
    onSuccess: (_data, id) => {
      notify(t('catSyncDone'), 'ok')
      qc.invalidateQueries({ queryKey: ['connection-metadata', id] })
      qc.invalidateQueries({ queryKey: ['metadata-search'] })
    },
    onError: (e: Error) => notify(e.message || t('catSyncFailed'), 'error'),
  })
}
