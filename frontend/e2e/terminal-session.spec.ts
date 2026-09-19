import { readFileSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { stubTerminal } from './fixtures'
import { installWsFake, ROWS_OUTPUT, type WsFake } from './wsFake'

// 终端会话唯一走得完整条路的地方。
//
// 单元测试(tests/unit)在 Node 里跑纯逻辑,碰不到 effect;而这条路上出问题的方式
// ——「偶尔断连」「切实例后编辑器一直 busy」——都是 effect 与 socket 生命周期
// 的事。所以它必须在真浏览器里、对着一条真的(被替身顶掉的)WebSocket 跑。
//
// 替身而不是真后端:e2e 的前提是「纯前端、几秒钟」,起一个 Go 网关加一个库会把
// 这个前提换掉。而 StrictMode 只在 npm run dev 里有,所以这一层反而是唯一能
// 白拿到 effect 双跑的地方 —— 下面第一条坑正是它。

const term = (page: Page) => page.locator('.xterm-rows')
const dot = (page: Page) => page.locator('.tv-dot')
const exportBtn = (page: Page) => page.getByRole('button', { name: '导出日志' })

async function openSession(page: Page): Promise<WsFake> {
  const fake = await installWsFake(page)
  await stubTerminal(page)
  await page.goto('/terminal')
  await expect(page.locator('.tv-tree')).toBeVisible()
  // 开场白到屏幕上,才算这条会话真的起来了。
  await expect(term(page)).toContainText('# 语句以 ; 结束并执行', { timeout: 10_000 })
  return fake
}

/** 往终端里敲字。 */
async function type(page: Page, text: string) {
  await page.locator('.xterm').first().click()
  await page.keyboard.type(text)
}
async function submit(page: Page, sql: string) {
  await type(page, sql)
  await page.keyboard.press('Enter')
}

test.describe('终端会话 · 一条路走完', () => {
  test('连接 → 键入 → 提交 → 回执 → 落屏落日志 → 断开 → 重连', async ({ page }) => {
    const fake = await openSession(page)

    // 开场白点名这条会话连的是谁、什么角色、什么策略。
    await expect(term(page)).toContainText('sandbox')
    await expect(term(page)).toContainText('ro')
    await expect(term(page)).toContainText('strict')
    await expect(page.locator('.tv-target')).toHaveText('dev-sandbox')

    await submit(page, 'select id,name from users;')

    // 断言**真正送进网关的那一帧**,不只是屏幕。屏幕对了而帧错了(比如漏了
    // database,语句就悄悄跑到别的库上),正是最难手点出来的一类回归。
    const exec = await fake.waitFor('exec')
    expect(exec).toMatchObject({
      type: 'exec', connectionId: 1, sql: 'select id,name from users', database: 'appdb',
    })

    fake.send(ROWS_OUTPUT)

    // 落屏:默认视图下表是画进终端里的。
    await expect(term(page)).toContainText('alice')
    // 同一批行还要喂给 HTML 结果表 —— 那是另一条渲染路径(setResult),默认关着,
    // 开关一开就该有内容,而不是等下一条语句才补上。
    await page.getByTitle('表格视图').click()
    await expect(page.locator('.rg')).toContainText('alice')
    // 落日志:导出按钮从灰变亮,说明这次会话有东西可导。
    await expect(exportBtn(page)).toBeEnabled()

    // 断开。offline 把它稳住 —— 否则 1 秒后的自动重连会和这个断言赛跑。
    fake.setOffline(true)
    await expect(dot(page)).toHaveClass(/closed/)

    // 重连。
    fake.setOffline(false)
    const before = fake.stats().opened
    await page.getByTitle('重连会话').click()
    await expect(dot(page)).toHaveClass(/open/)
    expect(fake.stats().opened).toBeGreaterThan(before)

    // 重连之后这条会话仍然能用 —— 断了一次不该把终端留在半死状态。
    await submit(page, 'select 2;')
    await fake.waitFor('exec', 2)
  })
})

test.describe('终端会话 · 踩过的坑', () => {
  /*
   * StrictMode 下建立终端的那个 effect 跑两遍,所以每一样东西都必须在清理函数里
   * 还回去。socket 漏掉的话,两条各收一半消息 —— 那正是「终端偶尔断连」的一种成因。
   *
   * 判据是**同时存活只有一条**,不是「只开一条」:StrictMode 下必然开两条
   * (挂载 → 清理 → 再挂载),断言开一条是误报。
   */
  test('StrictMode 把终端建了两遍,但同时只有一条 socket 活着', async ({ page }) => {
    const fake = await openSession(page)

    // 真的建了不止一次 —— 否则下面那句就什么都没验。
    expect(fake.stats().opened).toBeGreaterThan(1)
    expect(fake.stats().peakLive).toBe(1)
  })

  /*
   * 开场白只打一次。
   *
   * 护着这件事的是两样东西:`banneredFor` 那道闸,以及开场白 effect **有意写窄**
   * 的依赖数组。写宽了,换个主题、拖一下分栏这类与会话无关的重渲染都会把它再打
   * 一遍(index.tsx 的注释记了这件事)。
   *
   * 所以这里**必须制造一次与会话无关的重渲染**再数:不制造的话,那个 effect 从
   * 头到尾只会被触发一次(挂载时实例列表还没回来,connKey 还是 0,StrictMode 的
   * 两遍都在第一行就 return 了),这条规格就成了一条永远绿的装饰。
   */
  test('与会话无关的重渲染不会把开场白再打一遍', async ({ page }) => {
    await openSession(page)

    // 制造重渲染,而且是最真实的那种:**用这个终端**。每敲一个字都会更新补全状态,
    // 整页跟着重渲染一次。再改一次窗口大小。这些都与「这条会话连的是谁」无关。
    await type(page, 'sel')
    await page.setViewportSize({ width: 1100, height: 800 })
    await page.setViewportSize({ width: 1280, height: 800 })

    // 数开场白只能数帮助行:实例名在提示符里每行都出现,数它会把 1 段开场白数成 3 次。
    const screen = await term(page).innerText()
    expect(screen.split('# 语句以 ; 结束并执行').length - 1).toBe(1)
  })

  /*
   * 语句在途时 socket 断掉,应答永远不会来。编辑器若停在 busy,之后每个按键都被
   * 吞进粘贴队列(lib/lineEditor.ts 的 handleData),终端看着就像死了 —— 而这正是
   * 「切实例后编辑器一直 busy」那类报告的现场。
   *
   * 放它出来的是 useTerminalSession 里 onStatus('closed') → editor.resume()。
   */
  test('语句在途时断连,编辑器被放出来而不是永远 busy', async ({ page }) => {
    const fake = await openSession(page)

    await submit(page, 'select pg_sleep(60);')
    await fake.waitFor('exec')

    // 不回执,直接断。
    fake.setOffline(true)
    await expect(dot(page)).toHaveClass(/closed/)

    // 断线之后敲的字必须看得见。看不见就说明按键被吞进了队列。
    await type(page, 'select 1')
    await expect(term(page)).toContainText('select 1')
  })

  /*
   * 换实例就是换一次会话。屏幕和日志必须一起清:只清日志会留下一个**看得见却
   * 导不出**的落差 —— 屏幕上还挂着上一台的输出,而导出的文件从新开场白才开始,
   * 头部却只写着当前这台。两者要说同一件事。
   *
   * 所以这条规格两半都验。只断言屏幕就只验了一半,而漏掉的那一半恰恰是会被写进
   * 审计、事后拿来当凭据的那一份。
   */
  test('切实例把屏幕和日志一起清,不留上一台的输出', async ({ page }) => {
    const fake = await openSession(page)

    // 在 sandbox 上留下一条看得出是它的输出。
    await submit(page, 'select id,name from users;')
    await fake.waitFor('exec')
    fake.send(ROWS_OUTPUT)
    await expect(term(page)).toContainText('alice')

    // 切到第二台。
    await page.locator('.tv-inst').filter({ hasText: 'staging' }).click()
    await expect(page.locator('.tv-target')).toHaveText('dev-staging')

    // 屏幕这一半:新开场白在,上一台的输出没了。
    await expect(term(page)).toContainText('staging')
    await expect(term(page)).not.toContainText('alice')

    // 日志那一半:在新实例上跑一条,然后把日志导出来看。
    await submit(page, 'select 1;')
    await fake.waitFor('exec', 2)
    fake.send({ type: 'output', text: 'ok', ms: 3 })
    await expect(exportBtn(page)).toBeEnabled()

    const [download] = await Promise.all([
      page.waitForEvent('download'),
      exportBtn(page).click(),
    ])
    const file = await download.path()
    const text = readFileSync(file, 'utf8')
    expect(text).toContain('staging')
    expect(text).not.toContain('alice')
  })
})
