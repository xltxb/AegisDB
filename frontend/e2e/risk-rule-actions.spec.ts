import { test, expect } from '@playwright/test'
import { envelope, stubCommon } from './helpers'

// issue #125:「仅告警」与「仅审批」都存成 mid —— 选了只告警,命令却要走审批。字典没有"放行但告警"
// 这一档,界面上拿掉;统计卡和副标题里的"告警"一并改掉。

const CMDS = [
  { command: 'DROP', tiers: { prod: 'high' } },
  { command: 'GRANT', tiers: { prod: 'mid' } },
  { command: 'REVOKE', tiers: { prod: 'mid' } },
]

for (const [action, level] of [['拦截+审批', 'high'], ['仅审批', 'mid']] as const) {
  test(`新建规则只有两种动作,${action} 存成 ${level}`, async ({ page }) => {
    const writes: any[] = []
    await stubCommon(page)
    await page.route('**/api/v1/risk-commands*', (r) => {
      if (r.request().method() !== 'GET') writes.push(r.request().postDataJSON())
      return r.fulfill(envelope(CMDS))
    })
    await page.goto('/#/admin/risk-rules')
    await page.getByRole('button', { name: '新建规则' }).click()
    const modal = page.locator('.rfmodal')
    await expect(modal).not.toContainText('告警')
    const actions = modal.locator('.rf2 > div').nth(1).locator('.si')
    await expect(actions).toHaveText(['拦截+审批', '仅审批'])
    await actions.filter({ hasText: action }).click()
    await modal.getByRole('button', { name: '创建规则' }).click()
    await expect.poll(() => writes.length).toBeGreaterThan(0)
    for (const w of writes) expect(Object.values(w.tiers)).toEqual([level])
  })
}

test('统计卡说的是需审批,页面上没有"告警"', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/risk-commands*', (r) => r.fulfill(envelope(CMDS)))
  await page.goto('/#/admin/risk-rules')
  const stat = page.locator('.stat', { hasText: '需审批规则' })
  await expect(stat.locator('.n')).toHaveText('2')
  await expect(page.locator('.page')).not.toContainText('告警')
})

// issue #127:「规则名称」填了不提交也不保存 —— 字典按命令存,没有规则名。弹窗里唯一的输入框是自定义命令。
test('新建规则没有规则名称,触发命令照常新增', async ({ page }) => {
  const writes: any[] = []
  await stubCommon(page)
  await page.route('**/api/v1/risk-commands*', (r) => {
    if (r.request().method() !== 'GET') writes.push(r.request().postDataJSON())
    return r.fulfill(envelope(CMDS))
  })
  await page.goto('/#/admin/risk-rules')
  await page.getByRole('button', { name: '新建规则' }).click()
  const modal = page.locator('.rfmodal')
  await expect(modal.locator('.trigs')).toBeVisible()
  await expect(modal).not.toContainText('规则名称')
  await expect(modal.locator('input')).toHaveCount(1)
  await modal.locator('input').fill('revoke')
  await modal.locator('input').press('Enter')
  await expect(modal.locator('.trig', { hasText: 'REVOKE' })).toBeVisible()
  await modal.getByRole('button', { name: '创建规则' }).click()
  await expect.poll(() => writes.map((w) => w.command)).toEqual(['DROP', 'TRUNCATE', 'REVOKE'])
  await expect(modal).toHaveCount(0)
})
