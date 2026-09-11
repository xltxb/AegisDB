import { test, expect } from '@playwright/test'

import {
  BUILTIN_TIER_LABEL, dotFor, dotForEnv, envLabel, groupByTier, tierLabel, tierOf, UNKNOWN_DOT,
} from '../../src/lib/envTierLabels'
import type { EnvTier, Environment } from '../../src/types'

// Tiers and environments are rows an administrator can delete, and their codes
// survive in every approval and audit row that referenced them. Those screens are
// exactly where someone looks after a deletion, so resolving a code that is no
// longer in the list is the normal case here, not an edge case — the tree used to
// throw outright on one.
//
// ─────────────────────────────────────────────────────────────────────────────
// 这组用例对着 **React 版 `src/lib/envTierLabels.ts` 现在的实现**写,不是照搬 Vue 的。
// 两边已经分岔,而且分岔的是同一个函数:
//
//   - Vue 侧修过 #41:`tierLabel` **先看库里存的那串字**,只有它还是出厂原名才翻译,
//     被管理员改过就显示他改的那串。
//   - React 侧(这里)还是 #41 之前的写法:**内置 code 一律走 i18n**,`displayName`
//     只对自建分层生效。
//   - 而页面上实际用得更多的 `src/hooks/useEnvTier.ts` 又是第三种:直接读
//     `displayName`,**从不翻译**。
//
// 三种行为共存,报告里已单列。这里只钉住**当前这一份的真实行为**,好让任何一次统一
// 都必须显式地改测试、而不是悄悄改掉语义。
// ─────────────────────────────────────────────────────────────────────────────

const tier = (over: Partial<EnvTier>): EnvTier => ({
  code: 'x', displayName: 'X', sortOrder: 0,
  requireMfa: false, dangerBanner: false, countsInPending: false, scanBaseline: false,
  strictNoWhere: false, connLayer: '', defaultRole: 'dba_l2', ...over,
} as EnvTier)

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
] as Environment[]

const t = (k: string) => `i18n:${k}`

// 出厂的五个分层按读者的语言说;自建的只能显示当初键入的那串 —— 这是把分层做成数据
// 要付的代价。
test('内置分层按语言翻译,自建分层显示存下来的名字', () => {
  expect(tierLabel('prod', TIERS, t)).toBe('i18n:envProd')
  expect(tierLabel('prod-hk', TIERS, t)).toBe('香港生产')
})

// 分层列表还没加载完时,内置 code 仍按译文说 —— 那一瞬间显示 'prod' 三个字母比显示
// 译文更没用。
test('列表还没到手时,内置 code 退回译文', () => {
  expect(tierLabel('prod', [], t)).toBe('i18n:envProd')
})

// #41 的回归。修之前这里钉的是**旧行为**(内置 code 一律取译文),那条用例在修好的
// 那一刻变红,改的人必须承认自己在改语义 —— 它做到了这件事,现在换成钉住修好之后
// 的行为。
test('管理员给内置分层改了名,就显示他改的名字', () => {
  const renamed = [...TIERS, tier({ code: 'staging', displayName: '预发布-A', sortOrder: 4 })]
  expect(tierLabel('staging', renamed, t)).toBe('预发布-A')
  // 没改过的不受影响。
  expect(tierLabel('prod', renamed, t)).toBe('i18n:envProd')
})

// 2026-08-06 之前错标的那几个名字同样是本产品写下的,同样该翻译 —— 否则升级过的库
// 显示译文、没升级的显示旧中文,同一个分层在两套库上长得不一样。
test('历史上错标的出厂名也走翻译', () => {
  const old = TIERS.map((x) => (x.code === 'dev' ? { ...x, displayName: '测试 · DEV' } : x))
  expect(tierLabel('dev', old, t)).toBe('i18n:envDev')
})

// 旧写法的第二个代价:它**会遮住数据问题**。dev 那行的 display_name 曾被写成乱码
// `???? <fffd> DEV`,而界面照样显示「开发环境」,没有任何人看得见。现在必须如实露出来。
test('库里的名字坏了就如实显示,不拿译文盖住', () => {
  const broken = TIERS.map((x) => (x.code === 'dev' ? { ...x, displayName: '???? \uFFFD DEV' } : x))
  expect(tierLabel('dev', broken, t)).toBe('???? \uFFFD DEV')
})

test('五个内置分层都有文案键', () => {
  expect(Object.keys(BUILTIN_TIER_LABEL).sort()).toEqual(['dev', 'gli', 'prod', 'staging', 'uat'])
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
  const orphan = [{ code: 'ghost', displayName: 'Ghost', tierCode: 'gone', sortOrder: 0 }] as Environment[]
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
