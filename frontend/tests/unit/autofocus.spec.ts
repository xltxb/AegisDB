import { test, expect } from '@playwright/test'
import fs from 'fs'
import path from 'path'

import { vAutofocus } from '../../src/directives/autofocus'

// A modal that pops up in response to something the user did — the PROD MFA
// step-up above all — is asking a question. The caret must already be in the
// answer box: the user just pressed Enter on a statement, their hands are on the
// keyboard, and the six digits they type next have to land somewhere. They were
// landing in the xterm canvas behind the dialog instead.
test('the directive focuses the element it is bound to', () => {
  let focused = 0
  const el = { focus: () => { focused++ } }
  vAutofocus.mounted(el as any)
  expect(focused).toBe(1)
})

// Bound to a plain <div> or a component root without focus(), it must do nothing
// rather than throw — a directive that can break a whole modal render is worse
// than a missing caret.
test('the directive tolerates an element that cannot take focus', () => {
  expect(() => vAutofocus.mounted({} as any)).not.toThrow()
})

function vueFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return vueFiles(p)
    return e.name.endsWith('.vue') ? [p] : []
  })
}

// Every one-time-code box in the app is, by definition, the thing the user is
// being asked for. There is no such box that should open unfocused, so this is
// checkable as a rule rather than one dialog at a time.
//
// Either mechanism satisfies it: the directive, or an explicit focus() on a ref
// bound to that input (which is what the login form already did, and which also
// re-focuses after a rejected code — the directive cannot, since the input never
// unmounts on a retry).
test('every one-time-code input opens focused', () => {
  const offenders: string[] = []
  for (const file of [...vueFiles('src/components'), ...vueFiles('src/views')]) {
    const src = fs.readFileSync(file, 'utf8')
    // Each <input ...> tag that asks for a one-time code.
    for (const m of src.matchAll(/<input\b[^>]*>/gs)) {
      const tag = m[0]
      if (!tag.includes('one-time-code')) continue
      if (/\bv-autofocus\b/.test(tag)) continue
      const ref = /\bref="([^"]+)"/.exec(tag)
      if (ref && src.includes(`${ref[1]}.value?.focus()`)) continue
      offenders.push(`${file} → ${tag.replace(/\s+/g, ' ').slice(0, 60)}…`)
    }
  }
  expect(offenders, 'add v-autofocus to these inputs').toEqual([])
})
