import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'

/** 一个 IM 渠道的运行状况。能力(canX)是登记时声明的,开关(notify/approval)是设置里的。 */
export interface ImStatus {
  kind: string
  label: string
  canNotify: boolean
  canApprove: boolean
  configured: boolean
  notify: boolean
  approval: boolean
  running: boolean
  botName?: string
  lastError?: string
}

/** 某人在一个交互审批渠道上的绑定。 */
export interface ImIdentity {
  channel: string
  label: string
  enabled: boolean
  bound: boolean
  platformUserId?: string
  boundBy?: 'self' | 'admin'
  botName?: string
  boundAt?: string
}

type TestResult = { ok: boolean; message: string }

export const imApi = {
  status: () => http.get<any, Envelope<ImStatus[]>>('/settings/im/status').then(ok),
  testNotify: (kind: string) =>
    http.post<any, Envelope<TestResult>>(`/settings/im/${kind}/test-notify`).then(ok),
  testCard: (kind: string) =>
    http.post<any, Envelope<TestResult>>(`/settings/im/${kind}/test-card`).then(ok),

  myIdentities: () => http.get<any, Envelope<ImIdentity[]>>('/me/im-identities').then(ok),
  // 码只在这一次响应里出现。服务端只存哈希,没有第二个接口能再取。
  issueCode: (channel: string) =>
    http.post<any, Envelope<{ code: string; expiresAt: string; botName?: string }>>(
      `/me/im-identities/${channel}/code`).then(ok),
  unbindMine: (channel: string) =>
    http.delete<any, Envelope<{ ok: boolean }>>(`/me/im-identities/${channel}`).then(ok),

  userIdentities: (id: number) =>
    http.get<any, Envelope<ImIdentity[]>>(`/users/${id}/im-identities`).then(ok),
  bindUser: (id: number, channel: string, platformUserId: string) =>
    http.put<any, Envelope<{ ok: boolean }>>(`/users/${id}/im-identities/${channel}`, { platformUserId }).then(ok),
  unbindUser: (id: number, channel: string) =>
    http.delete<any, Envelope<{ ok: boolean }>>(`/users/${id}/im-identities/${channel}`).then(ok),
}

export const imStatusQueryOptions = () =>
  queryOptions({
    queryKey: ['im-status'] as const,
    queryFn: imApi.status,
    // 长连接会自己断、自己重连;状态点要跟得上,但不必实时。
    refetchInterval: 10_000,
  })

export const myImIdentitiesQueryOptions = (poll = false) =>
  queryOptions({
    queryKey: ['im-identities', 'me'] as const,
    queryFn: imApi.myIdentities,
    // 绑定码弹出后,用户去 IM 里发码;这边轮询,绑上了自己变。
    refetchInterval: poll ? 3_000 : false,
  })

export const userImIdentitiesQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['im-identities', id] as const,
    queryFn: () => imApi.userIdentities(id),
  })
