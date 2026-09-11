import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import type {
  APIClient, SettingsResp, WebhookConfig, WebhookDelivery,
} from '@/types'

export const settingsApi = {
  // ---- settings ----
  settings: () => http.get<any, Envelope<SettingsResp>>('/settings').then(ok),
  saveSettings: (body: Record<string, any>) => http.put('/settings', body),
  saveWebhook: (body: Partial<WebhookConfig>) =>
    http.put<any, Envelope<WebhookConfig>>('/settings/webhook', body).then(ok),
  testWebhook: () => http.post<any, Envelope<any>>('/settings/webhook/test').then(ok),
  testLark: () => http.post<any, Envelope<{ ok: boolean; message: string }>>('/settings/lark/test').then(ok),
  webhookDeliveries: (limit = 50) =>
    http.get<any, Envelope<WebhookDelivery[]>>(`/settings/webhook/deliveries?limit=${limit}`).then(ok),

  // ---- 开放接口凭据 ----
  // 建凭据不在这里:那一步会**当场返回一次明文令牌**,之后再也取不到,得配一整套
  // "只显示这一次"的交接界面。设置页只管开关与查看。
  apiClients: () => http.get<any, Envelope<APIClient[]>>('/api-clients').then(ok),
  setApiClientEnabled: (id: number, enabled: boolean) =>
    http.put<any, Envelope<APIClient>>(`/api-clients/${id}`, { enabled }).then(ok),
}

export const settingsQueryOptions = () =>
  queryOptions({
    queryKey: ['settings'] as const,
    queryFn: settingsApi.settings,
  })

export const apiClientsQueryOptions = () =>
  queryOptions({
    queryKey: ['api-clients'] as const,
    queryFn: settingsApi.apiClients,
    staleTime: 30_000,
  })

export const webhookDeliveriesQueryOptions = (limit = 20) =>
  queryOptions({
    queryKey: ['webhook-deliveries', limit] as const,
    queryFn: () => settingsApi.webhookDeliveries(limit),
    staleTime: 10_000,
  })
