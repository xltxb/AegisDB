// 收单检查的条目归并。
//
// 后端按「每条语句一条结论」给出 issues。几百条 DDL 的脚本里,绝大多数是同一句
// "ALTER 语句无法在不执行的前提下校验语法…未做语法检查",一条一行就是几百行
// (2026-11-23 用户:收单检查内容太长了)。这里把**结论完全相同**的条目并成一行,
// 语句序号压成区间(第 1–5、12 条),并按严重程度排序:错误在前,没做的检查在后。
//
// 只并「说的是同一件事」的条目 —— 级别、种类、对象、来源、渲染后的文字全部相同。
// 对象不同的(t_a 不存在 / t_b 不存在)仍各占一行:那是不同的问题,不能被压成一个数。

import type { PrecheckIssue } from '@/types'

export interface PrecheckGroup {
  level: PrecheckIssue['level']
  object?: string
  source?: string
  message: string
  /** 涉及的语句序号,升序去重;0 表示不针对某一条 */
  stmts: number[]
}

const levelRank: Record<string, number> = { error: 0, warn: 1, info: 2 }

export function groupPrecheckIssues(issues: PrecheckIssue[], text: (i: PrecheckIssue) => string): PrecheckGroup[] {
  const by = new Map<string, PrecheckGroup & { first: number }>()
  issues.forEach((i, idx) => {
    const message = text(i)
    const key = JSON.stringify([i.level, i.kind, i.object || '', i.source || '', message])
    let g = by.get(key)
    if (!g) {
      g = { level: i.level, object: i.object, source: i.source, message, stmts: [], first: idx }
      by.set(key, g)
    }
    g.stmts.push(i.stmt || 0)
  })
  return [...by.values()]
    .map((g) => ({ ...g, stmts: [...new Set(g.stmts)].sort((a, b) => a - b) }))
    // 严重程度优先;同级保持后端给出的顺序(它按语句顺序排)
    .sort((a, b) => (levelRank[a.level] ?? 3) - (levelRank[b.level] ?? 3) || a.first - b.first)
    .map(({ first: _f, ...g }) => g)
}

/**
 * 把语句序号压成区间串:[1,2,3,5,7,8] → "1–3、5、7–8"。
 * 区间段超过 maxRuns 时只列前面的段,其余用 "…" 收尾(总条数另行显示)。
 */
export function formatStmtRuns(stmts: number[], sep = '、', maxRuns = 8): string {
  const ns = stmts.filter((n) => n > 0)
  const runs: string[] = []
  for (let i = 0; i < ns.length; ) {
    let j = i
    while (j + 1 < ns.length && ns[j + 1] === ns[j] + 1) j++
    runs.push(j > i ? `${ns[i]}–${ns[j]}` : `${ns[i]}`)
    i = j + 1
  }
  if (runs.length > maxRuns) return runs.slice(0, maxRuns).join(sep) + sep + '…'
  return runs.join(sep)
}
