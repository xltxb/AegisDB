import { http, ok, type Envelope } from '../shared'
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
