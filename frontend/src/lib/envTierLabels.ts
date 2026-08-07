// envTierLabels — resolving a tier/environment code to a label and a colour.
//
// Pure functions over the two lists, kept out of the Pinia store so they can be
// tested directly (the store pulls in the axios client, which needs
// bundler-provided import.meta.env).
//
// What they all have in common is the unresolvable case. Tiers and environments
// are rows an administrator can delete, and their codes live on forever in
// approval and audit rows — which are exactly the screens someone reads after a
// deletion. Every function here answers for a code it has never seen instead of
// assuming the lookup succeeds; the tree used to throw outright on one.

import type { EnvTier, Environment } from '@/types'

/**
 * i18n keys for the five built-in tiers, so they stay translated.
 *
 * The tier is what says which KIND of environment an instance sits in — an
 * environment's name never does. Two of these were mislabelled until 2026-08-06
 * (gli as 灰度 rather than 法务, staging as 演练UAT rather than 预发布), so the
 * strings behind these keys are worth reading before reusing them anywhere.
 */
export const BUILTIN_TIER_LABEL: Record<string, string> = {
  prod: 'envProd', gli: 'envGli', staging: 'envStaging', uat: 'envUat', dev: 'envDev',
}
/** Colour per built-in tier — the palette operators already read at a glance. */
export const BUILTIN_DOT: Record<string, string> = {
  prod: 'danger', gli: 'info', staging: 'warning', dev: 'success',
}

/** Neutral colour for a code that resolves to no tier. */
export const UNKNOWN_DOT = 'muted'

type Translate = (key: string) => string

/**
 * Display name for a tier code.
 *
 * Built-ins go through i18n so they stay translated. A tier an administrator
 * created can only show the name that was typed — it has no translations, which
 * is the accepted trade-off of making tiers data. An unknown code shows itself:
 * for a deleted tier that is the only true thing left to say about it.
 */
export function tierLabel(code: string, tiers: EnvTier[], t: Translate): string {
  if (BUILTIN_TIER_LABEL[code]) return t(BUILTIN_TIER_LABEL[code])
  return tiers.find((x) => x.code === code)?.displayName || code
}

/** Display name for an environment code; unknown codes show verbatim. */
export function envLabel(code: string, environments: Environment[]): string {
  return environments.find((x) => x.code === code)?.displayName || code
}

/**
 * Colour for a tier code.
 *
 * A custom tier borrows the danger colour only when it actually carries
 * production-grade control, so the colour keeps meaning what it meant. An
 * unknown code is neutral rather than alarming or reassuring: claiming either
 * would be asserting a control level nobody can vouch for.
 */
export function dotFor(tierCode: string, tiers: EnvTier[]): string {
  if (BUILTIN_DOT[tierCode]) return BUILTIN_DOT[tierCode]
  const tier = tiers.find((x) => x.code === tierCode)
  if (!tier) return UNKNOWN_DOT
  return tier.dangerBanner || tier.requireMfa ? 'danger' : 'info'
}

/** The tier governing an environment, or undefined when it does not resolve. */
export function tierOf(envCode: string, tiers: EnvTier[], environments: Environment[]): EnvTier | undefined {
  const env = environments.find((x) => x.code === envCode)
  return env ? tiers.find((x) => x.code === env.tierCode) : undefined
}

/** Colour for an environment, taken from the tier that governs it. */
export function dotForEnv(envCode: string, tiers: EnvTier[], environments: Environment[]): string {
  const tier = tierOf(envCode, tiers, environments)
  return tier ? dotFor(tier.code, tiers) : UNKNOWN_DOT
}

/** Environments grouped under their tier, both keeping their list order. */
export function groupByTier(tiers: EnvTier[], environments: Environment[]): Record<string, Environment[]> {
  const out: Record<string, Environment[]> = {}
  for (const tier of tiers) out[tier.code] = []
  for (const env of environments) (out[env.tierCode] ||= []).push(env)
  return out
}
