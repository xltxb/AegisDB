import { test, expect } from '@playwright/test'
import fs from 'fs'
import path from 'path'

// A modal that pops up in response to something the user did — the PROD MFA
// step-up above all — is asking a question. The caret must already be in the
// answer box: the user just pressed Enter on a statement, their hands are on the
// keyboard, and the six digits they type next have to land somewhere. They were
// landing in the xterm canvas behind the dialog instead.
function tsxFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return tsxFiles(p)
    return e.name.endsWith('.tsx') ? [p] : []
  })
}

// Every one-time-code box in the app is, by definition, the thing the user is
// being asked for. There is no such box that should open unfocused, so this is
// checkable as a rule rather than one dialog at a time.
//
// Either mechanism satisfies it: the autoFocus prop, or an explicit focus() on a
// ref bound to that input (which is what the login form already did, and which
// also re-focuses after a rejected code — autoFocus cannot, since the input never
// unmounts on a retry).
test('every one-time-code input opens focused', () => {
  const offenders: string[] = []
  for (const file of [...tsxFiles('src/components'), ...tsxFiles('src/views')]) {
    const src = fs.readFileSync(file, 'utf8')
    // Each <input ...> tag that asks for a one-time code. JSX attributes hold arrow
    // functions, so "up to the first >" would cut the tag short: read to the "/>".
    for (const m of src.matchAll(/<input\b[\s\S]*?\/>/g)) {
      const tag = m[0]
      if (!tag.includes('one-time-code')) continue
      if (/\bautoFocus\b/.test(tag)) continue
      const ref = /\bref=\{(\w+)\}/.exec(tag)
      if (ref && src.includes(`${ref[1]}.current?.focus()`)) continue
      offenders.push(`${file} → ${tag.replace(/\s+/g, ' ').slice(0, 60)}…`)
    }
  }
  expect(offenders, 'add autoFocus to these inputs').toEqual([])
})
