import { test, expect } from '@playwright/test'

import {
  dotFor, dotForEnv, envLabel, groupByTier, tierLabel, tierOf, UNKNOWN_DOT,
} from '../../src/lib/envTierLabels'
import type { EnvTier, Environment } from '../../src/types'

// Tiers and environments are rows an administrator can delete, and their codes
// survive in every approval and audit row that referenced them. Those screens are
// exactly where someone looks after a deletion, so resolving a code that is no
// longer in the list is the normal case here, not an edge case — the tree used to
// throw outright on one.

const tier = (over: Partial<EnvTier>): EnvTier => ({
  code: 'x', displayName: 'X', sortOrder: 0,
  requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false,
  connLayer: '', defaultRole: 'dba_l2', ...over,
})

const TIERS: EnvTier[] = [
  tier({ code: 'prod', displayName: '生产环境 · PROD', dangerBanner: true, requireMfa: true, scanBaseline: true }),
  tier({ code: 'dev', displayName: '测试 · DEV', sortOrder: 1 }),
  tier({ code: 'prod-hk', displayName: '香港生产', sortOrder: 2, requireMfa: true }),
  tier({ code: 'canary', displayName: '金丝雀', sortOrder: 3 }),
]

const ENVS: Environment[] = [
  { code: 'prod', displayName: '生产环境 · PROD', tierCode: 'prod', sortOrder: 0 },
  { code: 'dev', displayName: '测试 · DEV', tierCode: 'dev', sortOrder: 1 },
  { code: 'hk-1', displayName: '香港集群一', tierCode: 'prod-hk', sortOrder: 2 },
]

// i18n gives the built-ins their translations; an operator-created tier can only
// show what was typed, which is the accepted cost of tiers being data.
const t = (k: string) => `i18n:${k}`

test('built-in tiers keep their translations, custom ones use their stored name', () => {
  expect(tierLabel('prod', TIERS, t)).toBe('i18n:envProd')
  expect(tierLabel('prod-hk', TIERS, t)).toBe('香港生产')
})

test('a tier code that no longer exists shows itself rather than throwing', () => {
  expect(tierLabel('deleted-tier', TIERS, t)).toBe('deleted-tier')
  expect(tierLabel('deleted-tier', [], t)).toBe('deleted-tier')
})

test('an environment code that no longer exists shows itself', () => {
  expect(envLabel('hk-1', ENVS)).toBe('香港集群一')
  expect(envLabel('retired-cluster', ENVS)).toBe('retired-cluster')
})

// The colour is a claim about how dangerous the target is. A custom tier earns
// the danger colour by carrying production-grade control, not by its name.
test('colour follows the control flags, not the tier name', () => {
  expect(dotFor('prod', TIERS)).toBe('danger')       // built-in palette
  expect(dotFor('prod-hk', TIERS)).toBe('danger')    // custom, but forces MFA
  expect(dotFor('canary', TIERS)).toBe('info')       // custom, no control flags
})

// Neither alarming nor reassuring: no tier means no basis for either claim.
test('an unresolvable code is neutral, never coloured', () => {
  expect(dotFor('deleted-tier', TIERS)).toBe(UNKNOWN_DOT)
  expect(dotForEnv('retired-cluster', TIERS, ENVS)).toBe(UNKNOWN_DOT)
})

test('an environment resolves to its tier, and its colour comes from there', () => {
  expect(tierOf('hk-1', TIERS, ENVS)?.code).toBe('prod-hk')
  expect(dotForEnv('hk-1', TIERS, ENVS)).toBe('danger')
  expect(dotForEnv('dev', TIERS, ENVS)).toBe('success')
})

// An environment bound to a tier that was deleted out from under it must not
// resolve to some other tier — it resolves to nothing, and reads as unknown.
test('an environment pointing at a missing tier resolves to nothing', () => {
  const orphan: Environment[] = [{ code: 'ghost', displayName: 'Ghost', tierCode: 'gone', sortOrder: 0 }]
  expect(tierOf('ghost', TIERS, orphan)).toBeUndefined()
  expect(dotForEnv('ghost', TIERS, orphan)).toBe(UNKNOWN_DOT)
})

test('grouping keeps every tier, including ones holding no environments', () => {
  const grouped = groupByTier(TIERS, ENVS)
  expect(Object.keys(grouped)).toEqual(['prod', 'dev', 'prod-hk', 'canary'])
  expect(grouped['prod-hk'].map((e) => e.code)).toEqual(['hk-1'])
  // A tier with no environments still gets a bucket, so the tree's top level is
  // the full set of control levels rather than only the populated ones.
  expect(grouped['canary']).toEqual([])
})
