import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { imApi, imStatusQueryOptions } from '@/api/modules/im'
import { settingsApi } from '@/api/modules/settings'
import { useUIStore } from '@/stores/ui'

export function useImStatus() {
  return useQuery(imStatusQueryOptions())
}

/**
 * 测试前先把当前表单里的 IM 配置存下来 —— 服务端只用已保存的配置去发,不存的话试的是
 * 上一次保存的那一份,而管理员以为试的是眼前这一份。
 */
function useImTest(fn: (kind: string) => Promise<{ ok: boolean; message: string }>) {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (v: { kind: string; settings: Record<string, unknown> }) => {
      await settingsApi.saveSettings(v.settings)
      return fn(v.kind)
    },
    onSuccess: (r) => {
      notify(r.message || t('imTestOk'), r.ok ? 'ok' : 'error')
      qc.invalidateQueries({ queryKey: ['im-status'] })
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => notify(e.message || t('imTestFail'), 'error'),
  })
}

export function useTestImNotify() {
  return useImTest(imApi.testNotify)
}

export function useTestImCard() {
  return useImTest(imApi.testCard)
}
