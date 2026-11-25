import { test, expect } from '@playwright/test'

import { reviewGroups, EXAMPLES_PER_RULE } from '../../src/lib/reviewGroups'
import type { ReviewFinding, ReviewResult } from '../../src/types'

// 审查结果按规则分组:每条规则一行 + 命中总数 + 前 5 条示例。
// 2026-11-10:50 万条命中逐条画,浏览器卡死;老结果(没有 rules)也要按同样方式收口。

const hit = (code: string, level: 'error' | 'warn' | 'info', stmt: number): ReviewFinding => ({
  code, name: code, level, category: 'dml', stmt, line: stmt, sql: `stmt ${stmt}`, message: `msg ${code}`,
})
const base = { dialect: 'mysql', statements: 0, errors: 0, warnings: 0, infos: 0, passed: true } as const

test('新结果:命中数取 rules,示例取 findings,错误排前面', () => {
  const r: ReviewResult = {
    ...base, passed: false, total: 20005,
    rules: [
      { code: 'ins.cols', name: 'ins.cols', level: 'warn', category: 'dml', count: 20000, message: 'm' },
      { code: 'dml.where', name: 'dml.where', level: 'error', category: 'dml', count: 5, message: 'm' },
    ],
    findings: [1, 2, 3, 4, 5].map((n) => hit('ins.cols', 'warn', n)).concat([6, 7, 8, 9, 10].map((n) => hit('dml.where', 'error', n))),
  }
  const g = reviewGroups(r)
  expect(g.map((x) => x.code)).toEqual(['dml.where', 'ins.cols'])
  expect(g[1].count).toBe(20000)
  expect(g[1].examples.map((f) => f.stmt)).toEqual([1, 2, 3, 4, 5])
  expect(g[1].more).toBe(19995)
  expect(g[0].more).toBe(0)
})

test('老结果(没有 rules):自己数,示例仍只取前 5 条', () => {
  const findings = Array.from({ length: 3000 }, (_, i) => hit('ins.cols', 'warn', i + 1)).concat([hit('dml.where', 'error', 9)])
  const g = reviewGroups({ ...base, findings })
  expect(g[0].code).toBe('dml.where')
  expect(g[1].count).toBe(3000)
  expect(g[1].examples.length).toBe(EXAMPLES_PER_RULE)
  expect(g[1].more).toBe(3000 - EXAMPLES_PER_RULE)
})

test('全部通过:没有分组', () => {
  expect(reviewGroups({ ...base, findings: [], rules: [], total: 0 })).toEqual([])
  expect(reviewGroups(null)).toEqual([])
})
