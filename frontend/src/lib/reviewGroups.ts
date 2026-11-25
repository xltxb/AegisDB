// 审查结果按规则分组展示:每条规则一行,带命中总数,下面挂前几条示例。
//
// 2026-11-10:一张 50 万条语句的脚本逐条展示,浏览器要画 50 万行直接卡死;后端改成每条规则
// 只带 5 条示例 + 命中总数(rules)。老的审查结果(之前存下的发布阶段)没有 rules,
// 这里从 findings 自己算,同样只取每条规则前 5 条示例 —— 老单再大也不会画出几十万行。

import type { ReviewFinding, ReviewLevel, ReviewResult, RuleRef } from '../types'

export const EXAMPLES_PER_RULE = 5

export interface ReviewGroup {
  code: string
  name: string
  level: ReviewLevel
  /** 命中总数 */
  count: number
  message: string
  messageRef?: RuleRef
  /** 前几条示例(最多 EXAMPLES_PER_RULE 条) */
  examples: ReviewFinding[]
  /** 没列出来的条数 = count - examples.length */
  more: number
}

const rank = (l: string) => (l === 'error' ? 0 : l === 'info' ? 2 : 1)
const keyOf = (code: string, level: string) => code + '\u0000' + level

export function reviewGroups(r: ReviewResult | null | undefined): ReviewGroup[] {
  if (!r) return []
  const findings = r.findings || []
  const groups: ReviewGroup[] = []
  const idx = new Map<string, number>()
  const ensure = (f: { code: string, name: string, level: ReviewLevel, message: string, messageRef?: RuleRef }) => {
    const k = keyOf(f.code, f.level)
    let i = idx.get(k)
    if (i === undefined) {
      i = groups.length
      idx.set(k, i)
      groups.push({ code: f.code, name: f.name, level: f.level, count: 0, message: f.message, messageRef: f.messageRef, examples: [], more: 0 })
    }
    return groups[i]
  }
  if (r.rules) {
    for (const h of r.rules) ensure(h).count = h.count
    for (const f of findings) {
      const g = ensure(f)
      if (g.examples.length < EXAMPLES_PER_RULE) g.examples.push(f)
    }
  } else {
    for (const f of findings) {
      const g = ensure(f)
      g.count++
      if (g.examples.length < EXAMPLES_PER_RULE) g.examples.push(f)
    }
  }
  for (const g of groups) {
    if (g.count < g.examples.length) g.count = g.examples.length
    g.more = g.count - g.examples.length
  }
  return groups.map((g, i) => ({ g, i }))
    .sort((a, b) => rank(a.g.level) - rank(b.g.level) || a.i - b.i)
    .map((x) => x.g)
}
