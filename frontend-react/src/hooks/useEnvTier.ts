import { useQuery } from '@tanstack/react-query'
import { envTiersQueryOptions, environmentsQueryOptions } from '@/api/modules/envtier'
import type { EnvTier, Environment } from '@/types'

/**
 * 分层与环境的读取口。
 *
 * **规则挂在分层上,环境只决定实例归属** —— 所以任何"这是不是生产"的判断都要
 * 先用实例的 env 找到环境、再由环境找到分层,不能看环境名叫不叫 prod。第二个
 * 生产集群(prod-hk)照样是生产,而把 prod 环境改挂到 dev 分层之后就不是了。
 *
 * 每个函数都要能回答一个**从未见过的 code**:分层与环境是管理员可删的行,而它们
 * 的 code 会永远留在审批与审计里 —— 那正是删除之后有人要读的页面。未知 code
 * 一律显示原文、取中性色,不抛错。
 */
export function useEnvTier() {
  const tiers = useQuery(envTiersQueryOptions())
  const envs = useQuery(environmentsQueryOptions())

  const tierList = (tiers.data ?? []) as EnvTier[]
  const envList = (envs.data ?? []) as Environment[]

  function tierCodeOf(envCode: string): string {
    return envList.find((e) => e.code === envCode)?.tierCode ?? ''
  }

  function tierOf(envCode: string): EnvTier | undefined {
    const code = tierCodeOf(envCode)
    return code ? tierList.find((x) => x.code === code) : undefined
  }

  function envLabel(envCode: string): string {
    return envList.find((e) => e.code === envCode)?.displayName || envCode
  }

  function tierLabel(envCode: string): string {
    return tierOf(envCode)?.displayName || tierCodeOf(envCode) || envCode
  }

  return {
    tiers: tierList,
    environments: envList,
    loading: tiers.isLoading || envs.isLoading,
    tierOf, tierCodeOf, tierLabel, envLabel,
  }
}
