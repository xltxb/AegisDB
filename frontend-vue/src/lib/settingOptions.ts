// settingOptions — stable keys for the settings dropdowns.
//
// The settings page previously held the TRANSLATED LABEL in its model and
// recovered the stored value at save time by comparing the model against t().
// Switching language left the model holding text from the previous language, so
// every comparison missed and both settings silently fell back to their
// defaults: an operator who switched language and pressed Save widened the
// approval timeout to auto-escalate and the session lifetime to 8h, with no
// indication anything had changed (EF11).
//
// The model therefore stores the KEY — which is also what the backend stores —
// and the label is derived for display only.

/** Values `approval.onTimeout` accepts. */
export const APPROVAL_TIMEOUT_KEYS = ['auto-reject', 'auto-escalate', 'keep-waiting'] as const
/** Values `security.sessionTTL` accepts. */
export const SESSION_TTL_KEYS = ['8h', '4h', '24h'] as const

export type ApprovalTimeoutKey = (typeof APPROVAL_TIMEOUT_KEYS)[number]
export type SessionTTLKey = (typeof SESSION_TTL_KEYS)[number]

/** i18n message ids, one per key. */
const MESSAGE_ID: Record<string, string> = {
  'auto-reject': 'autoReject',
  'auto-escalate': 'autoEscalate',
  'keep-waiting': 'keepWaiting',
  '4h': 'ttl4',
  '8h': 'ttl8',
  '24h': 'ttl24',
}

/** labelOf renders a key for display in the current language. */
export function labelOf(key: string, t: (id: string) => string): string {
  return t(MESSAGE_ID[key] ?? key)
}

/** keyOf validates a stored value against the allowed set, falling back to the
 *  documented default when the backend holds something unexpected (or nothing). */
export function keyOf<T extends string>(value: string, allowed: readonly T[], fallback: T): T {
  return (allowed as readonly string[]).includes(value) ? (value as T) : fallback
}

/** keyForLabel is the inverse of labelOf: a select hands back the label the user
 *  picked, and the view must store the key again. Resolved against the language
 *  currently in use — the one the label was just rendered in — so this never
 *  depends on an earlier language the way the old comparisons did. */
export function keyForLabel<T extends string>(
  label: string,
  allowed: readonly T[],
  t: (id: string) => string,
  fallback: T,
): T {
  return allowed.find((k) => labelOf(k, t) === label) ?? fallback
}
