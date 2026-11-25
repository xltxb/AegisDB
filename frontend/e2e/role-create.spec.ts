import { test, expect, type Page } from '@playwright/test'
import { envelope, stubCommon, autoConfirm } from './helpers'

// 2026-10-22:「新建角色」从前打开的是当前选中角色的编辑表单(标题「编辑角色 · xxx」,保存会改掉那个
// 角色)。现在新建与编辑分开:新建发 POST /roles,建好后选中新角色;编辑回显角色的真实值(可审批不再按 code 猜)。

const ROLES: any[] = [
  { id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown', members: 1 },
  { id: 6, code: 'r_ab12cd34ef', name: '发布审批', layer: '自定义', icon: 'shield', members: 0 },
]
const detail = (r: any, extra: Record<string, unknown> = {}) => ({ ...r, menus: {}, matrix: {}, memberIds: [], members: [], tags: [], ...extra })

async function open(page: Page) {
  const calls: { method: string; url: string; body: any }[] = []
  await stubCommon(page)
  await page.route('**/api/v1/roles', (r) => {
    if (r.request().method() === 'POST') {
      const body = r.request().postDataJSON()
      calls.push({ method: 'POST', url: r.request().url(), body })
      const created = { id: 9, code: 'r_0011223344', name: body.name, layer: body.layer || '自定义', icon: 'shield', members: 0 }
      ROLES.push(created)
      return r.fulfill(envelope(detail(created, { canApprove: false })))
    }
    return r.fulfill(envelope(ROLES))
  })
  await page.route(/\/api\/v1\/roles\/\d+$/, (r) => {
    const id = Number(r.request().url().split('/').pop())
    if (r.request().method() === 'PATCH') calls.push({ method: 'PATCH', url: r.request().url(), body: r.request().postDataJSON() })
    const role = ROLES.find((x) => x.id === id)
    return r.fulfill(envelope(detail(role, id === 6 ? { canApprove: true, description: '能批发布单', defaultConnRole: 'readonly' } : {})))
  })
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await page.goto('/#/admin/permissions')
  return calls
}

test('新建角色:是空表单、发 POST,不动当前选中的角色', async ({ page }) => {
  const calls = await open(page)
  await page.getByText('平台管理员', { exact: true }).first().click()
  await page.getByTestId('pm-new-role').click()
  await expect(page.getByTestId('role-form-title')).toHaveText('新建角色')
  await expect(page.getByTestId('role-form-name')).toHaveValue('')
  await expect(page.getByTestId('role-form-save')).toBeDisabled()
  await page.getByTestId('role-form-name').fill('数据分析')
  await page.getByTestId('role-form-save').click()
  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0].method).toBe('POST')
  expect(calls[0].body.name).toBe('数据分析')
  expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
  // 建好后选中新角色
  await expect(page.locator('.mtitle')).toContainText('数据分析')
})

test('编辑角色:回显真实的「可审批」与描述,保存发 PATCH', async ({ page }) => {
  const calls = await open(page)
  await page.getByText('发布审批', { exact: true }).first().click()
  await page.locator('.vbtn', { hasText: '编辑角色' }).click()
  await expect(page.getByTestId('role-form-title')).toContainText('编辑角色')
  await expect(page.locator('.imodal textarea')).toHaveValue('能批发布单')
  await page.getByTestId('role-form-save').click()
  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0].method).toBe('PATCH')
  expect(calls[0].url).toMatch(/\/roles\/6$/)
  expect(calls[0].body.canApprove).toBe(true) // 从前按 code 猜成 false,保存会把它改掉
  expect(calls[0].body.description).toBe('能批发布单')
})

// 删除:只有自定义角色有按钮;确认后发 DELETE;服务端拒绝(还有成员)时把原因提示出来
test('删除角色:内置角色没有按钮;自定义角色确认后删除,被拒时说明原因', async ({ page }) => {
  const dels: string[] = []
  let refuse = true
  await stubCommon(page)
  const roles: any[] = [
    { id: 1, code: 'admin', name: '平台管理员', layer: 'L0 · 全局', icon: 'crown', members: 1 },
    { id: 7, code: 'r_77aa77aa77', name: '临时角色', layer: '自定义', icon: 'shield', members: 1 },
  ]
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope(roles)))
  await page.route(/\/api\/v1\/roles\/\d+$/, (r) => {
    const id = Number(r.request().url().split('/').pop())
    if (r.request().method() === 'DELETE') {
      dels.push(r.request().url())
      if (refuse) return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 40001, msg: '角色「临时角色」下还有 1 个用户,请先把他们移出这个角色', data: null }) })
      roles.splice(roles.findIndex((x) => x.id === id), 1)
      return r.fulfill(envelope({ ok: true }))
    }
    const role = roles.find((x) => x.id === id)
    return r.fulfill(envelope(detail(role, { deletable: role.code.startsWith('r_') })))
  })
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([])))
  await autoConfirm(page)
  await page.goto('/#/admin/permissions')

  await page.getByText('平台管理员', { exact: true }).first().click()
  await expect(page.getByTestId('pm-delete-role')).toHaveCount(0)

  await page.getByText('临时角色', { exact: true }).first().click()
  await page.getByTestId('pm-delete-role').click()
  await expect(page.getByText('请先把他们移出这个角色')).toBeVisible()
  expect(dels).toHaveLength(1)

  refuse = false
  await page.getByTestId('pm-delete-role').click()
  await expect.poll(() => dels.length).toBe(2)
  await expect(page.locator('.ritem', { hasText: '临时角色' })).toHaveCount(0)
})
