// Terminal snippets — saved scripts bound to hotkeys 1-9.
//
// The pieces here are deliberately pure so they can be tested without a
// terminal, and small so the interesting part stays visible: a hotkey does NOT
// execute anything. It types. The snippet's text is fed to the line editor
// exactly as if the operator had pasted it, which means it goes through
// handleSubmit → /risk/check → the approval and MFA prompts like everything
// else. The capability matrix, the high-risk dictionary and strict mode all
// judge it against the instance it is aimed at, at the moment it fires.
//
// That ordering is the whole safety story of the feature. A snippet written
// against dev and fired on prod out of muscle memory is judged as a prod
// statement, because nothing about the verdict was decided when it was saved.

/** Hotkeys are Alt+1 … Alt+9. */
export const SNIPPET_SLOTS = [1, 2, 3, 4, 5, 6, 7, 8, 9] as const

/**
 * The hotkey slot a keydown asks for, or null if it isn't one of ours.
 *
 * Alt+digit rather than Ctrl+digit: Ctrl+1…9 switches browser tabs in Chrome and
 * Firefox and cannot be cancelled from the page, so half the keys would silently
 * do something else. Alt+digit is unclaimed in both on Windows and Linux.
 *
 * Matching is on `e.code` (the physical key) rather than `e.key`, because on
 * macOS Option+1 produces `¡` — keying off the character would leave the
 * shortcut working on some layouts and not others.
 */
export function slotFromEvent(e: KeyboardEvent): number | null {
  if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return null
  const m = /^(?:Digit|Numpad)([1-9])$/.exec(e.code || '')
  return m ? Number(m[1]) : null
}

/** The label shown next to a snippet in the list and the picker. */
export function slotLabel(slot: number): string {
  return slot > 0 ? `Alt+${slot}` : ''
}

/**
 * Turn a snippet body into the exact keystrokes to feed the line editor.
 *
 * The editor submits when the accumulated text ends with `;`, is a backslash
 * meta-command, or ends with the MySQL display terminators \g / \G. A body that
 * ends without one of those would leave the terminal parked at a continuation
 * prompt with the statement typed but never run — which reads as the hotkey
 * having done nothing, right up until the next Enter runs it. So a terminator is
 * supplied when the body lacks one.
 *
 * It goes on its own line when the last line carries a `--` comment: appended
 * inline it would land INSIDE the comment, where it terminates nothing and the
 * statement still hangs.
 *
 * The trailing newline is what submits. Everything after the first statement
 * rides along and the editor runs it one at a time, exactly as for a paste.
 */
export function snippetSubmitText(body: string): string {
  const text = body.replace(/\r\n?/g, '\n').replace(/[\s]+$/, '')
  if (!text) return ''
  const needsTerminator = !(text.endsWith(';') || /\\[gG]$/.test(text) || text.startsWith('\\'))
  if (!needsTerminator) return text + '\n'
  const lastLine = text.slice(text.lastIndexOf('\n') + 1)
  return text + (lastLine.includes('--') ? '\n;\n' : ';\n')
}

/**
 * Whether firing a hotkey on this tier should take two presses.
 *
 * A hotkey is fast and blind: the key that is routine on dev is one keystroke
 * away on prod, and muscle memory does not read the prompt. On a tier carrying
 * the danger banner the first press only says what would run and where; the
 * second runs it.
 *
 * An UNRESOLVED tier arms too, matching the caution banner: no tier means the
 * instance's control level is unknown, not that it is safe. Getting this
 * backwards would put the least confirmation on exactly the instances nobody has
 * classified yet.
 *
 * None of this is the safety net — the gateway is, and it judges the statement
 * either way. This is for the case the gateway is right to allow: a snippet that
 * is perfectly legal on production and simply wasn't meant for production.
 */
export function needsArming(tier: { dangerBanner?: boolean } | undefined | null): boolean {
  return !tier || !!tier.dangerBanner
}

/** A one-line preview of a snippet body, for the picker and the arming notice. */
export function snippetPreview(body: string, max = 60): string {
  const one = body.replace(/\s+/g, ' ').trim()
  return one.length > max ? one.slice(0, max - 1) + '…' : one
}
