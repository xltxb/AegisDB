import { test, expect, type Locator, type Page } from '@playwright/test'
import { envelope, stubCommon, ME } from './helpers'

// 聊天审批卡片(.scratch/chat-approval-cards/PRD.md)的界面:用户菜单里的绑定、设置 › 审批里的各
// 渠道、审批页上的拒绝原因与决定渠道、用户管理里的替人解绑。

const CODE = 'AB2CD3EF4G'
const CJK = /[一-鿿]/

const fail = (code: number, msg: string, msgRef?: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify({ code, msg, msgRef, data: null }) })
const unbound = { channel: 'lark', botName: '', bound: false }
const bound = { channel: 'lark', botName: '', bound: true, boundAt: '2026-11-25T11:40:00Z' }
const tgUnbound = { channel: 'telegram', botName: 'aegis_appr_bot', bound: false }

const english = (page: Page) => page.addInitScript(() => localStorage.setItem('aegisdb_lang', 'en'))

/** 一块界面上没有漏翻的中文:看得见的文字,以及占位与悬停提示。 */
async function expectEnglish(box: Locator) {
  expect(await box.innerText()).not.toMatch(CJK)
  const attrs = await box.locator('[placeholder],[title]').evaluateAll((els) =>
    els.map((e) => (e.getAttribute('placeholder') ?? '') + (e.getAttribute('title') ?? '')))
  for (const a of attrs) expect(a).not.toMatch(CJK)
}

// ---------------------------------------------------------------- 绑定

interface BindStub { state: { list: unknown[] }; codes: any[]; asked: string[]; removed: string[] }

async function stubBind(page: Page, opts: { list: unknown[]; mfa?: boolean; botName?: string; code?: (body: any) => object | undefined }): Promise<BindStub> {
  const s: BindStub = { state: { list: opts.list }, codes: [], asked: [], removed: [] }
  await stubCommon(page)
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(envelope({ ...ME, mfaEnabled: !!opts.mfa })))
  await page.route('**/api/v1/dashboard**', (r) => r.fulfill(envelope({})))
  await page.route('**/api/v1/auth/chat-bindings', (r) => r.fulfill(envelope(s.state.list)))
  await page.route('**/api/v1/auth/chat-bindings/*/code', (r) => {
    const body = r.request().postDataJSON()
    s.codes.push(body)
    s.asked.push(new URL(r.request().url()).pathname)
    return r.fulfill((opts.code?.(body) ?? envelope({ code: CODE, expiresAt: '2026-11-25T12:00:00Z', botName: opts.botName ?? '' })) as never)
  })
  await page.route('**/api/v1/auth/chat-bindings/lark', (r) => {
    s.removed.push(r.request().method())
    s.state.list = [unbound]
    return r.fulfill(envelope({ ok: true }))
  })
  return s
}

async function openBind(page: Page) {
  await page.goto('/#/dashboard')
  await page.locator('.avatar').click()
  await page.getByTestId('menu-chat').click()
  await expect(page.getByTestId('chat-bind-modal')).toBeVisible()
}

test.describe('审批机器人绑定', () => {
  test('渠道都关着:用户菜单里没有这一项', async ({ page }) => {
    let asked = 0
    await stubBind(page, { list: [] })
    await page.route('**/api/v1/auth/chat-bindings', (r) => { asked++; return r.fulfill(envelope([])) })
    await page.goto('/#/dashboard')
    await expect(page.locator('.avatar')).toBeVisible()
    expect(asked).toBe(0) // 打开菜单才问
    await page.locator('.avatar').click()
    await expect(page.getByTestId('menu-password')).toBeVisible()
    await expect.poll(() => asked).toBe(1)
    await expect(page.getByTestId('menu-chat')).toHaveCount(0)
  })

  test('读不到绑定状态:同样没有这一项', async ({ page }) => {
    await stubBind(page, { list: [unbound] })
    await page.route('**/api/v1/auth/chat-bindings', (r) => r.fulfill(fail(50000, 'x')))
    await page.goto('/#/dashboard')
    const answered = page.waitForResponse('**/api/v1/auth/chat-bindings')
    await page.locator('.avatar').click()
    await answered
    await expect(page.getByTestId('menu-password')).toBeVisible()
    await expect(page.getByTestId('menu-chat')).toHaveCount(0)
  })

  test('再次打开菜单:等到这一次的答复才显示;迟到的旧答复不算', async ({ page }) => {
    await stubBind(page, { list: [] })
    const replies: ((list: unknown[]) => void)[] = []
    await page.route('**/api/v1/auth/chat-bindings', async (r) => {
      const list = await new Promise<unknown[]>((done) => replies.push(done))
      await r.fulfill(envelope(list))
    })
    const open = async (n: number) => {
      await page.locator('.avatar').click()
      await expect.poll(() => replies.length).toBe(n)
    }
    const close = () => page.locator('.um-mask').click({ position: { x: 600, y: 300 } })
    await page.goto('/#/dashboard')
    await open(1)
    replies[0]([unbound])
    await expect(page.getByTestId('menu-chat')).toBeVisible()
    await close()
    await open(2) // 渠道可能已经关了:不拿上一次的答复先顶着
    await expect(page.getByTestId('menu-password')).toBeVisible()
    await expect(page.getByTestId('menu-chat')).toHaveCount(0, { timeout: 500 })
    await close()
    await open(3)
    const third = page.waitForResponse('**/api/v1/auth/chat-bindings')
    replies[2]([])
    await third
    const second = page.waitForResponse('**/api/v1/auth/chat-bindings')
    replies[1]([unbound]) // 第二次的答复这时才到
    await second
    await page.waitForTimeout(300)
    await expect(page.getByTestId('menu-chat')).toHaveCount(0)
  })

  test('解绑成了、重读却失败:不报失败,也不显示成还绑着', async ({ page }) => {
    const s = await stubBind(page, { list: [bound] })
    await openBind(page)
    let reread = 0
    await page.route('**/api/v1/auth/chat-bindings', (r) => { reread++; return r.fulfill(fail(50000, '读不到')) })
    await page.getByTestId('chat-unbind-lark').click()
    await page.getByTestId('confirm-ok').click()
    await expect.poll(() => reread).toBeGreaterThan(0)
    await expect(page.getByTestId('chat-state-lark')).toHaveText('未绑定')
    await expect(page.getByText('读不到')).toHaveCount(0)
    await expect(page.getByTestId('chat-gen-lark')).toBeEnabled()
    expect(s.removed).toEqual(['DELETE'])
  })

  test('启用了 MFA 的账号重新生成:还有效的码不藏起来,可以取消', async ({ page }) => {
    const s = await stubBind(page, { list: [unbound], mfa: true, code: (b) => (b.mfaCode === '654321' ? envelope({ code: 'ZZ99YY88XX', expiresAt: '', botName: '' }) : undefined) })
    await openBind(page)
    const gen = async (mfa: string) => {
      await page.getByTestId('chat-gen-lark').click()
      await page.getByTestId('chat-mfa').fill(mfa)
      await page.getByTestId('chat-mfa-submit').click()
    }
    await gen('123456')
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await page.getByTestId('chat-gen-lark').click()
    await expect(page.getByTestId('chat-mfa')).toBeVisible()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await page.getByTestId('chat-mfa').fill('11')
    await page.getByTestId('chat-mfa-cancel').click()
    await expect(page.getByTestId('chat-mfa')).toHaveCount(0)
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    expect(s.codes.length).toBe(1)
    await gen('654321')
    await expect(page.getByTestId('chat-code')).toHaveText('ZZ99YY88XX')
  })

  test('生成码、写明 10 分钟内有效与怎么用;绑上之后自动变成已绑定;解绑要确认', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const s = await stubBind(page, { list: [unbound] })
    await openBind(page)
    const modal = page.getByTestId('chat-bind-modal')
    await expect(modal).not.toContainText('Telegram')
    await expect(page.getByTestId('chat-state-lark')).toHaveText('未绑定')
    await expect(page.getByTestId('chat-unbind-lark')).toHaveCount(0)

    await page.getByTestId('chat-gen-lark').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await expect(modal).toContainText('10 分钟内有效')
    await expect(modal).toContainText('私聊')
    expect(s.codes).toEqual([{}]) // 没开 MFA 的账号不要验证码
    await page.getByTestId('chat-code-copy').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(CODE)

    s.state.list = [bound] // 在聊天里把码发给了机器人
    await expect(page.getByTestId('chat-state-lark')).toHaveText('已绑定', { timeout: 10_000 })
    await expect(page.getByTestId('chat-code')).toHaveCount(0)

    await page.getByTestId('chat-unbind-lark').click()
    await page.getByTestId('confirm-cancel').click()
    expect(s.removed).toEqual([])
    await page.getByTestId('chat-unbind-lark').click()
    await expect(page.getByTestId('confirm-message')).toContainText('Lark')
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('chat-state-lark')).toHaveText('未绑定')
    expect(s.removed).toEqual(['DELETE'])
  })

  test('启用了 MFA 的账号先要验证码;输错了说原因、可以再试', async ({ page }) => {
    const s = await stubBind(page, {
      list: [unbound], mfa: true,
      code: (b) => (b.mfaCode === '123456' ? undefined : fail(40001, '验证码错误,请重试')),
    })
    await openBind(page)
    await page.getByTestId('chat-gen-lark').click()
    const mfa = page.getByTestId('chat-mfa')
    await expect(mfa).toBeFocused()
    expect(s.codes).toEqual([]) // 还没问服务端要码
    await page.getByTestId('chat-mfa-submit').click()
    await expect(page.getByTestId('chat-err')).toBeVisible() // 不足 6 位
    expect(s.codes).toEqual([])
    await mfa.fill('000000')
    await page.getByTestId('chat-mfa-submit').click()
    await expect(page.getByTestId('chat-err')).toContainText('验证码错误')
    await expect(page.getByTestId('chat-code')).toHaveCount(0)
    await mfa.fill('123456')
    await page.getByTestId('chat-mfa-submit').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    expect(s.codes).toEqual([{ mfaCode: '000000' }, { mfaCode: '123456' }])
  })

  test('页面不知道账号刚开了 MFA:服务端答 42800 时亮出验证码框', async ({ page }) => {
    await stubBind(page, { list: [unbound], code: (b) => (b.mfaCode ? undefined : fail(42800, '请输入 MFA 验证码')) })
    await openBind(page)
    await page.getByTestId('chat-gen-lark').click()
    await page.getByTestId('chat-mfa').fill('123456')
    await page.getByTestId('chat-mfa-submit').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
  })

  test('关掉弹窗码就没了;没在显示码时不轮询', async ({ page }) => {
    await stubBind(page, { list: [unbound] })
    let asked = 0
    await page.route('**/api/v1/auth/chat-bindings', (r) => { asked++; return r.fulfill(envelope([unbound])) })
    await openBind(page)
    await page.getByTestId('chat-gen-lark').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await expect.poll(() => asked, { timeout: 10_000 }).toBeGreaterThan(1) // 打开菜单那一次之外还在问
    await page.getByTestId('chat-close').click()
    await expect(page.getByTestId('chat-bind-modal')).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(CODE)

    await page.locator('.avatar').click()
    await page.getByTestId('menu-chat').click()
    await expect(page.getByTestId('chat-state-lark')).toBeVisible()
    await expect(page.getByTestId('chat-code')).toHaveCount(0)
    const before = asked
    await page.waitForTimeout(4000)
    expect(asked).toBe(before)
  })

  test('码过了 10 分钟不再显示,要人重新生成', async ({ page }) => {
    await page.clock.install()
    await stubBind(page, { list: [unbound] })
    await openBind(page)
    await page.getByTestId('chat-gen-lark').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await page.clock.fastForward('10:01')
    await expect(page.getByTestId('chat-code')).toHaveCount(0)
    await expect(page.getByTestId('chat-err')).toContainText('过期')
    await expect(page.getByTestId('chat-gen-lark')).toBeVisible()
  })

  test('英文界面:绑定弹窗全是英文', async ({ page }) => {
    await english(page)
    await stubBind(page, { list: [unbound], mfa: true })
    await openBind(page)
    const modal = page.getByTestId('chat-bind-modal')
    await expectEnglish(modal)
    await page.getByTestId('chat-gen-lark').click()
    await expectEnglish(modal)
    await page.getByTestId('chat-mfa').fill('123456')
    await page.getByTestId('chat-mfa-submit').click()
    await expect(modal).toContainText('10 minutes')
    await expectEnglish(modal)
  })

  test('Telegram:打开链接即可绑定,码也看得见;Lark 没有链接', async ({ page }) => {
    const s = await stubBind(page, { list: [unbound, tgUnbound], botName: 'aegis_appr_bot' })
    await openBind(page)
    const modal = page.getByTestId('chat-bind-modal')
    await expect(modal).toContainText('Lark（飞书）')
    await expect(page.getByTestId('chat-state-telegram')).toHaveText('未绑定')
    await page.getByTestId('chat-gen-lark').click()
    await expect(page.getByTestId('chat-code')).toHaveText(CODE)
    await expect(page.getByTestId('chat-link')).toHaveCount(0) // 给了机器人名也没有:不是 Telegram

    await page.getByTestId('chat-gen-telegram').click()
    const link = page.getByTestId('chat-link')
    await expect(link).toHaveAttribute('href', `https://t.me/aegis_appr_bot?start=${CODE}`)
    await expect(link).toHaveText(`https://t.me/aegis_appr_bot?start=${CODE}`)
    await expect(link).toHaveAttribute('target', '_blank')
    await expect(link).toHaveAttribute('rel', /noopener/)
    await expect(page.getByTestId('chat-code')).toHaveCount(2)
    await expect(modal).toContainText('Telegram')
    expect(s.asked).toEqual(['/api/v1/auth/chat-bindings/lark/code', '/api/v1/auth/chat-bindings/telegram/code'])

    s.state.list = [unbound, { ...tgUnbound, bound: true, boundAt: '2026-11-25T11:40:00Z' }]
    await expect(page.getByTestId('chat-state-telegram')).toHaveText('已绑定', { timeout: 10_000 })
    await expect(link).toHaveCount(0)
  })

  for (const botName of ['', 'evil/../x?start=1#', 'a b']) {
    test(`Telegram:机器人名是「${botName}」时不给链接,改说把码发给它`, async ({ page }) => {
      await stubBind(page, { list: [{ ...tgUnbound, botName }], botName })
      await openBind(page)
      await page.getByTestId('chat-gen-telegram').click()
      await expect(page.getByTestId('chat-code')).toHaveText(CODE)
      await expect(page.getByTestId('chat-bind-modal')).toContainText('私聊把上面的码发给它')
      await expect(page.getByTestId('chat-link')).toHaveCount(0)
      await expect(page.getByTestId('chat-bind-modal').locator('a')).toHaveCount(0)
    })
  }

  test('Telegram:码里的特殊字符不会改动链接', async ({ page }) => {
    await stubBind(page, { list: [tgUnbound], code: () => envelope({ code: 'A&b=c#d', expiresAt: '', botName: 'aegis_appr_bot' }) })
    await openBind(page)
    await page.getByTestId('chat-gen-telegram').click()
    await expect(page.getByTestId('chat-link')).toHaveAttribute('href', 'https://t.me/aegis_appr_bot?start=A%26b%3Dc%23d')
  })

  test('机器人还没连上:说出后端的原因,没有码', async ({ page }) => {
    await english(page)
    await stubBind(page, { list: [{ ...tgUnbound, botName: '' }], code: () => fail(40001, '审批机器人还没连上,请稍后再试', { code: 'm9ec49329' }) })
    await openBind(page)
    await page.getByTestId('chat-gen-telegram').click()
    await expect(page.getByTestId('chat-err')).toHaveText(/not connected yet/i)
    await expect(page.getByTestId('chat-code')).toHaveCount(0)
    await expect(page.getByTestId('chat-gen-telegram')).toBeEnabled()
  })

  test('英文界面:Telegram 这一块全是英文', async ({ page }) => {
    await english(page)
    await stubBind(page, { list: [tgUnbound], botName: 'aegis_appr_bot' })
    await openBind(page)
    await page.getByTestId('chat-gen-telegram').click()
    await expect(page.getByTestId('chat-link')).toBeVisible()
    await expectEnglish(page.getByTestId('chat-bind-modal'))
  })
})

// ---------------------------------------------------------------- 设置

const ST_OFF = { enabled: false, state: 'off', detail: '', botName: '' }

interface SetStub { saved: Record<string, any>[]; tests: any[]; asked: () => number; status: { value: unknown }; tg: { value: unknown } }

async function openSettings(page: Page, opts: { settings?: Record<string, string>; secrets?: string[]; status?: unknown; tg?: unknown; save?: (b: any) => object | undefined; tab?: string } = {}): Promise<SetStub> {
  const s = { saved: [] as Record<string, any>[], tests: [] as any[], n: 0, status: { value: opts.status ?? ST_OFF }, tg: { value: opts.tg ?? ST_OFF } }
  await stubCommon(page)
  await page.route('**/api/v1/settings', (r) => {
    if (r.request().method() === 'PUT') {
      const body = r.request().postDataJSON()
      s.saved.push(body)
      return r.fulfill((opts.save?.(body) ?? envelope({ ok: true })) as never)
    }
    return r.fulfill(envelope({ settings: opts.settings ?? {}, secretsSet: Object.fromEntries((opts.secrets ?? []).map((k) => [k, true])) }))
  })
  await page.route('**/api/v1/settings/chat-status', (r) => {
    s.n++
    return r.fulfill(envelope({ lark: s.status.value, telegram: s.tg.value }))
  })
  await page.route('**/api/v1/settings/chat-test', (r) => { s.tests.push(r.request().postDataJSON()); return r.fulfill(envelope({ ok: true })) })
  await page.goto(`/#/admin/settings?tab=${opts.tab ?? 'approval'}`)
  await expect(page.locator('.savebar')).toBeVisible()
  return { saved: s.saved, tests: s.tests, asked: () => s.n, status: s.status, tg: s.tg }
}

const ON = {
  'chat.cardLang': '"en"', 'chat.lark.enabled': 'true', 'chat.lark.region': '"lark"', 'chat.lark.appId': '"cli_a1"',
}
const LARK_SECRET = 'chat.lark.appSecret'
const TG_TOKEN = 'chat.telegram.botToken'
const TG_PROXY = 'chat.telegram.proxyToken'
const TG_ON = { 'chat.telegram.enabled': 'true' }
const TG_OFF_PART = { 'chat.telegram.enabled': false, 'chat.telegram.botToken': '', 'chat.telegram.includeSql': false, 'chat.telegram.proxyToken': '' }
const LARK_OFF_PART = { 'chat.lark.enabled': false, 'chat.lark.region': 'feishu', 'chat.lark.appId': '', 'chat.lark.appSecret': '' }
const chatPart = (b: Record<string, any>) => Object.fromEntries(Object.entries(b).filter(([k]) => k.startsWith('chat.')))

test.describe('设置 › 审批 › 审批机器人', () => {
  test('老库里没有这些设置:默认关着,整包保存带上默认值,不会顺手打开', async ({ page }) => {
    const s = await openSettings(page)
    const panel = page.getByTestId('chat-panel')
    await expect(panel).toBeVisible()
    for (const ch of ['lark', 'telegram']) await expect(page.getByTestId(`chat-${ch}-enabled`)).not.toHaveClass(/on/)
    await expect(page.getByTestId('chat-lark-appid')).toHaveCount(0)
    await expect(page.getByTestId('chat-telegram-token')).toHaveCount(0)
    // 开关处就写明,不必先打开;对两个渠道都算数,只说一遍,和卡片语言一起排在各渠道前面
    const text = await panel.innerText()
    const at = ['用户 IP 白名单管不到卡片上的点击', '离职的人,在管理员于网关里停用其账号之前,聊天绑定仍然有效', '卡片语言']
    for (const s of at) {
      expect(text.split(s).length, s).toBe(2)
      expect(text.indexOf(s), s).toBeLessThan(text.indexOf('Lark（飞书） 审批机器人'))
    }
    expect(text.indexOf('Lark（飞书） 审批机器人')).toBeLessThan(text.indexOf('Telegram 审批机器人'))
    await expect(panel).toContainText('SSO')

    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(1)
    expect(chatPart(s.saved[0])).toEqual({
      'chat.cardLang': 'zh', ...LARK_OFF_PART, ...TG_OFF_PART,
    })
  })

  test('打开、填写、选国际版、保存:键与值;密钥存完就不留在框里', async ({ page }) => {
    const s = await openSettings(page)
    await page.getByTestId('chat-lark-enabled').click()
    await page.getByTestId('chat-lark-appid').fill('  cli_b2 ')
    await page.getByTestId('chat-lark-secret').fill(' s3cret ')
    const panel = page.getByTestId('chat-panel')
    await expect(page.getByTestId('chat-lark-unverified')).toHaveCount(0)
    await page.getByTestId('chat-lark-region').click()
    await page.getByTestId('chat-lark-region').locator('.opt').nth(1).click()
    await expect(page.getByTestId('chat-lark-unverified')).toContainText('未验证')
    await page.getByTestId('chat-card-lang').click()
    await page.getByTestId('chat-card-lang').locator('.opt', { hasText: 'English' }).click()
    await expect(panel).toContainText('保存后才生效')

    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(1)
    expect(chatPart(s.saved[0])).toEqual({
      'chat.cardLang': 'en', 'chat.lark.enabled': true, 'chat.lark.region': 'lark', 'chat.lark.appId': 'cli_b2', 'chat.lark.appSecret': 's3cret', ...TG_OFF_PART,
    })
    await expect(page.getByTestId('chat-lark-secret')).toHaveValue('')
    await expect(page.getByTestId('chat-lark-secret')).toHaveAttribute('placeholder', /已配置/)
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(2)
    expect(s.saved[1]['chat.lark.appSecret']).toBe('') // 留空 = 不改
  })

  test('已配置的密钥:留空不改;清除要确认,传 null', async ({ page }) => {
    const s = await openSettings(page, { settings: ON, secrets: [LARK_SECRET] })
    await expect(page.getByTestId('chat-lark-appid')).toHaveValue('cli_a1')
    await expect(page.getByTestId('chat-lark-unverified')).toBeVisible() // 读回来的就是国际版
    await page.getByTestId('chat-lark-secret-clear').click()
    await page.getByTestId('confirm-cancel').click()
    expect(s.saved).toEqual([])
    await page.getByTestId('chat-lark-secret-clear').click()
    await page.getByTestId('confirm-ok').click()
    await expect.poll(() => s.saved).toEqual([{ 'chat.lark.appSecret': null }])
    await expect(page.getByTestId('chat-lark-secret-clear')).toHaveCount(0)
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(2)
    expect(chatPart(s.saved[1])).toEqual({
      'chat.cardLang': 'en', 'chat.lark.enabled': true, 'chat.lark.region': 'lark', 'chat.lark.appId': 'cli_a1', 'chat.lark.appSecret': '', ...TG_OFF_PART,
    })
  })

  test('保存被拒:说出原因,密钥还在框里', async ({ page }) => {
    await openSettings(page, { save: () => fail(40001, '启用 Lark 审批机器人必须填写 App ID') })
    await page.getByTestId('chat-lark-enabled').click()
    await page.getByTestId('chat-lark-secret').fill('s3cret')
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect(page.getByText('启用 Lark 审批机器人必须填写 App ID')).toBeVisible()
    await expect(page.getByTestId('chat-lark-secret')).toHaveValue('s3cret')
  })

  test('状态:连上 / 出错带原因;只在这一页上轮询;保存后马上重取', async ({ page }) => {
    const s = await openSettings(page, { settings: ON, secrets: [LARK_SECRET], status: { enabled: true, state: 'connecting', detail: '', botName: '' } })
    const st = page.getByTestId('chat-lark-status')
    await expect(st).toContainText('连接中')
    s.status.value = { enabled: true, state: 'connected', detail: '', botName: '' }
    await expect(st).toContainText('已连接', { timeout: 10_000 }) // 轮询到的
    s.status.value = { enabled: true, state: 'error', detail: 'lark: bootstrap: code 10003', botName: '' }
    const before = s.asked()
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(s.asked, { timeout: 2000 }).toBeGreaterThan(before)
    await expect(st).toContainText('出错')
    await expect(st).toContainText('lark: bootstrap: code 10003')
    s.status.value = { enabled: true, state: 'conflict', detail: 'lark: connection: x', botName: '' }
    await expect(st).toContainText('冲突', { timeout: 10_000 })

    await page.locator('.tab', { hasText: '通知' }).click()
    await page.waitForTimeout(300)
    const left = s.asked()
    await page.waitForTimeout(6000)
    expect(s.asked()).toBe(left)
  })

  test('别的页签上不问状态;页面藏到后台时停、回来接着问', async ({ page }) => {
    const s = await openSettings(page, { tab: 'notify', settings: ON, secrets: [LARK_SECRET] })
    await page.waitForTimeout(500)
    expect(s.asked()).toBe(0)
    await page.locator('.tab', { hasText: '审批' }).click()
    await expect.poll(s.asked).toBeGreaterThan(0)
    const hide = (v: boolean) => page.evaluate((h) => {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => h })
      document.dispatchEvent(new Event('visibilitychange'))
    }, v)
    await hide(true)
    await page.waitForTimeout(300)
    const at = s.asked()
    await page.waitForTimeout(6000)
    expect(s.asked()).toBe(at)
    await hide(false)
    await expect.poll(s.asked, { timeout: 2000 }).toBeGreaterThan(at)
  })

  test('发测试卡片:即点即发;没绑定的说原因', async ({ page }) => {
    const s = await openSettings(page, { settings: ON, secrets: [LARK_SECRET], status: { enabled: true, state: 'connected', detail: '', botName: '' } })
    // 改了字段还没保存时,旁边的「已连接」说的是旧配置:这句话要一直在
    await expect(page.getByTestId('chat-panel')).toContainText('状态与测试卡片用的是已保存的配置')
    await page.getByTestId('chat-lark-test').click()
    await expect(page.getByTestId('chat-lark-msg')).toContainText('已发送')
    expect(s.tests).toEqual([{ channel: 'lark' }])
    expect(s.saved).toEqual([])
    await page.route('**/api/v1/settings/chat-test', (r) => r.fulfill(fail(40001, '你还没有绑定这个审批机器人')))
    await page.getByTestId('chat-lark-test').click()
    await expect(page.getByTestId('chat-lark-msg')).toContainText('你还没有绑定这个审批机器人')
  })

  test('英文界面:这一块全是英文,网关写的原因也翻译', async ({ page }) => {
    await english(page)
    await openSettings(page, {
      settings: ON, secrets: [LARK_SECRET],
      status: { enabled: true, state: 'error', detail: '密钥无法解密', detailRef: { code: 'm5d4b86df' }, botName: '' },
    })
    const panel = page.getByTestId('chat-panel')
    await expect(page.getByTestId('chat-lark-status')).toContainText('Error')
    await expect(page.getByTestId('chat-lark-unverified')).toContainText(/not verified/i)
    await expect(panel).toContainText(/IP allowlist/)
    await expectEnglish(panel)
  })

  test('Telegram:打开、填写、保存;带 SQL 默认关着并写明语句会到 Telegram 的服务器;机密存完不留在框里', async ({ page }) => {
    const s = await openSettings(page)
    await page.getByTestId('chat-telegram-enabled').click()
    const panel = page.getByTestId('chat-panel')
    const sql = page.getByTestId('chat-telegram-sql')
    await expect(sql).not.toHaveClass(/on/)
    await expect(panel).toContainText('语句会发到 Telegram 的服务器')
    await expect(panel).toContainText('关着时,卡片上没有语句、原因')
    await expect(page.getByTestId('chat-telegram-proxy')).toHaveAttribute('placeholder', /socks5/)
    for (const id of ['token', 'proxy']) await expect(page.getByTestId(`chat-telegram-${id}`)).toHaveAttribute('type', 'password')
    await page.getByTestId('chat-telegram-token').fill(' 123456789:AAbb ')
    await page.getByTestId('chat-telegram-proxy').fill(' socks5://u:p@10.0.0.9:1080 ')
    await sql.click()
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(1)
    expect(chatPart(s.saved[0])).toEqual({
      'chat.cardLang': 'zh', ...LARK_OFF_PART,
      'chat.telegram.enabled': true, 'chat.telegram.botToken': '123456789:AAbb', 'chat.telegram.includeSql': true, 'chat.telegram.proxyToken': 'socks5://u:p@10.0.0.9:1080',
    })
    for (const id of ['token', 'proxy']) {
      await expect(page.getByTestId(`chat-telegram-${id}`)).toHaveValue('')
      await expect(page.getByTestId(`chat-telegram-${id}`)).toHaveAttribute('placeholder', /已配置/)
      await expect(page.getByTestId(`chat-telegram-${id}-clear`)).toBeVisible()
    }
    await page.getByRole('button', { name: '保存设置' }).click()
    await expect.poll(() => s.saved.length).toBe(2)
    expect(s.saved[1]).toMatchObject({ [TG_TOKEN]: '', [TG_PROXY]: '', 'chat.telegram.includeSql': true }) // 留空 = 不改
  })

  test('Telegram:两个机密分开清除,各传各的 null;读回来的开关照旧', async ({ page }) => {
    const s = await openSettings(page, { settings: { ...TG_ON, 'chat.telegram.includeSql': 'true' }, secrets: [TG_TOKEN, TG_PROXY] })
    await expect(page.getByTestId('chat-telegram-sql')).toHaveClass(/on/)
    await page.getByTestId('chat-telegram-token').fill('123456789:new')
    await page.getByTestId('chat-telegram-proxy-clear').click()
    await page.getByTestId('confirm-ok').click()
    await expect.poll(() => s.saved).toEqual([{ [TG_PROXY]: null }])
    await expect(page.getByTestId('chat-telegram-proxy-clear')).toHaveCount(0)
    await expect(page.getByTestId('chat-telegram-token')).toHaveValue('123456789:new') // 另一个不受影响
    await page.getByTestId('chat-telegram-token-clear').click()
    await page.getByTestId('confirm-ok').click()
    await expect.poll(() => s.saved).toEqual([{ [TG_PROXY]: null }, { [TG_TOKEN]: null }])
    await expect(page.getByTestId('chat-telegram-token')).toHaveValue('')
    await expect(page.getByTestId('chat-telegram-token-clear')).toHaveCount(0)
  })

  test('Telegram:只配了令牌时,代理没有「清除」;Lark 的也不受影响', async ({ page }) => {
    await openSettings(page, { settings: { ...ON, ...TG_ON }, secrets: [TG_TOKEN] })
    await expect(page.getByTestId('chat-telegram-token-clear')).toBeVisible()
    await expect(page.getByTestId('chat-telegram-proxy-clear')).toHaveCount(0)
    await expect(page.getByTestId('chat-lark-secret-clear')).toHaveCount(0)
  })

  test('Telegram:清除被拒(开着时令牌不能清)说出原因,仍算已配置', async ({ page }) => {
    await openSettings(page, { settings: TG_ON, secrets: [TG_TOKEN], save: () => fail(40001, '启用 Telegram 审批机器人必须填写 Bot Token') })
    await page.getByTestId('chat-telegram-token-clear').click()
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByText('启用 Telegram 审批机器人必须填写 Bot Token')).toBeVisible()
    await expect(page.getByTestId('chat-telegram-token-clear')).toBeVisible()
  })

  test('两个渠道的状态与测试卡片各管各的;冲突说的是机器人在别处也连着', async ({ page }) => {
    const s = await openSettings(page, {
      settings: { ...ON, ...TG_ON }, secrets: [LARK_SECRET, TG_TOKEN],
      status: { enabled: true, state: 'connected', detail: '', botName: '' },
      tg: { enabled: true, state: 'conflict', detail: 'telegram: getUpdates: code 409: Conflict', botName: 'aegis_appr_bot' },
    })
    await expect(page.getByTestId('chat-lark-status')).toHaveText('已连接')
    const st = page.getByTestId('chat-telegram-status')
    await expect(st).toContainText('冲突:这个机器人在别处也连着')
    await expect(st).toContainText('code 409')
    s.tg.value = { enabled: true, state: 'error', detail: '这个机器人设置了 webhook,网关收不到消息;请先在原来使用它的地方删除 webhook', detailRef: { code: 'm8d8b1127' }, botName: '' }
    await expect(st).toContainText('删除 webhook', { timeout: 10_000 })
    await page.getByTestId('chat-telegram-test').click()
    await expect(page.getByTestId('chat-telegram-msg')).toHaveText('已发送,请到 Telegram 里查看。')
    await expect(page.getByTestId('chat-lark-msg')).toHaveText('')
    expect(s.tests).toEqual([{ channel: 'telegram' }])
  })

  test('英文界面:Telegram 这一块全是英文,webhook 的原因也翻译', async ({ page }) => {
    await english(page)
    await openSettings(page, {
      settings: TG_ON, secrets: [TG_TOKEN],
      tg: { enabled: true, state: 'error', detail: '这个机器人设置了 webhook,网关收不到消息;请先在原来使用它的地方删除 webhook', detailRef: { code: 'm8d8b1127' }, botName: '' },
    })
    const panel = page.getByTestId('chat-panel')
    await expect(page.getByTestId('chat-telegram-status')).toContainText('This bot has a webhook set')
    await expect(panel).toContainText(/statements are sent to Telegram's servers/)
    await expectEnglish(panel)
  })
})

// ---------------------------------------------------------------- 审批页、通知、用户管理

const XSS = '<img src=x onerror="window.__pwned=1">先备份'

async function openApprovals(page: Page) {
  const row = (over: object) => ({
    connectionId: 1, env: 'prod', tierCode: 'prod', instance: 'prod-order-01', database: 'orders', type: 'cmd',
    command: 'DROP TABLE orders_2024_q3;', keyword: 'DROP', riskLevel: 'high', initiatorId: 2, initiator: 'Chen Hao', reason: 'cleanup',
    createdAt: '2026-11-25T08:00:00Z', decidedAt: '2026-11-25T09:00:00Z', steps: [], result: '', resultRows: 0,
    canDecide: false, canExecute: false, canCancel: false, decidedVia: 'console', rejectReason: '', ...over,
  })
  const items = [
    row({ id: 1, apNo: 'AP-9201', status: 'rejected', decidedVia: 'lark', rejectReason: XSS }),
    row({ id: 2, apNo: 'AP-9202', status: 'rejected' }),
    row({ id: 3, apNo: 'AP-9203', status: 'approved', decidedVia: 'telegram' }),
    row({ id: 4, apNo: 'AP-9204', status: 'cancelled', decidedVia: 'lark' }),
  ]
  await stubCommon(page)
  await page.route((u) => u.pathname.endsWith('/api/v1/approvals'), (r) =>
    r.fulfill(envelope({ items, total: items.length, pending: 0, page: 1, pageSize: 20 })))
  await page.goto('/#/approvals')
  return (no: string) => page.locator('.tr', { hasText: no }).click()
}
const closeCard = (page: Page) => page.locator('.dcard .dx').click()

test.describe('审批页', () => {
  test('驳回的单显示拒绝原因与决定渠道,原因只当文字', async ({ page }) => {
    const open = await openApprovals(page)
    await expect(page.locator('.tr', { hasText: 'AP-9201' }).locator('.stbadge')).toHaveAttribute('title', XSS)
    await open('AP-9201')
    await expect(page.getByTestId('ap-reject-reason')).toHaveText(XSS)
    await expect(page.getByTestId('ap-decided-via')).toHaveText('经 Lark 审批')
    expect(await page.evaluate(() => (window as any).__pwned)).toBeUndefined()
    await expect(page.locator('.dcard img')).toHaveCount(0)
    await closeCard(page)
    await open('AP-9202') // 站内驳回、没有原因:两样都不显示
    await expect(page.locator('.dcard')).toBeVisible()
    await expect(page.getByTestId('ap-reject-reason')).toHaveCount(0)
    await expect(page.getByTestId('ap-decided-via')).toHaveCount(0)
    await closeCard(page)
    await open('AP-9203')
    await expect(page.getByTestId('ap-decided-via')).toHaveText('经 Telegram 审批')
    await closeCard(page)
    await open('AP-9204') // 通过之后又撤回的:这张单的结局不是在聊天里定的
    await expect(page.locator('.dcard')).toBeVisible()
    await expect(page.getByTestId('ap-decided-via')).toHaveCount(0)
  })

  test('英文', async ({ page }) => {
    await english(page)
    const open = await openApprovals(page)
    await open('AP-9201')
    await expect(page.getByTestId('ap-decided-via')).toHaveText('Decided via Lark')
    await expect(page.locator('.dcard')).toContainText('Reason for rejecting')
  })
})

test('铃铛:绑定 / 解绑两类通知各有图标', async ({ page }) => {
  await stubCommon(page)
  await page.route('**/api/v1/dashboard**', (r) => r.fulfill(envelope({})))
  await page.route('**/api/v1/notifications*', (r) => r.fulfill(envelope({ unread: 0, items: [
    { id: 1, type: 'chat-bound', title: '已绑定审批机器人', body: 'x', refNo: '', read: true },
    { id: 2, type: 'chat-unbound', title: '审批机器人已解绑', body: 'x', refNo: '', read: true },
  ] })))
  await page.goto('/#/dashboard')
  await page.locator('.notif .bell').click()
  const items = page.locator('.notif .pitem')
  await expect(items.nth(0).locator('svg.lucide-link2')).toHaveCount(1)
  await expect(items.nth(1).locator('svg.lucide-unlink2')).toHaveCount(1)
})

async function openUser(page: Page) {
  const calls: string[] = []
  await stubCommon(page)
  await page.route('**/api/v1/users', (r) => r.fulfill(envelope([
    { id: 2, name: 'Chen Hao', email: 'chenhao@aegisdb.io', initials: 'CH', dept: 'Dev', roles: ['RO'], roleIds: [4], primaryRoleId: 4, status: 'active', kind: 'human', mfaEnabled: false, lastActive: '—', ssoBound: false },
  ])))
  await page.route('**/api/v1/roles', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/users/2/tags', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/users/2/chat-bindings', (r) => { calls.push(r.request().method()); return r.fulfill(envelope({ ok: true })) })
  await page.goto('/#/admin/permissions')
  await page.locator('.vtabs .vt').nth(1).click()
  await page.locator('button.manage').first().click()
  return calls
}

test.describe('用户管理', () => {
  test('管理员替人解绑要确认', async ({ page }) => {
    const calls = await openUser(page)
    await page.getByTestId('user-chat-unbind').click()
    await expect(page.getByTestId('confirm-message')).toContainText('Chen Hao')
    await page.getByTestId('confirm-cancel').click()
    expect(calls).toEqual([])
    await page.getByTestId('user-chat-unbind').click()
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('user-chat-msg')).toBeVisible()
    expect(calls).toEqual(['DELETE'])
  })

  test('窗口矮的时候也够得到', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 480 })
    await openUser(page)
    await page.getByTestId('user-chat-unbind').click({ timeout: 5000 })
    await expect(page.getByTestId('confirm-dialog')).toBeVisible()
  })

  test('英文', async ({ page }) => {
    await english(page)
    await openUser(page)
    await expectEnglish(page.getByTestId('user-chat-sec'))
    await page.getByTestId('user-chat-unbind').click()
    await expectEnglish(page.getByTestId('confirm-dialog'))
  })
})
