import { test, expect, type Page } from '@playwright/test'

// 发布页「我的 / 全部」:露不露切换、默认在哪一边,都只看后端给的 canSeeAllReleases
// (admin / owner / l2 / audit,自定义角色不算 —— 2026-10-30 用户确认),前端不再拿
// canApprove / 角色码自己拼。从前 l2 看得到切换、点「全部」后端却仍只回自己的,切了等于没切。

import { envelope, stubCommon, ME } from './helpers'

async function openAs(page: Page, me: Record<string, unknown>) {
  const scopes: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(me)))
  await page.route('**/api/v1/releases?*', (r) => {
    scopes.push(new URL(r.request().url()).searchParams.get('scope') || '')
    return r.fulfill(envelope({ items: [], total: 0, page: 1, pageSize: 50 }))
  })
  await page.goto('/#/releases')
  await expect.poll(() => scopes.length).toBeGreaterThan(0)
  return scopes
}

const L2 = {
  ...ME, id: 4, name: 'Chen Hao', roleId: 3, roleCode: 'l2', roleName: 'DBA L2',
  roleIds: [3], roleNames: ['DBA L2'], roleCodes: ['l2'], canApprove: false, canSeeAllReleases: true,
}

test.describe('变更发布 · 我的 / 全部', () => {
  test('能看全部的人(l2)看得到切换,一进来就是「全部」', async ({ page }) => {
    const scopes = await openAs(page, L2)
    expect(scopes[0]).toBe('all')
    const seg = page.locator('.seg .si', { hasText: '全部' })
    await expect(seg).toBeVisible()
    await expect(seg).toHaveClass(/active/)
    await page.locator('.seg .si', { hasText: '我的' }).click()
    await expect.poll(() => scopes.at(-1)).toBe('mine')
  })

  test('可审批但后端说看不了全部(如自定义角色)时没有切换,只拉「我的」', async ({ page }) => {
    const scopes = await openAs(page, { ...L2, roleCodes: ['custom_x'], canApprove: true, canSeeAllReleases: false })
    expect(scopes[0]).toBe('mine')
    await expect(page.locator('.seg .si', { hasText: '全部' })).toHaveCount(0)
  })
})
