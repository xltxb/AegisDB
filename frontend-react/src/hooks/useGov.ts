import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { govApi, sensitiveColumnsQueryOptions } from '@/api/modules/gov'
import { useUIStore } from '@/stores/ui'
import type { SensitiveColumn } from '@/types'

export function useSensitiveColumns() {
  return useQuery(sensitiveColumnsQueryOptions())
}

function useNotifier() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    saved: () => notify(t('saved'), 'ok'),
    failed: (e: Error) => notify(e.message, 'error'),
  }
}

export function useSaveSensitiveColumn() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Partial<SensitiveColumn> }) =>
      govApi.saveSensitiveColumn(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['sensitive-columns'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useDeleteSensitiveColumn() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: (id: number) => govApi.deleteSensitiveColumn(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['sensitive-columns'] })
      n.saved()
    },
    onError: n.failed,
  })
}

/**
 * 批量导入 —— 一行一条,顺序逐条 POST。
 *
 * 后端没有 import 接口,这里也不假装有:它只是把粘进来的几行拆开,走的仍然是
 * 新增那一条路由。**顺序**而不是并发,是因为一批规则里如果第五行写错了,人要
 * 知道前四行已经建好了 —— 并发发出去再收一堆成败混杂的结果,没人说得清库里
 * 现在到底有哪几条。
 */
export function useImportSensitiveColumns() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: async (rows: Partial<SensitiveColumn>[]) => {
      let done = 0
      for (const r of rows) {
        await govApi.saveSensitiveColumn(0, r)
        done++
      }
      return done
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ['sensitive-columns'] }),
    onSuccess: n.saved,
    onError: n.failed,
  })
}
