import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { execWindowsApi, execWindowsQueryOptions } from '@/api/modules/execWindows'
import { useUIStore } from '@/stores/ui'
import type { ExecWindow } from '@/types'

export function useExecWindows() {
  return useQuery(execWindowsQueryOptions())
}

function useInvalidate() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['exec-windows'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  }
}

export function useCreateExecWindow() {
  const h = useInvalidate()
  return useMutation({ mutationFn: (b: Partial<ExecWindow>) => execWindowsApi.create(b), ...h })
}

export function useDeleteExecWindow() {
  const h = useInvalidate()
  return useMutation({ mutationFn: (id: number) => execWindowsApi.remove(id), ...h })
}
