import { test, expect, type Page } from '@playwright/test'

// Convert a CSS <time> ("1s", "0.0001s", "10ms") to seconds.
function seconds(dur: string): number {
  const first = dur.split(',')[0].trim()
  return first.endsWith('ms') ? parseFloat(first) / 1000 : parseFloat(first)
}

// Load the app and return the computed animation-duration of the global
// `.spin` loader rule (the infinite spinner used across the UI).
async function spinDuration(page: Page): Promise<string> {
  await page.goto('/')
  // Wait until the global stylesheet (motion tokens) has actually applied.
  await page.waitForFunction(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--dur-base').trim() !== '',
  )
  return page.evaluate(() => {
    const el = document.createElement('div')
    el.className = 'spin'
    document.body.appendChild(el)
    const d = getComputedStyle(el).animationDuration
    el.remove()
    return d
  })
}

// US#57: a user whose OS asks for reduced motion must see animations collapse to
// ~0 — verified in a real browser via Playwright's reduced-motion emulation.
test.describe('US#57 prefers-reduced-motion', () => {
  test('collapses the infinite .spin loader when reduce is preferred', async ({ browser }) => {
    const ctx = await browser.newContext({ reducedMotion: 'reduce' })
    const page = await ctx.newPage()
    const dur = await spinDuration(page)
    await ctx.close()
    expect(seconds(dur)).toBeLessThan(0.05)
  })

  test('keeps the animation running when no preference is set', async ({ browser }) => {
    const ctx = await browser.newContext({ reducedMotion: 'no-preference' })
    const page = await ctx.newPage()
    const dur = await spinDuration(page)
    await ctx.close()
    expect(seconds(dur)).toBeGreaterThan(0.5)
  })
})
