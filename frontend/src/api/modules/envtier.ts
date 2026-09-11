import { http, ok, type Envelope } from '../shared'
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
