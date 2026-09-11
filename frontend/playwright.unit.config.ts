import { defineConfig } from '@playwright/test'

// Unit-test seam for the pure logic in src/lib/* — and for the two structural
// rules (autofocus, modal roots) that are about the source tree rather than a
// running app.
//
// There is no browser here and no dev server: the Playwright runner simply
// executes TypeScript in Node. Using the runner rather than adding Vitest keeps
// this seam to one devDependency, and it is the same runner the Vue
// implementation's 218 tests ran on, so the ported specs needed no rewriting.
//
// It exists because the logic these modules hold — control-character handling in
// rendered results, the line editor's busy/queue state machine, the import
// sheet's validation — is the most defect-prone code in the front end and had no
// way to be regression-tested at all after the React rewrite.
export default defineConfig({
  testDir: './tests/unit',
  // Not the app tsconfig: it maps `@/locales` to a test double so the runner
  // does not execute the i18n singleton's side effects. See
  // tests/unit/stubs/locales.ts.
  tsconfig: './tsconfig.unit.json',
  fullyParallel: true,
  reporter: 'list',
})
