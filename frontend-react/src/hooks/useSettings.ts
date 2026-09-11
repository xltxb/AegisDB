import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  apiClientsQueryOptions, settingsApi, settingsQueryOptions,
} from '@/api/modules/settings'
import { useUIStore } from '@/stores/ui'
import type { WebhookConfig } from '@/types'

export function useSettings() {
  return useQuery(settingsQueryOptions())
}

export function useApiClients() {
  return useQuery(apiClientsQueryOptions())
}

/**
 * 一次提交把运行时设置和 Webhook 一起存。
 *
 * 它们在后端是两个接口(`PUT /settings` 与 `PUT /settings/webhook`),但在界面上是
 * 同一个「保存设置」按钮。按顺序而不是并发:两个都失败时先报出来的是运行时设置那
 * 一个,而那是人改得最多的一块。
 *
 * 密钥类字段留空表示**保持不变**(服务端从不回传明文),所以调用方对着空输入框按
 * 保存不会把已配置的密钥清掉。
 */
export function useSaveSettings() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (body: { settings: Record<string, unknown>; webhook?: Partial<WebhookConfig> }) => {
      await settingsApi.saveSettings(body.settings)
      if (body.webhook) await settingsApi.saveWebhook(body.webhook)
    },
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => notify(e.message || t('saveFailed'), 'error'),
  })
}

/** 试发一条飞书卡片。先把当前填的地址存下来再试 —— 否则试的是上一次保存的那个。 */
export function useTestLark() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (cfg: { webhook: string; secret: string; consoleURL: string }) => {
      await settingsApi.saveSettings({
        'notify.larkWebhook': cfg.webhook.trim(),
        'notify.larkSecret': cfg.secret.trim(),
        'notify.consoleURL': cfg.consoleURL.trim(),
      })
      return settingsApi.testLark()
    },
    onSuccess: (r) => notify(r.message || t('larkTestOk'), r.ok ? 'ok' : 'error'),
    onError: (e: Error) => notify(e.message || t('larkTestFail'), 'error'),
  })
}

export function useTestWebhook() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: settingsApi.testWebhook,
    onSuccess: (r) => {
      notify(r?.message || t('whTestOk'), r?.ok === false ? 'error' : 'ok')
      qc.invalidateQueries({ queryKey: ['webhook-deliveries'] })
    },
    onError: (e: Error) => notify(e.message || t('whTestFail'), 'error'),
  })
}

/** 停用一个凭据是**立刻生效**的动作,不等下面那个保存按钮 —— 关掉它通常是急事。 */
export function useSetApiClientEnabled() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (v: { id: number; enabled: boolean }) =>
      settingsApi.setApiClientEnabled(v.id, v.enabled),
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['api-clients'] })
    },
    onError: (e: Error) => notify(e.message || t('saveFailed'), 'error'),
  })
}
