import type { Directive } from 'vue'

/**
 * `v-autofocus` — put the caret in this element as soon as it appears.
 *
 * Written for dialogs that interrupt the user mid-keystroke. The PROD MFA
 * step-up is the sharp case: the user presses Enter on a statement, the prompt
 * opens over the terminal, and the six digits they type next go to whatever
 * still holds focus — the xterm canvas behind the dialog. Nothing looks broken;
 * the code simply never arrives in the box.
 *
 * A modal's input only exists while the modal is open (`v-if`), so `mounted` is
 * exactly the moment it becomes focusable. The plain HTML `autofocus` attribute
 * is not a substitute: browsers honour it on page load, not reliably on an
 * element inserted later.
 */
export const vAutofocus: Directive<HTMLElement> = {
  mounted(el) {
    // Bound to a component root or a non-focusable element, do nothing rather
    // than throw — a directive must never be able to break a modal's render.
    ;(el as any)?.focus?.()
  },
}

export default vAutofocus
