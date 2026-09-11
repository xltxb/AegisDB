import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { Me } from '@/types'

export const authApi = {
  me: () => http.get<unknown, Envelope<Me>>('/auth/me').then(ok),
}

/**
 * `/auth/me` 带回 user + menus + capabilities,守卫与能力层都读它。
 *
 * `staleTime: Infinity` 是有意的:菜单与能力矩阵在一次会话里不会自己变,而守卫
 * 在**每次导航**都要 `ensureQueryData` 一次 —— 没有这个,换一页就多打一次接口。
 * 权限真的被改动时,后端会让令牌代次失效,人被弹回登录页,缓存随之作废。
 */
export const meQueryOptions = () =>
  queryOptions({
    queryKey: ['me'] as const,
    queryFn: authApi.me,
    staleTime: Infinity,
    retry: false,
  })
