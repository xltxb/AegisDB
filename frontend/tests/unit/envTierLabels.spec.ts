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

// 出厂那串名字按读者的语言说;管理员改过的、以及他自己建的,只能显示键入的那串 ——
// 这是把分层做成数据要付的代价。
const t = (k: string) => `i18n:${k}`

test('出厂名按语言翻译,自建分层显示存下来的名字', () => {
  expect(tierLabel('prod', TIERS, t)).toBe('i18n:envProd')   // 还是出厂那串
  expect(tierLabel('prod-hk', TIERS, t)).toBe('香港生产')      // 自建的
})

// 这条是 #41 的回归。改名之前它断言的正好是错行为:内置 code 一律取 i18n,于是
// 管理员改的名字在页面上永远不出现,而后端还特意保护了那次改名。
test('管理员给内置分层改了名,就显示他改的名字', () => {
  // 夹具里没有 staging,补一行进去 —— 它是内置 code,正是这条规则要管的那种。
  const renamed = [...TIERS, tier({ code: 'staging', displayName: '预发布-A', sortOrder: 4 })]
  expect(tierLabel('staging', renamed, t)).toBe('预发布-A')
  // 没改过的那些不受影响。
  expect(tierLabel('prod', renamed, t)).toBe('i18n:envProd')
})

// 2026-08-06 之前错标的两个名字同样是本产品写下的,同样该翻译 —— 否则升级过的库
// 显示译文、没升级的显示旧中文,同一个分层在两套库上长得不一样。
test('历史上错标的出厂名也走翻译', () => {
  const old = TIERS.map((x) => (x.code === 'dev' ? { ...x, displayName: '测试 · DEV' } : x))
  expect(tierLabel('dev', old, t)).toBe('i18n:envDev')
})

// 这一条是那次数据损坏的回归:dev 的 display_name 曾被写成乱码,而旧实现照样显示
// 「开发环境」,把问题遮了。现在它必须如实露出来。
test('库里的名字坏了就如实显示,不拿译文盖住', () => {
  const broken = TIERS.map((x) => (x.code === 'dev' ? { ...x, displayName: '???? \uFFFD DEV' } : x))
  expect(tierLabel('dev', broken, t)).toBe('???? \uFFFD DEV')
})

// 分层列表还没加载完时,内置 code 仍按译文说 —— 那一瞬间显示 'prod' 三个字母
// 比显示译文更没用。
test('列表还没到手时,内置 code 退回译文', () => {
  expect(tierLabel('prod', [], t)).toBe('i18n:envProd')
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
