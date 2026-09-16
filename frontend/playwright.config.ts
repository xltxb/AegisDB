import { defineConfig, devices } from '@playwright/test'

// Playwright drives the real React app in a real browser — the behaviours that
// only exist once a browser has laid the page out and applied the cascade.
//
// It is a second seam alongside `playwright.unit.config.ts`, not a replacement:
// the unit config runs pure logic in Node with no DOM, this one runs the app.
// A grid that stacks instead of sitting side by side, a media query that fails
// to collapse an animation, a tree that blanks when one of its two data sources
// errors — none of those can be seen from Node.
//
// The Vite dev server is started automatically and every API call is stubbed by
// the specs themselves, so the Go backend does not need to be running.
const PORT = 5175

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: 'list',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `npm run dev -- --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
})
