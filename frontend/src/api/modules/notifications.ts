import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import type { Notification } from '@/types'

export interface NotificationsResp {
  items: Notification[]
  /** 未读条数由服务端数,不是 items 里数出来的 —— 列表是分页的。 */
  unread: number
}

export const notificationsApi = {
  list: (limit = 30) =>
    http.get<any, Envelope<NotificationsResp>>(`/notifications?limit=${limit}`).then(ok),
  /** 空 ids = 全部标为已读(服务端 MarkNotificationsRead 的约定)。 */
  markRead: (ids: number[] = []) =>
    http.post<any, Envelope<{ ok: boolean }>>('/notifications/read', { ids }).then(ok),
}

/**
 * 收件箱 20s 一轮。
 *
 * 没有终态可言,所以这是少数几个**不会自己停**的轮询之一:审批结果、导出完成、
 * 发布卡在等待,都是人早已离开那个页面之后才发生的事。
 *
 * `retry: false`:轮询本身就是重试,失败一轮等 20 秒再来即可,连着打三遍只是把
 * 同一个错误放大三倍。
 */
export const notificationsQueryOptions = (limit = 30) =>
  queryOptions({
    queryKey: ['notifications', limit] as const,
    queryFn: () => notificationsApi.list(limit),
    refetchInterval: 20_000,
    retry: false,
  })
