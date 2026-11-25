import { test, expect } from '@playwright/test'
import fs from 'fs'
import path from 'path'

// Scoped styles (build/scopedCss.ts) put the PARENT component's scope attribute on
// a child component's ROOT element when the child spreads its props there. So when a modal's root carries a generic class like `overlay`, any
// parent that styles `.overlay` for its own dialogs also matches that root — and
// can override it.
//
// That is exactly how the tag picker broke: opened from inside the user card, it
// inherited the page's `z-index: 50` instead of its own, and because it sits
// earlier in the DOM than the card, equal stacking put it UNDERNEATH. It rendered
// correctly every time and was simply invisible — no error, no console warning,
// nothing to notice. A component root must therefore not use a class name its
// parents are likely to style.
const GENERIC_ROOT_CLASSES = ['overlay', 'mask', 'modal', 'card', 'page', 'head', 'body', 'foot']

function tsxFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return tsxFiles(p)
    return e.name.endsWith('.tsx') ? [p] : []
  })
}

/** The class lists on every component's root element in the file. */
function rootClasses(src: string): string[] {
  const out: string[] = []
  for (const m of src.matchAll(/\breturn (?:\(\s*)?(?:<>\s*)?<[a-z][\w-]*\b([\s\S]*?)>/g)) {
    const cls = /\bclassName=(?:"([^"]+)"|\{clsx\('([^']+)')/.exec(m[1])
    if (cls) out.push(...(cls[1] ?? cls[2]).split(/\s+/).filter(Boolean))
  }
  return out
}

test('no reusable component roots itself on a class its parents may style', () => {
  const offenders: string[] = []
  for (const file of tsxFiles('src/components')) {
    for (const cls of rootClasses(fs.readFileSync(file, 'utf8'))) {
      if (GENERIC_ROOT_CLASSES.includes(cls)) {
        offenders.push(`${file} → class="${cls}"`)
      }
    }
  }
  expect(offenders, 'give these roots a component-specific class').toEqual([])
})
