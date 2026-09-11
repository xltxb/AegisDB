import { test, expect } from '@playwright/test'

import { renderRule } from '../../src/lib/ruleText'
import type { RuleRef } from '../../src/types'

// A stand-in for vue-i18n with just enough messages to exercise the renderer.
const MSGS: Record<string, string> = {
  'ruleText.capApprove': 'Capability matrix · approval required',
  'ruleText.dictDeny': 'High-risk dictionary · direct execution forbidden on {tier}',
  'ruleText.execWindow': 'Execution window “{window}” · allowed without approval (originally: {inner})',
  'ruleText.batchHit': 'Statement {pos} {command} · {inner}',
  'ruleText.batchMore': '… and {n} more matches',
}
const i18n = {
  te: (k: string) => k in MSGS,
  t: (k: string, named?: Record<string, unknown>) =>
    MSGS[k].replace(/\{(\w+)\}/g, (_, n) => String(named?.[n] ?? '')),
}

test('renders a rule in the reader’s language instead of the server’s', () => {
  const ref: RuleRef = { code: 'dictDeny', args: { tier: 'PROD' } }
  expect(renderRule(ref, '高危命令字典 · PROD 禁止直接执行', i18n))
    .toBe('High-risk dictionary · direct execution forbidden on PROD')
})

// The canonical string is what the audit chain stores, and it is the only thing
// a build too old to know a new code can show. Rendering the key name instead
// would put "ruleText.somethingNew" in front of an operator asking why their
// command was stopped.
test('an unknown code falls back to the server’s own string', () => {
  const ref: RuleRef = { code: 'inventedAfterThisBuildShipped' }
  expect(renderRule(ref, '某条新规则 · 需审批', i18n)).toBe('某条新规则 · 需审批')
})

test('no ref at all still shows the server’s string', () => {
  expect(renderRule(undefined, '高危命令字典 · 需审批', i18n)).toBe('高危命令字典 · 需审批')
})

// Both halves have to translate: an execution window that relaxed a dictionary
// hit reads "window X allowed this, originally <rule>" — leaving the inner rule
// in the server's language would put the Chinese right back in the middle.
test('a nested rule translates inside and out', () => {
  const ref: RuleRef = {
    code: 'execWindow',
    args: { window: '凌晨发车' },
    parts: [{ code: 'dictDeny', args: { tier: 'PROD' } }],
  }
  expect(renderRule(ref, 'zh fallback', i18n)).toBe(
    'Execution window “凌晨发车” · allowed without approval ' +
    '(originally: High-risk dictionary · direct execution forbidden on PROD)',
  )
})

// A batch is a list, not a sentence — it has no message of its own, so it must
// not be treated as an unknown code and collapse the whole thing to the fallback.
test('a batch joins its per-statement hits, each translated', () => {
  const ref: RuleRef = {
    code: 'batch',
    parts: [
      { code: 'batchHit', args: { pos: '2', command: 'DROP' }, parts: [{ code: 'capApprove' }] },
      { code: 'batchMore', args: { n: '3' } },
    ],
  }
  expect(renderRule(ref, 'zh fallback', i18n)).toBe(
    'Statement 2 DROP · Capability matrix · approval required + … and 3 more matches',
  )
})

// A batch whose statements all carry codes this build does not know renders
// nothing — and must then fall back rather than printing an empty rule name.
test('a batch that renders to nothing falls back to the server’s string', () => {
  const ref: RuleRef = { code: 'batch', parts: [{ code: 'unknownX' }, { code: 'unknownY' }] }
  expect(renderRule(ref, '第1条 · 某新规则', i18n)).toBe('第1条 · 某新规则')
})
