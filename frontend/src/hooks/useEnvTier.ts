import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import * as L from '@/lib/envTierLabels'
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
  const { t } = useTranslation()
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

  /**
   * 分层显示名。**委托给 `lib/envTierLabels`,不在这里另写一套。**
   *
   * 这里原先是"只读 displayName、从不翻译",而 lib 里是"内置 code 一律翻译" ——
   * 同一个问题两个答案,界面上就是同一个分层在两页显示不同的名字。判据只有一处:
   * 还是出厂那串就按语言说,被人改过就显示他改的那串。
   */
  function tierLabel(envCode: string): string {
    const code = tierCodeOf(envCode)
    return code ? L.tierLabel(code, tierList, t) : envCode
  }

  return {
    tiers: tierList,
    environments: envList,
    loading: tiers.isLoading || envs.isLoading,
    tierOf, tierCodeOf, tierLabel, envLabel,
  }
}

// ---------------------------------------------------------------- 管理端写入
//
// 分层与环境是**数据**:管理员建一个新的生产集群,是在 prod 分层下加一个环境,
// 不复制任何规则行,建好那一刻就受完整管控。下面这几个 mutation 是那件事的入口。
//
// 每一个都失效同一组 key(分层、环境、环境用量,以及跟着环境走的连接列表),
// 因为这四份数据在界面上是一起读的:改了绑定却只刷新其中一份,表上就会出现
// 一个分层显示旧颜色、另一处显示新颜色。

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { envtierApi, environmentUsageQueryOptions } from '@/api/modules/envtier'
import { useUIStore } from '@/stores/ui'

/** 每个环境名下的实例数 —— 删除环境时"要迁多少台"就是它。 */
export function useEnvironmentUsage() {
  return useQuery(environmentUsageQueryOptions())
}

function useEnvTierFeedback() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['env-tiers'] })
    qc.invalidateQueries({ queryKey: ['environments'] })
    qc.invalidateQueries({ queryKey: ['environment-usage'] })
    // 实例的环境码可能被迁移改写(删除环境时),列表跟着失效。
    qc.invalidateQueries({ queryKey: ['connections'] })
  }
  return { invalidate, notify, t }
}

/** 新建分层。**必须从现有分层克隆规则** —— 见 createEnvTier 上的说明。 */
export function useCreateEnvTier() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: (body: Partial<EnvTier> & { templateCode: string }) => envtierApi.createEnvTier(body),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/**
 * 改一个分层。
 *
 * 整行回写(`{...tier, ...patch}`)而不是只发改动的那几个字段:后端的更新接口读的是
 * 整个对象,只发一个 `requireMfa` 会把其余属性位按零值写回去 —— 一次"打开 MFA"
 * 顺手关掉了无 WHERE 拦截,而界面上什么都看不出来。
 */
export function useUpdateEnvTier() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: ({ tier, patch }: { tier: EnvTier; patch: Partial<EnvTier> }) =>
      envtierApi.updateEnvTier(tier.code, { ...tier, ...patch }),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    // 开关已经显示成按下去的样子了,失败要把它拨回来 —— 失效即重取。
    onError: (e: Error) => { notify(e.message, 'error'); invalidate() },
  })
}

export function useDeleteEnvTier() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: (code: string) => envtierApi.deleteEnvTier(code),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

export function useCreateEnvironment() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: (body: Partial<Environment>) => envtierApi.createEnvironment(body),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 改名或改绑分层。同样整行回写。 */
export function useUpdateEnvironment() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: ({ env, patch }: { env: Environment; patch: Partial<Environment> }) =>
      envtierApi.updateEnvironment(env.code, { ...env, ...patch }),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => { notify(e.message, 'error'); invalidate() },
  })
}

/**
 * 删除环境。`moveTo` 是必填的:实例要被搬到另一个环境,而不是留在原地指着一个
 * 不存在的码 —— 那样它解析不到分层,也就没有任何规则,看起来却和一台受管控的
 * 实例一模一样。
 */
export function useDeleteEnvironment() {
  const { invalidate, notify, t } = useEnvTierFeedback()
  return useMutation({
    mutationFn: ({ code, moveTo }: { code: string; moveTo: string }) =>
      envtierApi.deleteEnvironment(code, moveTo),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
