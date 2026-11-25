import { test, expect, type Page, type Locator } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// 2026-11-10 用户反馈:新建了分层 fat,到「环境 · 实例分组」新建环境时下拉里选不到它。
// 选项其实在,是卡片 overflow: hidden 把展开在卡片底部的菜单裁掉了 —— 排在后面的
// (新建的分层总在最后)看不见也点不到。下拉改成 floating(按视口定位)后不再被裁。

const T = (code: string, i: number) => ({
  code, displayName: code.toUpperCase(), sortOrder: i, requireMfa: false, dangerBanner: false, countsInPending: false,
  scanBaseline: code === 'prod', strictNoWhere: true, maskSensitive: false, bizExportRestrict: false, connLayer: 'L', defaultRole: 'dba_l2',
})
const TIERS = ['prod', 'gli', 'staging', 'uat', 'dev', 'fat'].map(T)
const ENVS = ['prod', 'gli', 'staging', 'uat', 'dev'].map((c, i) => ({ code: c, displayName: c.toUpperCase(), tierCode: c, sortOrder: i }))

// 真的露在外面:选项中心点上命中的就是它自己(被裁掉的命中的是别的元素或什么都没有)。
// 不要先 scrollIntoView —— 它会把 overflow: hidden 的卡片内容滚上去,裁切就被掩盖了。
async function hittable(opt: Locator) {
  await expect(opt).toBeVisible()
  return opt.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    return !!hit && (hit === el || el.contains(hit))
  })
}

async function openEnvTab(page: Page) {
  await page.setViewportSize({ width: 1600, height: 900 })
  await stubCommon(page, { tiers: TIERS, envs: ENVS })
  await page.route('**/api/v1/connections*', (r) => r.fulfill(envelope([])))
  await page.goto('/#/admin/env-tiers')
  await page.locator('.segtab').nth(1).click()
}

test('新建环境:绑定分层下拉里每一项都看得见点得到,新建的 fat 也能选上并提交', async ({ page }) => {
  let created: any = null
  await openEnvTab(page)
  await page.route('**/api/v1/environments', (r) => {
    if (r.request().method() === 'POST') { created = r.request().postDataJSON(); return r.fulfill(envelope({})) }
    return r.fulfill(envelope(ENVS))
  })
  await page.getByRole('button', { name: '新建环境' }).click()
  const form = page.locator('.form')
  await form.locator('input').nth(0).fill('fat')
  await form.locator('input').nth(1).fill('FAT 环境')
  await form.locator('.vsel .control').click()

  const opts = form.locator('.vsel .opt')
  await expect(opts).toHaveText(['prod', 'gli', 'staging', 'uat', 'dev', 'fat'])
  for (const name of ['prod', 'gli', 'staging', 'uat', 'dev', 'fat']) {
    const o = opts.filter({ hasText: new RegExp(`^\\s*${name}\\s*$`) })
    expect(await hittable(o), `选项 ${name} 被遮住`).toBe(true)
  }
  await opts.filter({ hasText: /^\s*fat\s*$/ }).click()
  await expect(form.locator('.vsel .val')).toHaveText('fat')
  await form.getByRole('button', { name: '保存' }).click()
  await expect.poll(() => created).toMatchObject({ code: 'fat', tierCode: 'fat' })
})

test('环境行上改绑分层:最后一行的下拉同样不被卡片裁掉', async ({ page }) => {
  await openEnvTab(page)
  // 最后一行(dev)进入改绑
  const lastRow = page.locator('.bindcell').last()
  await lastRow.locator('button').first().click()
  await lastRow.locator('.vsel .control').click()
  const opts = lastRow.locator('.vsel .opt')
  await expect(opts).toHaveCount(6)
  const fat = opts.last()
  expect(await hittable(fat), '最后一项被遮住').toBe(true)
})
