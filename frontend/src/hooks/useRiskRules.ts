import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { riskRulesApi, riskCommandsQueryOptions } from '@/api/modules/riskRules'
import { terminalApi } from '@/api/modules/terminal'
import { useUIStore } from '@/stores/ui'
import type { RiskCommandView } from '@/types'

export function useRiskCommands() {
  return useQuery(riskCommandsQueryOptions())
}

/** 网关累计拦截数 —— 真实计数,不是装饰用的示例数字。 */
export function useGatewayStats() {
  return useQuery({
    queryKey: ['gateway-stats'] as const,
    queryFn: terminalApi.gatewayStats,
    staleTime: 30_000,
  })
}

/**
 * 三个写操作共用的收尾。
 *
 * 服务端每次都回整本字典,所以直接 `setQueryData` 落地,再 `invalidateQueries`
 * 兜一次后台校准 —— 前者让界面立刻是对的,后者保证它和服务端最终一致。
 */
function useDictWrite() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    onSuccess: (list: RiskCommandView[]) => {
      qc.setQueryData(['risk-commands'], list)
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['risk-commands'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  }
}

/**
 * 新增一条命令,**逐分层**给等级。
 *
 * 调用方必须把所有分层都填上:一个分层缺行,判定层读到的是"放行",而页面上看不出
 * 这是谁的决定。服务端还会照全部分层再展开一次(repository.UpsertRiskCommand),
 * 这里把界面上看得见的那份也给全,是为了让人在写下去之前就看到自己在决定什么。
 */
export function useUpsertRiskCommand() {
  const h = useDictWrite()
  return useMutation({
    mutationFn: ({ command, tiers }: { command: string; tiers: Record<string, string> }) =>
      riskRulesApi.upsertRiskCommand(command, tiers),
    ...h,
  })
}

/** 只改一个分层的等级 —— 服务端不会把同一条命令的其它分层一起带走。 */
export function usePatchRiskCommand() {
  const h = useDictWrite()
  return useMutation({
    mutationFn: ({ command, tier, level }: { command: string; tier: string; level: string }) =>
      riskRulesApi.patchRiskCommand(command, tier, level),
    ...h,
  })
}

export function useDeleteRiskCommand() {
  const h = useDictWrite()
  return useMutation({
    mutationFn: (command: string) => riskRulesApi.deleteRiskCommand(command),
    ...h,
  })
}
