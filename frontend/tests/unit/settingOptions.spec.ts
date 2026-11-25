import { test, expect } from '@playwright/test'

import { APPROVAL_TIMEOUT_KEYS, SESSION_TTL_KEYS, labelOf, keyOf, keyForLabel } from '../../src/lib/settingOptions'

// Two "languages". Like vue-i18n, these are keyed by MESSAGE ID, not by the
// stored value — the indirection under test.
const zh: Record<string, string> = {
  autoReject: '超时自动驳回',
  autoEscalate: '超时自动升级',
  keepWaiting: '一直等待',
  ttl4: '4 小时',
  ttl8: '8 小时',
  ttl24: '24 小时',
}
const en: Record<string, string> = {
  autoReject: 'Auto reject',
  autoEscalate: 'Auto escalate',
  keepWaiting: 'Keep waiting',
  ttl4: '4 hours',
  ttl8: '8 hours',
  ttl24: '24 hours',
}
const MSG: Record<string, string> = {
  'auto-reject': 'autoReject',
  'auto-escalate': 'autoEscalate',
  'keep-waiting': 'keepWaiting',
  '4h': 'ttl4',
  '8h': 'ttl8',
  '24h': 'ttl24',
}

// EF11: the settings page held the TRANSLATED LABEL in its model and recovered
// the stored value by comparing against t() at save time. Switching language
// left the model holding text from the previous language, so every comparison
// missed and both settings silently fell back to their defaults — an operator
// who switched language and pressed Save widened the approval timeout to
// auto-escalate and the session lifetime to 8h without being told. The stored
// value has to survive a language change.
test('a stored value survives a language switch', () => {
  for (const key of APPROVAL_TIMEOUT_KEYS) {
    const shown = labelOf(key, (id) => zh[id])
    expect(shown).toBe(zh[MSG[key]])
    // The user switches language; the model still holds the KEY, so reading it
    // back under the new language yields the same value.
    expect(keyOf(key, APPROVAL_TIMEOUT_KEYS, 'auto-escalate')).toBe(key)
    expect(labelOf(key, (id) => en[id])).toBe(en[MSG[key]])
  }
  for (const key of SESSION_TTL_KEYS) {
    expect(keyOf(key, SESSION_TTL_KEYS, '8h')).toBe(key)
    expect(labelOf(key, (id) => en[id])).toBe(en[MSG[key]])
  }
})

test('an unrecognised value falls back to the documented default', () => {
  expect(keyOf('something-else', APPROVAL_TIMEOUT_KEYS, 'auto-escalate')).toBe('auto-escalate')
  expect(keyOf('', SESSION_TTL_KEYS, '8h')).toBe('8h')
})

// The dropdown must offer every key, so nothing configurable becomes unreachable.
test('the option lists cover the values the backend accepts', () => {
  expect([...APPROVAL_TIMEOUT_KEYS].sort()).toEqual(['auto-escalate', 'auto-reject', 'keep-waiting'])
  expect([...SESSION_TTL_KEYS].sort()).toEqual(['24h', '4h', '8h'])
})

// The dropdown hands back the LABEL the user picked, so the view needs the
// reverse mapping to store the key again. It resolves against the language in
// use at that moment, which is the language the label was just rendered in.
test('a picked label maps back to its key', () => {
  expect(keyForLabel(zh['autoReject'], APPROVAL_TIMEOUT_KEYS, (id) => zh[id], 'auto-escalate')).toBe('auto-reject')
  expect(keyForLabel(en['keepWaiting'], APPROVAL_TIMEOUT_KEYS, (id) => en[id], 'auto-escalate')).toBe('keep-waiting')
  expect(keyForLabel(en['ttl24'], SESSION_TTL_KEYS, (id) => en[id], '8h')).toBe('24h')
  // Unknown text keeps the current default rather than inventing a value.
  expect(keyForLabel('mystery', SESSION_TTL_KEYS, (id) => en[id], '8h')).toBe('8h')
})
