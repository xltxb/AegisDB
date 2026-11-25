import { create } from 'zustand'
import api from '@/api'
import * as L from '@/lib/envTierLabels'
import i18n from '@/locales'
import type { EnvTier, Environment } from '@/types'

type Lists = { tiers: EnvTier[]; environments: Environment[] }

// 由两份清单推出来的查找表。清单变了一起重算。
function derive({ tiers, environments }: Lists) {
  return {
    tiers,
    environments,
    tierByCode: Object.fromEntries(tiers.map((t) => [t.code, t])) as Record<string, EnvTier>,
    envByCode: Object.fromEntries(environments.map((e) => [e.code, e])) as Record<string, Environment>,
    /** Tier codes in display order — the column order of both rule tables. */
    tierCodes: tiers.map((t) => t.code),
    /** Environments grouped under their tier, both in display order. */
    envsByTier: L.groupByTier(tiers, environments),
  }
}

interface EnvTierState extends ReturnType<typeof derive> {
  loaded: boolean
  load(force?: boolean): Promise<void>
  tierOf(envCode: string): ReturnType<typeof L.tierOf>
  tierLabel(code: string, t: (k: string) => string): string
  envLabel(code: string): string
  dotFor(tierCode: string): ReturnType<typeof L.dotFor>
  dotForEnv(envCode: string): ReturnType<typeof L.dotForEnv>
}

/**
 * Control tiers and environments, loaded once and read from everywhere.
 *
 * These used to be four strings hardcoded in nine places. They are rows now, so
 * every list that used to be written out by hand — the tree's sections, the rule
 * tables' columns, the connection form's dropdown, the import validator — reads
 * from here instead, and a newly created environment shows up in all of them
 * without a reload.
 *
 * The label/colour resolution lives in lib/envTierLabels as pure functions; this
 * store is the loading and caching around them.
 */
export const useEnvTierStore = create<EnvTierState>()((set, get) => {
  let inflight: Promise<void> | null = null
  return {
    ...derive({ tiers: [], environments: [] }),
    loaded: false,

    /**
     * Fetch both lists. Concurrent callers share one request: several components
     * mount at once on a page load and each wants the data.
     */
    async load(force = false) {
      if (get().loaded && !force) return
      if (inflight) return inflight
      inflight = (async () => {
        const [tiers, environments] = await Promise.all([api.envTiers(), api.environments()])
        set({ ...derive({ tiers, environments }), loaded: true })
      })().finally(() => { inflight = null })
      return inflight
    },

    tierOf: (envCode) => L.tierOf(envCode, get().tiers, get().environments),
    tierLabel: (code, t) => L.tierLabel(code, get().tiers, t),
    envLabel: (code) => L.envLabel(code, get().environments, (k) => i18n.global.t(k)),
    dotFor: (tierCode) => L.dotFor(tierCode, get().tiers),
    dotForEnv: (envCode) => L.dotForEnv(envCode, get().tiers, get().environments),
  }
})
