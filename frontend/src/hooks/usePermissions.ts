import {
  useMutation, useQueries, useQuery, useQueryClient, type QueryClient,
} from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  permissionsApi, rolesQueryOptions, roleQueryOptions, usersQueryOptions,
  userTagsQueryOptions, allTagsQueryOptions,
  type RoleDetailFull,
} from '@/api/modules/permissions'
import type { CapLevel } from '@/types'
import { envTiersQueryOptions } from '@/api/modules/envtier'
import { meQueryOptions } from '@/api/modules/auth'
import { isAdminOf } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

export function useRoles() {
  return useQuery(rolesQueryOptions())
}

export function useRole(id: number) {
  return useQuery(roleQueryOptions(id))
}

export function useUsers() {
  return useQuery(usersQueryOptions())
}

/**
 * 分层列表 —— 能力矩阵的列。
 *
 * 矩阵的列不能写死。分层是管理员建的行,写死一份清单意味着后建的分层在这张表里
 * 没有列,而**没有列的格子就是没人治理的格子**:矩阵里查不到的能力按放行处理。
 */
export function useEnvTiers() {
  return useQuery(envTiersQueryOptions())
}

/**
 * 档位由宽到严。数字小的更宽松,合并时取小的那个。
 *
 * 与服务端 `repository.levelRank` 同一张表(allow 0 / approve 1 / deny 2)。
 */
const LEVEL_RANK: Record<CapLevel, number> = { allow: 0, approve: 1, deny: 2 }

/**
 * 一个人的**有效权限** —— 把他名下每个角色的矩阵合成一张,每格取最宽松的那一档。
 *
 * ## 为什么是"最宽松",不是"最严格"
 *
 * 因为角色是**加法**。给一个人多挂一个角色,意图永远是"再多给他一点";要是取最
 * 严格的一档,那么给一位 DBA 额外挂上「只读」去看某个新库,反而会把他原有的写权限
 * 收掉 —— 没有任何管理员是这么想的,而界面上也不会有任何地方提示他刚刚削了谁的权。
 *
 * 更要紧的是:**网关就是这么合的**。service/gateway.go 的 BuildMe 调
 * repository.MatrixForRoles,那里逐格比 levelRank 取小值。这一页要是换一种合法,
 * 同一个人的权限在"管理员看到的"和"他自己实际能做的"之间就会对不上,而对不上的
 * 时候没人会怀疑是界面算错了。
 *
 * 所有角色都没写过的格子读作 allow,同样是照判定层的口径(见 CapabilityMatrix.levelOf)。
 */
export function useEffectiveMatrix(roleIds: number[]) {
  const results = useQueries({
    queries: roleIds.map((id) => roleQueryOptions(id)),
  })
  const matrix: Record<string, Record<string, string>> = {}
  for (const r of results) {
    const m = r.data?.matrix
    if (!m) continue
    for (const [cap, tiers] of Object.entries(m)) {
      matrix[cap] ??= {}
      for (const [tier, level] of Object.entries(tiers)) {
        const cur = matrix[cap][tier]
        if (!cur || LEVEL_RANK[level as CapLevel] < LEVEL_RANK[cur as CapLevel]) {
          matrix[cap][tier] = level
        }
      }
    }
  }
  return { matrix, isLoading: results.some((r) => r.isLoading) }
}

/** 角色与用户的所有改动都是管理员行为(服务端同样判一遍),非管理员整页只读。 */
export function useIsAdmin(): boolean {
  const { data: me } = useQuery(meQueryOptions())
  return isAdminOf(me ?? null)
}

function useNotifier() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    saved: () => notify(t('saved'), 'ok'),
    failed: (e: Error) => notify(e.message, 'error'),
  }
}

/**
 * 矩阵/菜单这类"点一下就生效"的开关做乐观更新。
 *
 * 能力矩阵的一格要循环三档,人会连点。等一次往返再变色的话,连点三下看到的是
 * 三次延迟到达的跳变,分不清自己现在停在哪一档 —— 于是又多点两下。先改缓存、
 * 失败回滚,是这个交互唯一说得通的做法。
 */
function optimisticRole(qc: QueryClient, id: number, patch: Partial<RoleDetailFull>) {
  const prev = qc.getQueryData<RoleDetailFull>(['role', id])
  if (prev) qc.setQueryData<RoleDetailFull>(['role', id], { ...prev, ...patch })
  return prev
}

export function useSetRoleCapabilities() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, matrix }: { id: number; matrix: Record<string, Record<string, string>> }) =>
      permissionsApi.setCapabilities(id, matrix),
    onMutate: async ({ id, matrix }) => {
      await qc.cancelQueries({ queryKey: ['role', id] })
      return { prev: optimisticRole(qc, id, { matrix }) }
    },
    onError: (e: Error, v, ctx) => {
      if (ctx?.prev) qc.setQueryData(['role', v.id], ctx.prev)
      n.failed(e)
    },
    onSuccess: n.saved,
    onSettled: (_d, _e, v) => qc.invalidateQueries({ queryKey: ['role', v.id] }),
  })
}

export function useSetRoleMenus() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, menus }: { id: number; menus: Record<string, boolean> }) =>
      permissionsApi.setMenus(id, menus),
    onMutate: async ({ id, menus }) => {
      await qc.cancelQueries({ queryKey: ['role', id] })
      return { prev: optimisticRole(qc, id, { menus }) }
    },
    onError: (e: Error, v, ctx) => {
      if (ctx?.prev) qc.setQueryData(['role', v.id], ctx.prev)
      n.failed(e)
    },
    onSuccess: n.saved,
    onSettled: (_d, _e, v) => qc.invalidateQueries({ queryKey: ['role', v.id] }),
  })
}

/**
 * 改角色本身(名称/默认库账号/描述/能否审批)。
 *
 * body 由调用方只装**改过的字段**。后端 UpdateRole 对空串与 nil 一律跳过,所以
 * "不发"等于"别动它" —— 这正是 issue #46 的修法:读不回来的字段就不要猜着发。
 */
export function useUpdateRole() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, body }: {
      id: number
      body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }
    }) => permissionsApi.updateRole(id, body),
    onSuccess: (detail, v) => {
      qc.setQueryData(['role', v.id], detail)
      qc.invalidateQueries({ queryKey: ['roles'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useAddRoleMember() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, userId }: { id: number; userId: number }) =>
      permissionsApi.addMember(id, userId),
    onSuccess: (detail, v) => {
      qc.setQueryData(['role', v.id], detail)
      // 成员数印在左侧角色卡上,用户页的角色列也跟着变 —— 两张都要失效。
      qc.invalidateQueries({ queryKey: ['roles'] })
      qc.invalidateQueries({ queryKey: ['users'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useRemoveRoleMember() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, userId }: { id: number; userId: number }) =>
      permissionsApi.removeMember(id, userId),
    onSuccess: (detail, v) => {
      qc.setQueryData(['role', v.id], detail)
      qc.invalidateQueries({ queryKey: ['roles'] })
      qc.invalidateQueries({ queryKey: ['users'] })
      n.saved()
    },
    onError: n.failed,
  })
}

/** 给一个人重新指派角色(多选,并集生效;第一个是主角色)。 */
export function useSetUserRoles() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, roleIds }: { id: number; roleIds: number[] }) =>
      permissionsApi.setUserRoles(id, roleIds),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      // 角色成员数与成员墙都变了,但改的是哪几个角色要由服务端说 —— 整族失效。
      qc.invalidateQueries({ queryKey: ['roles'] })
      qc.invalidateQueries({ queryKey: ['role'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useToggleUserStatus() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, status }: { id: number; status: 'active' | 'disabled' }) =>
      permissionsApi.patchUser(id, { status }),
    onSuccess: () => {
      // 停用会**收回该账户的全部角色**(service.PatchUser),所以角色那边同样要失效。
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['roles'] })
      qc.invalidateQueries({ queryKey: ['role'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useInviteUser() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ email, roleId }: { email: string; roleId: number }) =>
      permissionsApi.invite(email, roleId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['roles'] })
      n.saved()
    },
    onError: n.failed,
  })
}

export function useCreateUser() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: (body: { email: string; name?: string; password: string; roleIds: number[] }) =>
      permissionsApi.createUser(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['roles'] })
      n.saved()
    },
    onError: n.failed,
  })
}

// ─────────────────────────────────────────────────────────────────────────────
// 数据范围(标签)
//
// 一个人能碰哪些实例,由**标签**说了算,而不是由实例清单说了算:实例是天天在增的,
// 授权却不该跟着天天改。角色带一组标签,某个人还可以另带一组;**带了就以人为准,
// 空着才回落到角色**(service 那边同样是这个口径),所以"清空"是一个有意义的动作,
// 不是"没填"。
// ─────────────────────────────────────────────────────────────────────────────

/** 候选标签池。取自所有实例已经用过的标签,只是建议,不是可选值的全集。 */
export function useAllTags() {
  return useQuery(allTagsQueryOptions())
}

export function useSetRoleTags() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, tags }: { id: number; tags: string[] }) =>
      permissionsApi.setRoleTags(id, tags),
    onSuccess: (detail, v) => {
      // 这一条回的是整份 RoleDetail,直接坐进缓存,省掉一次往返。
      qc.setQueryData(['role', v.id], detail)
      n.saved()
    },
    onError: n.failed,
  })
}

/** 某个人**单独**被授的标签。空数组是合法值,含义是「按角色来」。 */
export function useUserTags(id: number) {
  return useQuery(userTagsQueryOptions(id))
}

export function useSetUserTags() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, tags }: { id: number; tags: string[] }) =>
      permissionsApi.setUserTags(id, tags),
    // 这一条只回 { ok: true },拿不到新值,所以老老实实失效重取。
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: ['user-tags', v.id] })
      n.saved()
    },
    onError: n.failed,
  })
}

// ─────────────────────────────────────────────────────────────────────────────
// 管理员代为处置一个账户的凭据
//
// 这三条都是**替别人做**的动作,所以它们不走 toast 了事:调用点要么先二次确认,
// 要么把结果就地写在那一小节里(代绑 OTP 会吐出一个只出现这一次的密钥)。
// ─────────────────────────────────────────────────────────────────────────────

/** 重置口令。长度后端也判(≥8),前端先判一次是为了少一次白跑的往返。 */
export function useSetUserPassword() {
  const n = useNotifier()
  return useMutation({
    mutationFn: ({ id, password }: { id: number; password: string }) =>
      permissionsApi.setUserPassword(id, password),
    onSuccess: n.saved,
    onError: n.failed,
  })
}

/**
 * 代绑 OTP。
 *
 * 服务端**当场生成新密钥并直接置为已启用**(service 的 UpdateUserMFA(id, true, …)),
 * 也就是说这次调用本身就改了那个人的登录方式 —— 他旧的验证器从这一刻起不再有效。
 * 返回的 otpauth URI 只在这次响应里出现,离开这个弹窗就再也拿不到,所以调用点要把
 * 它画成二维码留在屏幕上,而不是弹一下就收。
 */
export function useBindUserMfa() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: (id: number) => permissionsApi.bindUserMfa(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['users'] }),
    onError: n.failed,
  })
}

/** 解绑 OTP —— 那个人下次登录不再需要动态码。危险,调用点必须先二次确认。 */
export function useResetUserMfa() {
  const qc = useQueryClient()
  const n = useNotifier()
  return useMutation({
    mutationFn: (id: number) => permissionsApi.resetUserMfa(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      n.saved()
    },
    onError: n.failed,
  })
}
