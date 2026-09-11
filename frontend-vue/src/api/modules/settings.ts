import { http, ok, type Envelope } from '../shared'
import type {
  SettingsResp, WebhookConfig, WebhookDelivery,
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
}
