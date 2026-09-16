import type { Page } from '@playwright/test'

// Shared scaffolding for the e2e specs. Every spec seeds a session and stubs the
// APIs its screen touches, so none of them need the Go backend running — the
// behaviours under test are about layout and the cascade, not about the server.

/** The gateway's response envelope: every handler returns `{code, msg, data}`. */
export const envelope = (data: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify({ code: 0, msg: 'ok', data }),
})

/** A non-zero code — the shape the UI must read a server-side refusal out of. */
export const failure = (code: number, msg: string) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify({ code, msg, data: null }),
})

export const ALL_MENUS = {
  terminal: true, export: true, approve: true, db: true, rules: true,
  envtier: true, perms: true, audit: true, settings: true,
  pipeline: true, execwindow: true,
}

/**
 * An admin signed in at the back-office portal.
 *
 * `roleCodes` must contain `admin` and the portal must be `backend`, or the
 * `adminOnly` routes bounce to the first visible front-office page — see
 * `router/guards.ts`.
 */
export const ADMIN = {
  id: 1, name: 'Lin Wei', email: 'linwei@vela.io', initials: 'LW',
  roleId: 1, roleCode: 'admin', roleName: 'Admin', layer: 'core',
  roleIds: [1], roleNames: ['Admin'], roleCodes: ['admin'],
  canApprove: true, mfaEnabled: false,
  menus: ALL_MENUS,
  capabilities: {},
}

/**
 * Put a token and a portal in localStorage before the app boots.
 *
 * The portal matters: the guards check it *and* the role, so a session seeded
 * without it lands on the front office and every back-office spec times out
 * looking for a page it was redirected away from.
 */
export async function seedSession(page: Page, portal: 'backend' | 'frontend' = 'backend') {
  await page.addInitScript((p) => {
    localStorage.setItem('aegis_token', 'e2e-token')
    localStorage.setItem('aegis_portal', p)
  }, portal)
}

/**
 * Stub the calls the app shell makes on every authenticated page (identity, the
 * notification bell, the gateway health pill), so a spec only has to describe
 * the APIs of the screen it actually cares about.
 */
export async function stubShell(page: Page, me: unknown = ADMIN) {
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/notifications**', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/gateway/stats**', (r) =>
    r.fulfill(envelope({ online: true, p50Ms: 3, samples: 1, intercepts: 0 })))
  // The sidebar's pending-work badges and the shell's settings read. They fire on
  // every page, so leaving them to hit the dev proxy makes each spec depend on
  // how the app copes with a 500 from an API it is not testing.
  await page.route('**/api/v1/approvals?**', (r) =>
    r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 1 })))
  await page.route('**/api/v1/settings', (r) => r.fulfill(envelope({})))
}
