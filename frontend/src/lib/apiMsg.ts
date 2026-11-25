// apiMsg — render an API error's `msg` in the reader's language.
//
// Every business error arrives as `{ code, msg }` where `msg` is a Chinese
// sentence assembled in Go. It stays that way: the open API's callers, the logs
// and the Chinese console all read it, and it must not change underneath them.
// Alongside it the server now sends `msgRef` — the same sentence as a code plus
// its positional arguments (backend `internal/apimsg`) — and this renders it
// through the `apiMsg.` catalogue.
//
// The code is a hash of the Chinese sentence, so the catalogue keys are opaque;
// both catalogue blocks are generated from the Go sources (`go generate
// ./internal/apimsg`) and the backend tests check that every sentence has an
// English entry with the same placeholders.
//
// Two kinds of nested part fill a placeholder (`args._slot` says which one):
// another catalogued sentence, rendered recursively, or `raw` — text that is
// never translated (a driver's own error, an uncatalogued message).
//
// Fallback is all-or-nothing, as for rlLog: if this build does not know any code
// in the tree (a newer backend), the whole sentence stays the server's Chinese
// rather than an English sentence with a key name in it.

import type { RuleRef } from '@/types'

export interface ApiMsgI18n {
  t: (key: string, list: unknown[]) => string
  te: (key: string) => boolean
}

const RAW = 'raw'
const SLOT = '_slot'

/** apiMsg 的 code 形如 m + 8 位十六进制(中文原句的哈希),另有 raw。 */
export function isApiMsgCode(code: string | undefined): boolean {
  return !!code && (code === RAW || /^m[0-9a-f]{8}$/.test(code))
}

/**
 * Renders a part that is not an apiMsg sentence — e.g. a terminal output line
 * (`outText.`) nested in "failed after 3/5 statements · statement 4: <output>".
 * Returns '' when it does not know the code either.
 */
export type ForeignRender = (ref: RuleRef) => string

function render(ref: RuleRef, i18n: ApiMsgI18n, other?: ForeignRender): string | null {
  if (ref.code === RAW) return ref.args?.text ?? ''
  const key = 'apiMsg.' + ref.code
  if (!i18n.te(key)) {
    if (isApiMsgCode(ref.code) || !other) return null
    return other(ref) || null
  }
  const list: string[] = []
  for (const [k, v] of Object.entries(ref.args || {})) {
    if (/^\d+$/.test(k)) list[Number(k)] = v
  }
  for (const p of ref.parts || []) {
    const slot = p.args?.[SLOT]
    if (slot === undefined) continue
    const s = render(p, i18n, other)
    if (s === null) return null
    list[Number(slot)] = s
  }
  return i18n.t(key, list)
}

/** `canonical` (the server's Chinese msg) unless `ref` renders completely. */
export function renderApiMsg(ref: RuleRef | null | undefined, canonical: string, i18n: ApiMsgI18n, other?: ForeignRender): string {
  if (!ref || !ref.code) return canonical
  return render(ref, i18n, other) ?? canonical
}

/** Like renderApiMsg but '' when it cannot render (for callers with their own fallback). */
export function tryRenderApiMsg(ref: RuleRef, i18n: ApiMsgI18n, other?: ForeignRender): string {
  return render(ref, i18n, other) ?? ''
}

/** Stored refs (notification body, task errors) arrive as a JSON string column. */
export function parseRef(json: string | null | undefined): RuleRef | null {
  if (!json) return null
  try {
    const r = JSON.parse(json)
    return r && typeof r.code === 'string' ? (r as RuleRef) : null
  } catch {
    return null
  }
}
