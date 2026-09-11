import { http, ok, type Envelope } from '@/api/shared'
import type {
  EnvTier, Environment,
} from '@/types'

export const envtierApi = {
  // ---- control tiers & environments ----
  // Reads are open to any signed-in user: the instance tree, connection form and
  // rule tables all render from them. Writes need the envtier menu + admin.
  envTiers: () => http.get<any, Envelope<EnvTier[]>>('/env-tiers').then(ok),
  // A tier MUST be cloned from an existing one — a tier with no rule rows is an
  // environment where every lookup falls through to allowed.
  createEnvTier: (body: Partial<EnvTier> & { templateCode: string }) =>
    http.post<any, Envelope<EnvTier>>('/env-tiers', body).then(ok),
  updateEnvTier: (code: string, body: Partial<EnvTier>) =>
    http.put<any, Envelope<EnvTier>>(`/env-tiers/${encodeURIComponent(code)}`, body).then(ok),
  deleteEnvTier: (code: string) =>
    http.delete<any, Envelope<any>>(`/env-tiers/${encodeURIComponent(code)}`).then(ok),

  environments: () => http.get<any, Envelope<Environment[]>>('/environments').then(ok),
  /** instance count per environment — what a delete is about to move. */
  environmentUsage: () =>
    http.get<any, Envelope<Record<string, number>>>('/environments/usage').then(ok),
  createEnvironment: (body: Partial<Environment>) =>
    http.post<any, Envelope<Environment>>('/environments', body).then(ok),
  updateEnvironment: (code: string, body: Partial<Environment>) =>
    http.put<any, Envelope<Environment>>(`/environments/${encodeURIComponent(code)}`, body).then(ok),
  /** moveTo is mandatory: instances are reassigned, never left dangling. */
  deleteEnvironment: (code: string, moveTo: string) =>
    http.delete<any, Envelope<any>>(`/environments/${encodeURIComponent(code)}`, {
      data: { moveTo },
    }).then(ok),
}

// ---- TanStack Query 绑定 ----
// 分层与环境在一次会话里基本不变,但它们决定的是「要不要出红色警告」这类判断,
// 所以放在共用的 key 下由 Query 缓存,而不是各页各拉一次。
import { queryOptions } from '@tanstack/react-query'

export const envTiersQueryOptions = () =>
  queryOptions({
    queryKey: ['env-tiers'] as const,
    queryFn: envtierApi.envTiers,
    staleTime: 5 * 60_000,
  })

export const environmentsQueryOptions = () =>
  queryOptions({
    queryKey: ['environments'] as const,
    queryFn: envtierApi.environments,
    staleTime: 5 * 60_000,
  })

/**
 * 一个环境码归属哪个分层。
 *
 * 判「是不是生产」要走这里,而不是看环境码是不是叫 prod:规则挂在**分层**上,
 * 环境只决定实例归属。第二个生产集群(prod-hk)照样该出警告,而把 prod 环境改挂
 * 到 dev 分层之后就不该出。
 */
export function tierOfEnv(
  envCode: string, tiers: EnvTier[], envs: Environment[],
): EnvTier | undefined {
  const e = envs.find((x) => x.code === envCode)
  return e ? tiers.find((x) => x.code === e.tierCode) : undefined
}

/**
 * 每个环境名下有多少台实例。
 *
 * 删除环境时必须指定实例迁往哪里,而"要迁多少台"正是这个数 —— 它是那句确认文案
 * 里唯一的事实。服务端算不出来时(接口缺失/无权限)界面按 0 显示,不拦住删除:
 * 迁移目标仍然是必填的,真正的约束在服务端。
 */
export const environmentUsageQueryOptions = () =>
  queryOptions({
    queryKey: ['environment-usage'] as const,
    queryFn: envtierApi.environmentUsage,
    staleTime: 60_000,
  })
