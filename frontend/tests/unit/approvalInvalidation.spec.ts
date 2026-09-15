import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

import { APPROVAL_QUERY_KEYS } from '../../src/lib/approvalKeys'

// 批完最后一张待办,侧栏那颗红点还挂着 —— 最长再挂 60 秒,直到它自己的
// refetchInterval 到点。
//
// 成因不是忘了刷新,是**失效清单漏了一个键**。审批相关的查询现在有四个根键:
//
//   ['approvals', …]              列表(带 scope / 页码 / 状态)、收件箱的「全部」计数
//   ['approvals-pending']         顶栏那颗(scope=all:我发起的 + 我要签的)
//   ['approvals-inbox-pending']   侧栏那颗(只数轮到我签字的)
//
// 而 useDecideApproval 原来只失效前两个。TanStack 的前缀匹配是**逐个比较数组
// 元素**的:`['approvals']` 只匹配第一个元素恰好等于 `'approvals'` 的查询,
// `'approvals-inbox-pending'` 是另一个字符串,不在其中。两个键长得像,匹配规则
// 却一点也不像 —— 这正是它能被漏掉两次的原因。
//
// 所以这条规则钉的不是"当前少了哪一个",而是**下一个**:任何人再加一个
// `queryKey: ['approvals-…']`,只要没把它写进 APPROVAL_QUERY_KEYS,这里当场变红。
// 逐个查询去补的写法修不了这一类,因为它要求每个新加查询的人都记得去改另一个文件。

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sourceFiles(p)
    return /\.tsx?$/.test(e.name) ? [p] : []
  })
}

/** 注释里也会出现 `queryKey: ['approvals-…']` 这样的举例,先去掉再扫。 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

/**
 * 找出源码里所有**工单**查询的根键。
 *
 * 认的是 `approvals` 与 `approvals-*` —— 复数,指的是审批工单本身。
 *
 * 刻意**不**认 `approval-chain`(单数):那是"审批链上配了哪些人",一条设置,
 * 由设置页改。批准一张工单不会动它,把它拖进失效清单只会让每次签字都白白重取
 * 一遍人员配置。它不在这里是想清楚的结果,不是漏的。
 *
 * 只认字面量 —— 拼出来的那几段(`['approvals', scope, page]` 里的 scope、page)
 * 本来就落在 `'approvals'` 这个根下面,前缀失效带得走。
 */
function approvalRootKeys(): Map<string, string[]> {
  const found = new Map<string, string[]>()
  for (const f of sourceFiles(path.join(process.cwd(), 'src'))) {
    const src = stripComments(fs.readFileSync(f, 'utf8'))
    for (const m of src.matchAll(/queryKey:\s*\[\s*'([^']+)'/g)) {
      const root = m[1] as string
      if (!/^approvals(-|$)/.test(root)) continue
      found.set(root, [...(found.get(root) ?? []), path.relative(process.cwd(), f)])
    }
  }
  return found
}

test('每一个审批查询的根键都在失效清单里', () => {
  const declared = new Set<string>(APPROVAL_QUERY_KEYS.map((k) => k[0]))
  const missing = [...approvalRootKeys()].filter(([root]) => !declared.has(root))

  expect(
    missing.map(([root, files]) => `${root} (声明于 ${files.join(', ')})`),
    '这些查询键没有进 APPROVAL_QUERY_KEYS:批准/驳回之后它们不会刷新',
  ).toEqual([])
})

test('失效清单里没有已经不存在的键', () => {
  // 反方向也要守:留着一个没人注册的键不会报错,只会让这份清单慢慢变成
  // 一串没人敢删的字符串,下一个人就读不出它到底覆盖了什么。
  const live = approvalRootKeys()
  const stale = APPROVAL_QUERY_KEYS.map((k) => k[0]).filter((root) => !live.has(root))

  expect(stale, '清单里的键在 src 下已经没有对应的查询了').toEqual([])
})

test('清单覆盖了三颗计数各自的键 —— 它们是三个不同的口径', () => {
  // 顶栏与侧栏的数字口径不同(见 api/modules/approvals.ts 的注释),所以是两个
  // 独立的查询,不能合并。既然不能合并,失效时就必须两个都点到名。
  const roots = APPROVAL_QUERY_KEYS.map((k) => k[0])
  expect(roots).toContain('approvals')
  expect(roots).toContain('approvals-pending')
  expect(roots).toContain('approvals-inbox-pending')
})
