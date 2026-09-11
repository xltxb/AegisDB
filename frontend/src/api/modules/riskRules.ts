import { http, ok, type Envelope } from '@/api/shared'
import type {
  RiskCommandView,
} from '@/types'

export const riskRulesApi = {
  // ---- risk commands ----
  riskCommands: () => http.get<any, Envelope<RiskCommandView[]>>('/risk-commands').then(ok),
  upsertRiskCommand: (command: string, tiers: Record<string, string>) =>
    http.post<any, Envelope<RiskCommandView[]>>('/risk-commands', { command, tiers }).then(ok),
  patchRiskCommand: (name: string, tier: string, level: string) =>
    http.patch<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`, { tier, level }).then(ok),
  deleteRiskCommand: (name: string) =>
    http.delete<any, Envelope<RiskCommandView[]>>(`/risk-commands/${name}`).then(ok),
}

// ---- TanStack Query 绑定 ----
import { queryOptions } from '@tanstack/react-query'

/**
 * 高危命令字典。
 *
 * 每个写操作(新增 / 切等级 / 删除)服务端都回**整本字典**,所以 mutation 可以直接
 * 把返回值塞进这个 key,不必再多跑一次 GET —— 少一次往返,也少一次"刚点完还是旧值"
 * 的闪烁。
 */
export const riskCommandsQueryOptions = () =>
  queryOptions({
    queryKey: ['risk-commands'] as const,
    queryFn: riskRulesApi.riskCommands,
  })
