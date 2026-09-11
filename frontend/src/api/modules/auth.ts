import { queryOptions } from '@tanstack/react-query'
import http, { ok, type Envelope } from '@/api/http'
import type { Me, Member } from '@/types'

export const authApi = {
  me: () => http.get<unknown, Envelope<Me>>('/auth/me').then(ok),

  /** 默认审批链 —— 就是「DBA 负责人」这个角色的成员,维护在权限页,这里只读。 */
  approvalChain: () =>
    http.get<unknown, Envelope<{ chain: Member[] }>>('/approval-chain').then(ok),

  // ---- 二次验证(TOTP)自助绑定。每个账号都能用,不需要管理员。 ----
  // setup 只发密钥与 otpauth URI,**不改账号状态**;真正生效是 enable 那一步,
  // 而它要人先用验证器算出一个码 —— 否则会出现"绑上了但手机上没有"的账号。
  mfaSetup: () =>
    http.post<unknown, Envelope<{ secret: string; otpauthUri: string }>>('/auth/mfa/setup').then(ok),
  mfaEnable: (code: string) =>
    http.post<unknown, Envelope<{ ok: boolean }>>('/auth/mfa/enable', { code }).then(ok),
  mfaDisable: (code: string) =>
    http.post<unknown, Envelope<{ ok: boolean }>>('/auth/mfa/disable', { code }).then(ok),
}

/**
 * 审批链成员。
 *
 * `retry: false`:取不到几乎总是"这个角色没配人"或"没权限看",重试改变不了答案,
 * 而调用方要把"取不到"如实说出来 —— 显示成一个空列表会被读成"审批链是空的"。
 */
export const approvalChainQueryOptions = () =>
  queryOptions({
    queryKey: ['approval-chain'] as const,
    queryFn: authApi.approvalChain,
    staleTime: 60_000,
    retry: false,
  })

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
