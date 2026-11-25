import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 2026-11-14:规范配置 ——
//   ① 出处/分级按所选的库显示(MySQL 页上不再拿 DWS 的【強制】说"必须带 WHERE");
//   ② 参数改成表单:每格标明含义和留空时的默认值(按库),无参数的规则直接说没有参数。

const base = { kind: 'builtin', enabled: true, category: 'naming', message: '', sortOrder: 0 }
const RULES = [
  { ...base, id: 1, code: 'dml.require.where', name: 'UPDATE/DELETE 必须带 WHERE', category: 'dml', level: 'error', dialect: 'all',
    spec: 'mandatory', specRef: 'DWS对象设计和业务开发规范 RULE 46', params: '' },
  { ...base, id: 2, code: 'naming.identifier.length', name: '标识符长度上限', level: 'warn', dialect: 'mysql,tidb,oracle,generic',
    spec: '', specRef: '', params: '' },
]
const CATALOG = {
  dialects: ['all', 'mysql', 'tidb', 'dws', 'oracle'], categories: ['dml', 'naming'], levels: ['error', 'warn', 'info'],
  specs: ['critical', 'mandatory', 'recommended'],
  params: { 'naming.identifier.length': [{ key: 'max', type: 'int', default: 64, dialectDefaults: { oracle: 30 } }] },
}

async function open(page: Page) {
  const saved: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope(ME)))
  await page.route('**/api/v1/sql-review/rules', (r) => r.fulfill(envelope(RULES)))
  await page.route('**/api/v1/sql-review/rules/*', (r) => {
    const body = r.request().postDataJSON()
    saved.push(body.params)
    const id = Number(r.request().url().split('/').pop())
    return r.fulfill(envelope({ ...RULES.find((x) => x.id === id), ...body }))
  })
  await page.route('**/api/v1/sql-review/catalog', (r) => r.fulfill(envelope(CATALOG)))
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.goto('/#/admin/sql-review')
  await expect(page.locator('.v-head')).toBeVisible()
  return saved
}

const row = (page: Page, code: string) => page.locator('.rule', { has: page.locator('.rcode', { hasText: code }) })

test('出处按库显示:只出自 DWS 规范的规则,在 MySQL 页上是平台内置', async ({ page }) => {
  await open(page)
  const where = row(page, 'dml.require.where')
  await expect(where.locator('.sref')).toContainText('DWS')

  await page.locator('.tab', { hasText: 'MYSQL' }).click()
  await expect(where.locator('.sbadge')).toHaveText('平台内置')
  await expect(where.locator('.sref')).toHaveCount(0)

  await page.locator('.tab', { hasText: 'DWS' }).click()
  await expect(where.locator('.sbadge')).toHaveText('强制')
  await expect(where.locator('.sref')).toContainText('RULE 46')
})

test('参数表单:留空的格子显示生效中的默认值(按库),填了才写进参数', async ({ page }) => {
  const saved = await open(page)
  const idLen = row(page, 'naming.identifier.length')
  // 列表行不再是一个"—":直接说它按多少在跑
  await expect(idLen.locator('.rmsg')).toContainText('标识符长度上限(字符) 64')
  await page.locator('.tab', { hasText: 'ORACLE' }).click()
  await expect(idLen.locator('.rmsg')).toContainText('30')

  await idLen.locator('.rbtn').first().click()
  const input = idLen.locator('.pfield[data-key="max"] input')
  await expect(input).toHaveAttribute('placeholder', '30')
  await expect(idLen.locator('.phelp')).toContainText('Oracle 上默认: 30')
  await input.fill('128')
  await idLen.getByRole('button', { name: '保存' }).click()
  await expect.poll(() => saved.at(-1)).toBe('{"max":128}')
})

test('没有可调参数的规则直接说没有,不再摆一个空文本框', async ({ page }) => {
  await open(page)
  const where = row(page, 'dml.require.where')
  await where.locator('.rbtn').first().click()
  await expect(where.locator('.pnone')).toBeVisible()
  await expect(where.locator('.pfield')).toHaveCount(0)
})
