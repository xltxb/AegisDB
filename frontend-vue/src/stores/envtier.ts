import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import api from '@/api'
import * as L from '@/lib/envTierLabels'
import type { EnvTier, Environment } from '@/types'

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
export const useEnvTierStore = defineStore('envtier', () => {
  const tiers = ref<EnvTier[]>([])
  const environments = ref<Environment[]>([])
  const loaded = ref(false)
  let inflight: Promise<void> | null = null

  const tierByCode = computed(() => Object.fromEntries(tiers.value.map((t) => [t.code, t])))
  const envByCode = computed(() => Object.fromEntries(environments.value.map((e) => [e.code, e])))

  /** Tier codes in display order — the column order of both rule tables. */
  const tierCodes = computed(() => tiers.value.map((t) => t.code))
  /** Environments grouped under their tier, both in display order. */
  const envsByTier = computed(() => L.groupByTier(tiers.value, environments.value))

  /**
   * Fetch both lists. Concurrent callers share one request: several components
   * mount at once on a page load and each wants the data.
   */
  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (inflight) return inflight
    inflight = (async () => {
      const [ts, es] = await Promise.all([api.envTiers(), api.environments()])
      tiers.value = ts
      environments.value = es
      loaded.value = true
    })().finally(() => { inflight = null })
    return inflight
  }

  const tierOf = (envCode: string) => L.tierOf(envCode, tiers.value, environments.value)
  const tierLabel = (code: string, t: (k: string) => string) => L.tierLabel(code, tiers.value, t)
  const envLabel = (code: string) => L.envLabel(code, environments.value)
  const dotFor = (tierCode: string) => L.dotFor(tierCode, tiers.value)
  const dotForEnv = (envCode: string) => L.dotForEnv(envCode, tiers.value, environments.value)

  return {
    tiers, environments, loaded, tierCodes, tierByCode, envByCode, envsByTier,
    load, tierOf, tierLabel, envLabel, dotFor, dotForEnv,
  }
})
