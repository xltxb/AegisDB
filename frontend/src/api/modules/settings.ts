import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import type {
  APIClient, ServiceAccount, SettingsResp, UserView, WebhookConfig, WebhookDelivery,
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
  // 建凭据会**当场返回一次明文令牌**(`key.secret`),服务端只留 bcrypt 散列,没有
  // 第二个接口能再取一次。所以创建这一步在界面上必须配一屏"只显示这一次"的交接页,
  // 见 components/settings/ApiClientsPanel.tsx。
  apiClients: () => http.get<any, Envelope<APIClient[]>>('/api-clients').then(ok),
  setApiClientEnabled: (id: number, enabled: boolean) =>
    http.put<any, Envelope<APIClient>>(`/api-clients/${id}`, { enabled }).then(ok),
  createApiClient: (body: {
    name: string; userId: number; allowIps?: string; scopes?: string[]
    pipelineId?: number; enabled?: boolean
  }) => http.post<any, Envelope<{ client: APIClient; token: string }>>('/api-clients', body).then(ok),
  deleteApiClient: (id: number) =>
    http.delete<any, Envelope<{ ok: boolean }>>(`/api-clients/${id}`).then(ok),

  // ---- 服务账号 ----
  // 凭据背后的机器主体。凭据该绑它,而不是某个人的账号 —— 人会离职、会换角色,
  // 绑在人身上的集成会跟着一起断。
  serviceAccounts: () => http.get<any, Envelope<ServiceAccount[]>>('/service-accounts').then(ok),
  createServiceAccount: (body: { name: string; roleIds: number[]; tags?: string[]; dept?: string }) =>
    http.post<any, Envelope<UserView>>('/service-accounts', body).then(ok),
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

export const serviceAccountsQueryOptions = () =>
  queryOptions({
    queryKey: ['service-accounts'] as const,
    queryFn: settingsApi.serviceAccounts,
    staleTime: 30_000,
    retry: false,
  })

export const webhookDeliveriesQueryOptions = (limit = 20) =>
  queryOptions({
    queryKey: ['webhook-deliveries', limit] as const,
    queryFn: () => settingsApi.webhookDeliveries(limit),
    staleTime: 10_000,
  })
