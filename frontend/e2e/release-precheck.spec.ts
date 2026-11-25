import { test, expect, type Page } from '@playwright/test'

// 发布单上的「收单检查」。
//
// 这块面板存在的全部理由,是让**后面来看这张单的人**(审批的、执行的、出事回头查的)
// 知道收单那一刻查到了什么。所以这组用例盯的不是"有没有显示",而是三件最容易骗人的事:
//
//   1. **覆盖范围要和结论一起出现。** 只说"0 错误"会被读成"每条都验过了",而 DDL 根本
//      没法在不执行的前提下验语法 —— 已检查/未检查两个数必须同屏。
//   2. **对象结论要说清是怎么知道的。** 一份三天前的缓存说"表不存在",很可能只是那张表
//      是昨天建的。缓存的时间要摆出来。
//   3. **没有结果时不能留白。** 空白会被读成"检查通过"。老单子和"一条都没检查成"都要
//      有一句明确的话。

import { envelope, stubCommon } from './helpers'

const PRECHECK = {
  statements: 3,
  errors: 1, warnings: 0, infos: 2,
  syntaxChecked: 1, syntaxSkipped: 2,
  objectSource: 'cache',
  cacheSyncedAt: '2026-09-08T10:00:00+08:00',
  issues: [
    { stmt: 2, level: 'error', kind: 'object', object: 't_nowhere', message: '对象不存在', source: 'cache' },
    // 字段与约束两类也要能显示 —— 面板按 level 渲染,新种类不该需要改界面,
    // 但"不该需要"和"确实不需要"是两件事,这里验后者。
    { stmt: 4, level: 'error', kind: 'object', object: 't_order.memo', message: '字段已存在,这条加列语句会失败', source: 'live' },
    { stmt: 5, level: 'error', kind: 'constraint', object: 't_order', message: '表中已有数据,加非空且无默认值的字段会失败', source: 'live' },
    { stmt: 1, level: 'info', kind: 'skipped', message: 'CREATE 语句无法在不执行的前提下校验语法(DDL 隐式提交或加表锁),未做语法检查' },
    { stmt: 3, level: 'info', kind: 'skipped', message: '引用对象缺失,语法检查已跳过(否则只会重复报同一件事)' },
  ],
}

function release(precheck: string | undefined) {
  return {
    id: 1, relNo: 'REL-1', title: '订单表订正', pipelineId: 1, pipelineName: '标准发布流程',
    connectionId: 7, instance: 'tongcha', database: 'appdb', env: 'prod', tierCode: 'prod',
    engine: 'mysql', changeType: 'ddl', sql: 'ALTER TABLE t ADD COLUMN c INT', reason: '',
    creatorId: 1, creator: 'Lin Wei', source: 'console', status: 'pending', risk: 'mid', error: '',
    createdAt: '2026-09-11T12:00:00+08:00', startedAt: null, finishedAt: null,
    precheck,
    stages: [{ id: 1, releaseId: 1, stepOrder: 1, name: '执行变更', type: 'execute', status: 'pending', log: '', config: '', onFailure: 'stop' }],
  }
}

async function openReleases(page: Page, precheck: string | undefined) {
  await stubCommon(page)
  await page.route('**/api/v1/projects', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [release(precheck)], total: 1, page: 1, pageSize: 50 })))
  await page.goto('/#/releases')
  await page.locator('.pbox').waitFor({ state: 'visible', timeout: 15000 })
}

const box = (page: Page) => page.locator('.pbox')

test.describe('发布单 · 收单检查', () => {
  test('结论旁边必须有覆盖范围,否则"0 错误"会被读成"每条都验过了"', async ({ page }) => {
    await openReleases(page, JSON.stringify(PRECHECK))

    await expect(box(page)).toContainText('1 错误')
    // 关键:已检查 1 条、未检查 2 条,和结论在同一块里。
    await expect(box(page)).toContainText('已检查 1 条')
    await expect(box(page)).toContainText('未检查 2 条')
  })

  test('对象结论说清来源,缓存的把同步时间摆出来', async ({ page }) => {
    await openReleases(page, JSON.stringify(PRECHECK))

    await expect(box(page)).toContainText('本地元数据缓存')
    await expect(box(page)).toContainText('缓存之后建的对象会被报成不存在')
    await expect(box(page)).toContainText('2026')       // 那份缓存的时间
    await expect(box(page)).toContainText('t_nowhere')  // 点名是哪个对象
  })

  test('字段与约束两类结论照样显示,并标成实时', async ({ page }) => {
    await openReleases(page, JSON.stringify(PRECHECK))

    await expect(box(page)).toContainText('字段已存在')
    await expect(box(page)).toContainText('t_order.memo')
    await expect(box(page)).toContainText('加非空且无默认值的字段会失败')
    // 字段与数据结论一律实时 —— 标签要跟着,不能含糊成缓存。
    await expect(box(page).locator('.pitem', { hasText: '字段已存在' })).toContainText('实时')
  })

  test('没做的检查照样列出来,不是从结果里消失', async ({ page }) => {
    await openReleases(page, JSON.stringify(PRECHECK))

    await expect(box(page)).toContainText('未做语法检查')
    await expect(box(page)).toContainText('语法检查已跳过')
  })

  test('没有结果时给一句明确的话,而不是留白 —— 留白会被读成通过', async ({ page }) => {
    await openReleases(page, undefined)

    await expect(box(page)).toContainText('没有收单检查结果')
    await expect(box(page)).toContainText('不代表检查通过')
  })

  test('结果损坏时当作没有,不猜它的形状', async ({ page }) => {
    await openReleases(page, '{ 这不是 JSON')

    await expect(box(page)).toContainText('没有收单检查结果')
  })

  // 2026-11-23 用户:330 条 DDL 的脚本,收单检查是 300 行一模一样的"未做语法检查"。
  // 同一结论并成一行、序号压成区间;错误排最前;超过 6 行先收起。
  test('同一结论并成一行,错误在前,长列表先收起', async ({ page }) => {
    const ALTER = 'ALTER 语句无法在不执行的前提下校验语法(DDL 隐式提交或加表锁),未做语法检查'
    const CREATE = 'CREATE 语句无法在不执行的前提下校验语法(DDL 隐式提交或加表锁),未做语法检查'
    const issues: object[] = []
    for (let n = 1; n <= 300; n++) {
      issues.push({ stmt: n, level: 'info', kind: 'skipped', message: n <= 5 || n > 11 ? ALTER : CREATE })
    }
    issues.push({ stmt: 120, level: 'error', kind: 'object', object: 't_gone', message: '对象不存在', source: 'cache' })
    for (const [i, o] of ['t_a', 't_b', 't_c', 't_d', 't_e', 't_f', 't_g'].entries()) {
      issues.push({ stmt: 200 + i, level: 'warn', kind: 'object', object: o, message: '表已存在', source: 'live' })
    }
    const pc = { ...PRECHECK, statements: 330, errors: 1, warnings: 7, syntaxChecked: 0, syntaxSkipped: 300, issues }
    await openReleases(page, JSON.stringify(pc))

    const rows = box(page).locator('.pitem')
    await expect(rows).toHaveCount(6)                       // 1 错误 + 7 警告 + 2 未检查 = 10 组,先显示 6
    await expect(rows.first()).toContainText('t_gone')      // 错误排最前
    await box(page).getByRole('button', { name: '展开其余 4 项' }).click()
    await expect(rows).toHaveCount(10)
    // 对象不同的警告各占一行,不被压成一个数
    await expect(box(page).locator('.pitem', { hasText: 't_f' })).toHaveCount(1)
    const alter = box(page).locator('.pitem', { hasText: ALTER })
    await expect(alter).toHaveCount(1)
    await expect(alter).toContainText('第 1–5、12–300 条')
    await expect(alter).toContainText('共 294 条')
    await expect(box(page).locator('.pitem', { hasText: CREATE })).toContainText('第 6–11 条')
    await box(page).getByRole('button', { name: '收起' }).click()
    await expect(rows).toHaveCount(6)
  })
})
