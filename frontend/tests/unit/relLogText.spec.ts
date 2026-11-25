import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'

import { renderRelLog, renderRelErr, type RelLogI18n } from '../../src/lib/relLogText'
import type { RuleRef } from '../../src/types'

// 发布阶段日志按读者语言渲染(rlLog.)。
//
// 用户在英文界面看 REL-1,执行变更的日志整段是中文 —— 日志是服务端拼好的中文。现在同一段话
// 另以逐行 RuleRef 下发;这里**读真实的词条文件**,挡的是"后端加了 code、前端没加文案"。
// 中文那份与服务端一字不差由后端 TestRelLogMatchesFrontendZh 核对。

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

function lookup(cat: Record<string, unknown>, key: string): string | undefined {
  let cur: unknown = cat
  for (const k of key.split('.')) {
    if (!cur || typeof cur !== 'object' || !(k in (cur as object))) return undefined
    cur = (cur as Record<string, unknown>)[k]
  }
  return typeof cur === 'string' ? cur : undefined
}

function i18nOf(locale: 'zh' | 'en'): RelLogI18n {
  const cat = catalogue(locale + '.json5')
  const t = (k: string, named?: Record<string, unknown>) =>
    (lookup(cat, k) ?? k).replace(/\{(\w+)\}/g, (_, n) => String(named?.[n] ?? ''))
  return {
    t, te: (k) => lookup(cat, k) !== undefined, locale,
    stage: (type) => t('plType_' + type),
    rule: (_code, stored) => stored,
  }
}

const en = i18nOf('en')
const zh = i18nOf('zh')

// 执行变更的一段典型日志:执行完成 + EXIT 尾注、逐条结果、语句回显(原样)。
const EXEC: RuleRef[] = [
  { code: 'autoOn' },
  { code: 'execDone', args: { n: '1', rows: '1' } },
  { code: 'execNote', args: { cat: '1', note: '在第 1 条 EXIT 处结束', ns: 'outText' }, parts: [{ code: 'flowExited', args: { pos: '1' } }] },
  { code: 'stmtOk', args: { pos: '1', total: '1', ms: '12', out: '执行成功 · 1 行受影响', ns: 'outText' }, parts: [{ code: 'execAffected', args: { n: '1' } }] },
  { code: 'raw', args: { text: '    update t_bond set bond_type=1 where bondseq_id=855048;' } },
]
const EXEC_ZH = '· 自动执行:分层允许自动执行,未经人工确认直接下发\n· 执行完成 · 1 条语句 · 影响 1 行 · 在第 1 条 EXIT 处结束\n'
  + '· [1/1] 执行成功 · 1 行受影响 (12ms)\n    update t_bond set bond_type=1 where bondseq_id=855048;'

test('英文界面:执行日志整段是英文,语句回显原样', () => {
  const out = renderRelLog(JSON.stringify(EXEC), EXEC_ZH, en)
  expect(out).not.toMatch(/[一-鿿]/)
  expect(out).toContain('· Done · 1 statement(s) · 1 row(s) affected · ')
  expect(out).toContain('· [1/1] Executed successfully · 1 row(s) affected (12ms)')
  expect(out.split('\n').pop()).toBe('    update t_bond set bond_type=1 where bondseq_id=855048;')
})

test('中文界面:渲染结果与服务端存的原串一字不差', () => {
  expect(renderRelLog(JSON.stringify(EXEC), EXEC_ZH, zh)).toBe(EXEC_ZH)
})

test('认不出的 code / 老单没有 refs / refs 损坏:整段回落到原串,不拼半句', () => {
  const refs = [...EXEC, { code: 'fromTheFuture' }]
  expect(renderRelLog(JSON.stringify(refs), EXEC_ZH, en)).toBe(EXEC_ZH)
  expect(renderRelLog(undefined, '老日志', en)).toBe('老日志')
  expect(renderRelLog('{oops', '老日志', en)).toBe('老日志')
})

test('嵌套那句话认不出时用它的原串,不丢内容(驱动报错本来也不翻)', () => {
  const refs: RuleRef[] = [{ code: 'stmtFail', args: { pos: '2', total: '3', ms: '5', out: 'ERROR 1146: Table missing', ns: 'outText' }, parts: [{ code: 'nope' }] }]
  expect(renderRelLog(JSON.stringify(refs), 'x', en)).toBe('· Statement 2/3 failed: ERROR 1146: Table missing (5ms)')
})

test('执行前复判:规则名按 ruleText 翻', () => {
  const refs: RuleRef[] = [{ code: 'recheckDeny', args: { rule: '能力矩阵 · 该环境禁止此操作', ns: 'ruleText' }, parts: [{ code: 'capDeny' }] }]
  const out = renderRelLog(JSON.stringify(refs), 'x', en)
  expect(out).not.toMatch(/[一-鿿]/)
  expect(out).toMatch(/^· Re-check before execution refused it \(.+\); nothing was executed$/)
})

test('在线变更进度:阶段名与尾巴都按语言', () => {
  const refs: RuleRef[] = [
    { code: 'oscProgressPct', args: { copied: '1,000', est: '2,000', replayed: '3', pct: '50' }, parts: [{ code: 'oscPh_copying' }] },
    { code: 'oscThrottled', args: { cat: '1' } },
  ]
  expect(renderRelLog(JSON.stringify(refs), 'x', en))
    .toBe('  Progress: copying · 1,000 / about 2,000 rows copied (about 50%) · 3 rows replayed · database busy, throttling')
  expect(renderRelLog(JSON.stringify(refs), 'x', zh))
    .toBe('  进度:拷贝 · 已拷贝 1,000 / 约 2,000 行(约 50%) · 已重放 3 行 · 库端繁忙,限流中')
})

test('错误条:阶段名按类型、失败原因取日志开头几行', () => {
  const ref: RuleRef = {
    code: 'errStageFailed', args: { type: 'execute', stage: '执行变更', log: '· 已执行 0/1 条后中止 · 第 1 条失败: boom' },
    parts: [{ code: 'haltedAtFail', args: { done: '0', total: '1', pos: '1', out: 'boom' } }],
  }
  expect(renderRelErr(JSON.stringify(ref), '阶段「执行变更」失败: …', en))
    .toBe('Stage “Execute” failed: · Stopped after 0/1 statement(s) · statement 1 failed: boom')
  expect(renderRelErr(JSON.stringify({ code: 'errAborted', args: { who: 'Lin Wei' } }), '已由 Lin Wei 终止', en)).toBe('Aborted by Lin Wei')
  expect(renderRelErr('', '老错误', en)).toBe('老错误')
})
