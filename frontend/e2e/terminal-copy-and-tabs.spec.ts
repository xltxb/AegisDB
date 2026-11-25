import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, gotoTerminal } from './helpers'

// Web 命令行的两处回归:
//
//   1. 选中即复制:人在别处复制了别的内容,回来在终端里再选**同一段**文字,剪贴板必须
//      重新变成终端里的这段。原先记住了"上次复制过什么"、相同就不写,粘出来的是别处的内容。
//   2. 切到另一个标签页再切回来,当前那行提示符必须完整。原先隐藏时仍然 fit,把终端缩成
//      最小列数、内容被重排,切回来只剩提示符开头几个字符(截图里的「tidb-prod-」)。

const CONN = {
  id: 1, name: 'tidb-prod-c66-seamless-jili', engine: 'TiDB', env: 'prod', tierCode: 'prod', status: 'online',
  host: 'tidb.internal', port: 4000, database: 'c66', policy: 'audit-only', defaultRole: 'dba_l2', tags: '',
}

async function openTerminal(page: Page) {
  await stubCommon(page, {
    tiers: [{ code: 'prod', displayName: 'PROD', sortOrder: 0, requireMfa: false, dangerBanner: false, countsInPending: true, scanBaseline: true, connLayer: 'L1', defaultRole: 'dba_l2' }],
    envs: [{ code: 'prod', displayName: 'PROD', tierCode: 'prod', sortOrder: 0 }],
  })
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([CONN])))
  await page.route((u) => u.pathname.endsWith('/api/v1/connections/1/schema'), (r) =>
    r.fulfill(envelope({ connectionId: 1, databases: [{ name: 'c66', tables: [] }] })))
  await gotoTerminal(page)
  await page.locator('.inst.active', { hasText: CONN.name }).waitFor()
  await page.locator('.xterm:visible').waitFor()
  // 进生产实例的确认框(如果弹了)
  const ack = page.locator('.mfa-btn.danger:visible')
  if (await ack.count()) await ack.first().click()
}

const rows = (page: Page) => page.evaluate(() => {
  const x = [...document.querySelectorAll('.xterm')].find((e) => (e as HTMLElement).offsetParent !== null)
  return x ? [...x.querySelectorAll('.xterm-rows > div')].map((d) => (d.textContent || '').trimEnd()).filter(Boolean) : []
})

test('选中即复制:别处复制过别的内容后,再选同一段仍会写回剪贴板', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await openTerminal(page)
  const first = page.locator('.xterm:visible .xterm-rows > div').first()
  await expect(first).toContainText('会话已连接')
  const box = (await first.boundingBox())!
  const selectFirstRow = async () => {
    await page.mouse.move(box.x + 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x + 260, box.y + box.height / 2, { steps: 5 })
    await page.mouse.up()
  }
  const clip = () => page.evaluate(() => navigator.clipboard.readText())

  await selectFirstRow()
  await expect.poll(clip).toContain('会话已连接')
  const copied = await clip()

  await page.evaluate(() => navigator.clipboard.writeText('别的文件里复制的内容'))
  await selectFirstRow()
  await expect.poll(clip).toBe(copied)
})

test('切到别的标签页再切回来:提示符完整', async ({ page }) => {
  await openTerminal(page)
  // 默认库异步加载完,下一个提示符会从「实例 ❯」变成「实例/库 ❯」—— 等它定下来再比
  await page.locator('.xterm:visible').click()
  await expect.poll(async () => {
    await page.keyboard.press('Enter')
    return (await rows(page)).at(-1)
  }).toContain('/c66')
  const before = (await rows(page)).at(-1) || ''
  expect(before).toContain(CONN.name)
  expect(before).toContain('❯')

  await page.locator('.tabspacer').dblclick()
  await expect(page.locator('.tab')).toHaveCount(2)
  await page.waitForTimeout(300)
  await page.locator('.tab').first().click()
  await page.waitForTimeout(300)

  const after = await rows(page)
  expect(after.at(-1)).toBe(before)
  for (const r of after) {
    if (r.startsWith('tidb-prod')) expect(r, '不该有被截断的提示符行').toContain('❯')
  }
})
