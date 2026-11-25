import { test, expect, type Page } from '@playwright/test'

// 变更发布页的多语言。
//
// 用户在英文界面看 REL-1,流水线上的「规范审查」「执行变更」还是中文:阶段名是后端按类型
// 派生的**规范中文**(service.stageTypeLabel —— 它进记录,所以语言稳定),界面应按类型
// 用 plType_* 显示;流程编辑页和待办早就这么做了,只有发布页直接摆了库里那份中文。
//
// 原因、标题这类是调用方 / 发起人填的数据,两种语言下都原样显示。

import { envelope, stubCommon } from './helpers'

function release(stages: Array<{ name: string; type: string; status: string }>, risk = 'low') {
  return {
    id: 1, relNo: 'REL-1', title: 'abiu-test-orderID=16113', pipelineId: 1, pipelineName: 'fat',
    connectionId: 7, instance: 'fat-tidb', database: 'yone', env: 'fat', tierCode: 'dev',
    engine: 'tidb', changeType: 'dml', sql: 'update t_bond set bond_type=1 where bondseq_id=855048;',
    reason: '[FAT] DML 自动执行,来源 UpgradeOrder#16113',
    creatorId: 1, creator: 'Lin Wei', source: 'api', status: 'success', risk, error: '',
    createdAt: '2026-10-30T10:16:25+08:00', startedAt: null, finishedAt: null,
    stages: stages.map((s, i) => ({
      id: i + 1, releaseId: 1, stepOrder: i + 1, log: '', config: '', onFailure: 'abort', ...s,
    })),
  }
}

const STAGES = [
  { name: '规范审查', type: 'review', status: 'success' },
  { name: '人工审批', type: 'approve', status: 'skipped' },
  { name: '执行变更', type: 'execute', status: 'waiting' },
  { name: '执行后校验', type: 'verify', status: 'pending' },
  { name: '结果通知', type: 'notify', status: 'failed' },
]

async function openReleases(page: Page, lang: 'zh' | 'en', rel = release(STAGES)) {
  await page.addInitScript((l) => localStorage.setItem('aegisdb_lang', l), lang)
  await stubCommon(page)
  await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([])))
  await page.route('**/api/v1/releases?*', (r) => r.fulfill(envelope({ items: [rel], total: 1, page: 1, pageSize: 50 })))
  await page.route(/\/api\/v1\/releases\/\d+$/, (r) => r.fulfill(envelope(rel)))
  await page.goto('/#/releases')
  await page.locator('.node').first().waitFor({ state: 'visible', timeout: 15000 })
}

const CJK = /[一-鿿]/

test.describe('变更发布 · 多语言', () => {
  test('英文界面:流水线阶段名、阶段状态、风险都是英文', async ({ page }) => {
    await openReleases(page, 'en')
    const names = await page.locator('.node .nname').allInnerTexts()
    expect(names.map((s) => s.trim())).toEqual(['SQL review', 'Approval', 'Execute', 'Verify', 'Notify'])
    const states = await page.locator('.node .nst').allInnerTexts()
    expect(states.map((s) => s.trim())).toEqual(['Done', 'Skipped', 'Waiting', 'Queued', 'Failed'])
    await expect(page.locator('.dhead .pill')).toHaveText('Risk Low')
    await expect(page.locator('.ritem .pill')).toHaveText('Done')

    // 阶段详情的标题同样按类型
    await page.locator('.node', { hasText: 'Execute' }).click()
    await expect(page.locator('.sbox .sbt')).toHaveText('Execute')
    await expect(page.locator('.sbox .pill')).toHaveText('Waiting')
    await expect(page.locator('.sbox')).toContainText('Confirm & execute')

    // 整条流水线与详情头里不该再有中文
    expect(CJK.test(await page.locator('.pipe').innerText())).toBe(false)
    expect(CJK.test(await page.locator('.sbox .sbhead').innerText())).toBe(false)
    // 原因是调用方填的:原样显示,标签是英文
    await expect(page.locator('.reason')).toHaveText('Reason: [FAT] DML 自动执行,来源 UpgradeOrder#16113')
  })

  test('中文界面:阶段名、状态、风险是中文', async ({ page }) => {
    await openReleases(page, 'zh', release(STAGES, 'high'))
    const names = await page.locator('.node .nname').allInnerTexts()
    expect(names.map((s) => s.trim())).toEqual(['规范审查', '人工审批', '执行变更', '执行后校验', '结果通知'])
    const states = await page.locator('.node .nst').allInnerTexts()
    expect(states.map((s) => s.trim())).toEqual(['已完成', '已跳过', '等待处理', '待开始', '失败'])
    await expect(page.locator('.dhead .pill')).toHaveText('风险 高危')
  })

  test('老快照里自定义的阶段名是用户数据,两种语言都原样显示', async ({ page }) => {
    await openReleases(page, 'en', release([
      { name: 'DBA 复核', type: 'manual', status: 'success' },
      { name: '', type: 'execute', status: 'success' },
    ]))
    const names = await page.locator('.node .nname').allInnerTexts()
    expect(names.map((s) => s.trim())).toEqual(['DBA 复核', 'Execute'])
  })

  test('新建发布的预审结果:内置规则名按读者语言显示', async ({ page }) => {
    await openReleases(page, 'en')
    await page.route('**/api/v1/connections', (r) => r.fulfill(envelope([
      { id: 7, name: 'fat-tidb', env: 'fat', engine: 'tidb', database: 'yone' },
    ])))
    await page.route('**/api/v1/connections/7/schema*', (r) => r.fulfill(envelope({ databases: [{ name: 'yone', tables: [] }] })))
    await page.route('**/api/v1/sql-review/check', (r) => r.fulfill(envelope({
      result: {
        dialect: 'mysql', statements: 1, errors: 1, warnings: 0, infos: 0, passed: false,
        findings: [{ code: 'dml.require.where', name: 'UPDATE/DELETE 必须带 WHERE', level: 'error', category: 'dml',
          stmt: 1, line: 1, sql: 'delete from t', message: 'x' }],
      },
    })))
    await page.reload()
    await page.locator('.node').first().waitFor({ state: 'visible', timeout: 15000 })
    await page.getByRole('button', { name: 'New release' }).click()
    await page.locator('textarea.sqlbox').fill('delete from t')
    await page.getByRole('button', { name: 'Pre-check' }).click()
    await expect(page.locator('.pv .pvline')).toContainText('UPDATE/DELETE must have a WHERE clause')
  })
})

// 阶段日志与错误条(迁移 0059):服务端存中文原串,同一段话另带逐行 RuleRef,界面按语言渲染;
// 老单没有 refs,显示存下的中文原串。
const EXEC_LOG = '· 执行完成 · 1 条语句 · 影响 1 行\n· [1/1] 执行成功 · 1 行受影响 (12ms)\n    update t_bond set bond_type=1 where bondseq_id=855048;'
const EXEC_REFS = JSON.stringify([
  { code: 'execDone', args: { n: '1', rows: '1' } },
  { code: 'stmtOk', args: { pos: '1', total: '1', ms: '12', out: '执行成功 · 1 行受影响', ns: 'outText' }, parts: [{ code: 'execAffected', args: { n: '1' } }] },
  { code: 'raw', args: { text: '    update t_bond set bond_type=1 where bondseq_id=855048;' } },
])

function withLogs(refs: boolean) {
  const rel = release([
    { name: '规范审查', type: 'review', status: 'success' },
    { name: '执行变更', type: 'execute', status: 'failed' },
  ]) as Record<string, any>
  rel.status = 'failed'
  rel.error = '阶段「执行变更」失败: · 已执行 0/1 条后中止 · 第 1 条失败: boom'
  rel.stages[1].log = EXEC_LOG
  if (refs) {
    rel.stages[1].logRefs = EXEC_REFS
    rel.errorRef = JSON.stringify({
      code: 'errStageFailed', args: { type: 'execute', stage: '执行变更', log: '· 已执行 0/1 条后中止 · 第 1 条失败: boom' },
      parts: [{ code: 'haltedAtFail', args: { done: '0', total: '1', pos: '1', out: 'boom' } }],
    })
  }
  return rel as ReturnType<typeof release>
}

test.describe('变更发布 · 阶段日志与错误条的多语言', () => {
  test('英文界面:执行变更日志、错误条是英文,语句回显原样', async ({ page }) => {
    await openReleases(page, 'en', withLogs(true))
    await expect(page.locator('.errbar')).toHaveText('Stage “Execute” failed: · Stopped after 0/1 statement(s) · statement 1 failed: boom')
    await page.locator('.node', { hasText: 'Execute' }).click()
    const log = page.locator('.sbox pre.log')
    await expect(log).toContainText('· Done · 1 statement(s) · 1 row(s) affected')
    await expect(log).toContainText('· [1/1] Executed successfully · 1 row(s) affected (12ms)')
    await expect(log).toContainText('update t_bond set bond_type=1 where bondseq_id=855048;')
    expect(CJK.test(await log.innerText())).toBe(false)
  })

  test('中文界面:日志与存下的原串一致', async ({ page }) => {
    await openReleases(page, 'zh', withLogs(true))
    await page.locator('.node', { hasText: '执行变更' }).click()
    await expect(page.locator('.sbox pre.log')).toHaveText(EXEC_LOG)
    await expect(page.locator('.errbar')).toHaveText('阶段「执行变更」失败: · 已执行 0/1 条后中止 · 第 1 条失败: boom')
  })

  test('老单(没有 refs):英文界面显示存下的中文原串,不留白', async ({ page }) => {
    await openReleases(page, 'en', withLogs(false))
    await page.locator('.node', { hasText: 'Execute' }).click()
    await expect(page.locator('.sbox pre.log')).toHaveText(EXEC_LOG)
    await expect(page.locator('.errbar')).toContainText('阶段「执行变更」失败')
  })
})
