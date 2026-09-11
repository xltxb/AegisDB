import { defineConfig, devices } from '@playwright/test'

// Playwright drives the real Vue app in a real browser — the PRD's "one e2e"
// seam. The Vite dev server is started automatically. Today this covers the
// US#57 reduced-motion guarantee; the full login→intercept→approve tracer
// (which also needs the Go backend running) plugs into this same harness.
const PORT = 5174

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
