import { useQuery } from '@tanstack/react-query'
import { environmentsQueryOptions, envTiersQueryOptions, tierOfEnv } from '@/api/modules/envtier'
import type { EnvTier } from '@/types'

/**
 * 由环境码查分层。
 *
 * 「这是不是一台要出红色警告的库」问的是**分层上的属性位**(`dangerBanner`),不是
 * 环境码叫不叫 prod。两者在默认部署里恰好一致,所以写错了也要等到有人加第二个生产
 * 环境、或者把 prod 挂到别的分层上,才会被发现。
 */
export function useTierOf(): (envCode: string) => EnvTier | undefined {
  const { data: tiers } = useQuery(envTiersQueryOptions())
  const { data: envs } = useQuery(environmentsQueryOptions())
  return (envCode: string) => tierOfEnv(envCode, tiers ?? [], envs ?? [])
}
