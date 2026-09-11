import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  apiClientsQueryOptions, serviceAccountsQueryOptions, settingsApi, settingsQueryOptions,
} from '@/api/modules/settings'
import { approvalChainQueryOptions } from '@/api/modules/auth'
import { useUIStore } from '@/stores/ui'
import type { WebhookConfig } from '@/types'

export function useSettings() {
  return useQuery(settingsQueryOptions())
}

export function useApiClients() {
  return useQuery(apiClientsQueryOptions())
}

export function useServiceAccounts() {
  return useQuery(serviceAccountsQueryOptions())
}

/**
 * 默认审批链上的人。
 *
 * 取不到时**不要显示成空**:一个空列表会被读成"审批链上没有人",而真实情况通常是
 * 这个角色还没配人、或者调用者看不到成员名单 —— 两件事要人去做的动作完全不同。
 * 调用方照着 `error` 分开说(见设置页的审批分区)。
 */
export function useApprovalChain() {
  return useQuery(approvalChainQueryOptions())
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

/**
 * 发一张新凭据。
 *
 * **不 notify("已保存")** —— 这一步的结果不是"存好了",而是一段只出现这一次的
 * 明文令牌。把它降格成一条 4 秒后消失的 toast,等于把令牌弄丢。调用方拿返回值去
 * 开那一屏交接页,人确认抄走了才算完。
 */
export function useCreateApiClient() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: settingsApi.createApiClient,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['api-clients'] })
      qc.invalidateQueries({ queryKey: ['service-accounts'] }) // 每个账号名下的凭据数会变
    },
    onError: (e: Error) => notify(e.message || t('actionFailed'), 'error'),
  })
}

/** 删凭据是不可逆的:对面那套系统下一次调用就会 401,所以调用方要先确认。 */
export function useDeleteApiClient() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => settingsApi.deleteApiClient(id),
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['api-clients'] })
      qc.invalidateQueries({ queryKey: ['service-accounts'] })
    },
    onError: (e: Error) => notify(e.message || t('actionFailed'), 'error'),
  })
}

/** 建服务账号。建完通常紧接着就要给它发凭据,调用方据此把创建表单顺手打开。 */
export function useCreateServiceAccount() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: settingsApi.createServiceAccount,
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['service-accounts'] })
      qc.invalidateQueries({ queryKey: ['users'] })
    },
    onError: (e: Error) => notify(e.message || t('actionFailed'), 'error'),
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
