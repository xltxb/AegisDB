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

/**
 * 出厂时这个产品给每个内置分层写下过的名字 —— 当前的,以及历史上错过的那三个。
 *
 * 它是判断「这个名字是不是我们自己写的」的依据,与后端 `wrongBuiltinNames`
 * 同源(bootstrap/seed.go):后端用它决定要不要覆盖一行,前端用它决定要不要翻译
 * 一行。两边问的是同一个问题。
 *
 * 键是存进库的那串中文,不是 code —— **用户改过名之后自然不再匹配**,于是显示他
 * 自己的名字,不需要另记一个"改过没有"的标志位。`builtinNames.ts` 早就这么做了。
 */
export const SHIPPED_TIER_NAME: Record<string, string> = {
  '生产环境 · PROD': 'envProd',
  '法务环境 · GLI': 'envGli',
  '预发布环境 · STAGING': 'envStaging',
  '演练环境 · UAT': 'envUat',
  '开发环境 · DEV': 'envDev',
  // 2026-08-06 之前错标的三个,同样是本产品写下的,同样该翻译。
  '灰度 · GLI': 'envGli',
  '演练UAT · STAGING': 'envStaging',
  '测试 · DEV': 'envDev',
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
 * 顺序是:**先看库里存的那串字,再决定要不要翻译**。
 *
 * 原先是反过来的 —— 内置 code 一律走 i18n、从不读 `displayName`。那样管理员改了名
 * 页面永远显示译文,而后端特意用 `wrongBuiltinNames` 只覆盖"本产品自己写下的"名字
 * 来保护这次改名;更糟的是它**会遮住数据问题**:dev 那行的 display_name 曾被写成
 * 乱码,而界面照样显示「开发环境」,没人看得见。见 GitHub issue #41。
 *
 * 未知 code 显示自身 —— 对一个已被删除的分层,那是唯一还成立的话。
 */
export function tierLabel(code: string, tiers: EnvTier[], t: Translate): string {
  const stored = tiers.find((x) => x.code === code)?.displayName?.trim()
  if (!stored) {
    // 查不到这一行:内置 code 仍按译文说(分层列表还没加载完时走这条),否则显示 code。
    return BUILTIN_TIER_LABEL[code] ? t(BUILTIN_TIER_LABEL[code]) : code
  }
  const shipped = SHIPPED_TIER_NAME[stored]
  return shipped ? t(shipped) : stored
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
