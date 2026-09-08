// ruleText — render a verdict's rule in the reader's language.
//
// The gateway assembles the rule name in Go ("高危命令字典 · PROD 禁止直接执行"),
// and that string is the canonical record: it is what the approval ticket and the
// audit chain store, and it must not change with whoever happens to be looking at
// it. But it was also the only thing the terminal had to print, so an English
// session showed one Chinese sentence in the middle of an English line.
//
// So the server now sends the same statement twice: the canonical string, and a
// `RuleRef` — a stable code plus its arguments — which this renders through i18n.
// Rules nest (a batch holds one ref per gated statement; an execution window
// holds the verdict it relaxed), so this recurses and both halves get translated.
//
// The fallback matters as much as the rendering: a ref whose code this build does
// not know renders as the server's string rather than as a key name. New codes
// ship with the backend, and a frontend one deploy behind must still be able to
// tell an operator why their command was stopped.

import type { RuleRef } from '@/types'

type Translate = (key: string, named?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

/** Separator between the per-statement hits of a batch — matches the server's. */
const BATCH_SEP = ' + '

export interface RuleI18n {
  t: Translate
  te: HasKey
}

/**
 * Render `ref` in the current locale, falling back to `canonical` (the server's
 * own string) when the ref is missing or names a code this build has no text for.
 */
export function renderRule(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n) || canonical || ''
}

function renderRef(ref: RuleRef | undefined | null, i18n: RuleI18n): string {
  if (!ref || !ref.code) return ''

  // A batch is a list, not a sentence: it has no text of its own, only its parts.
  if (ref.code === 'batch') {
    const parts = (ref.parts || []).map((p) => renderRef(p, i18n)).filter(Boolean)
    return parts.join(BATCH_SEP)
  }

  const key = 'ruleText.' + ref.code
  if (!i18n.te(key)) return '' // unknown code → caller falls back to the canonical string

  // `inner` is the nested rule — the statement's own rule inside a batch hit, the
  // pre-relaxation verdict inside an execution window. Rendered first so the
  // outer message can interpolate it.
  const inner = ref.parts && ref.parts.length ? renderRef(ref.parts[0], i18n) : ''
  return i18n.t(key, { ...(ref.args || {}), inner })
}
