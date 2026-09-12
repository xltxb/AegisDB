import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

// 二次确认一律走 lib/confirm。
//
// `confirmAction` 是一层很薄的包装,薄到看起来可以绕开 —— 但它存在的理由正是"以后
// 要把原生对话框换成产品自己的弹窗,而不必去翻每一个调用点"(M15)。绕过去的每一处,
// 到换的那天都会是一个漏网的原生弹窗:样式对不上是小事,它还会**阻塞整个页面**,
// 而批量审批这类地方恰恰最不能卡住。
//
// 它顺带还兜了一件事:`typeof window === 'undefined'` 时不调用 —— 直接写
// window.confirm 的地方在没有 window 的环境里会直接抛。
const ALLOWED = ['src/lib/confirm.ts']

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sourceFiles(p)
    return /\.(ts|tsx)$/.test(e.name) ? [p] : []
  })
}

test('二次确认一律走 lib/confirm,不直接调 window.confirm', () => {
  const offenders: string[] = []
  for (const file of sourceFiles('src')) {
    const rel = file.replace(/\\/g, '/')
    if (ALLOWED.includes(rel)) continue
    const lines = fs.readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, i) => {
      const t = line.trim()
      if (t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')) return
      if (/window\.confirm/.test(line)) offenders.push(`${rel}:${i + 1}  ${t}`)
    })
  }
  expect(offenders, `这些地方绕过了 lib/confirm:\n${offenders.join('\n')}`).toEqual([])
})
