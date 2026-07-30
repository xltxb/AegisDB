import { defineConfig } from '@playwright/test'

// Unit-test seam for the terminal's pure logic modules (src/lib/*).
//
// The browser-driving suite lives in playwright.config.ts and boots a Vite dev
// server; these tests need neither, so they get their own config: no webServer,
// no browser project, just the Playwright runner executing TypeScript in Node.
// Using the runner already in the repo keeps this seam dependency-free.
//
// It exists because the terminal's most defect-prone logic — control-character
// handling in rendered results, and the line editor's busy/queue state machine —
// had no way to be regression-tested at all.
export default defineConfig({
  testDir: './tests/unit',
  fullyParallel: true,
  reporter: 'list',
})
