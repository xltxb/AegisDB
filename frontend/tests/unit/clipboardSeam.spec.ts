import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

// 复制走 lib/clipboard,不直接碰 navigator.clipboard。
//
// navigator.clipboard 只在**安全上下文**(HTTPS 或 localhost)里存在,而这个网关经常
// 被人用局域网 IP 直接打开(http://10.28.2.125:5173)—— 在那种地址下它是 undefined,
// 直接调用的复制按钮什么都不做,而点的人看不出为什么:剪贴板里还是上一次的内容。
//
// `lib/clipboard.copyText` 正是为此存在的(execCommand 兜底,并如实返回成没成)。绕过
// 它的每一处都是一个在局域网下静默失效的复制按钮 —— 其中最要命的是**只回显一次**的
// 那些:导出包口令、开放接口密钥。那一次没复制上,东西就真的丢了。
//
// 这条规则是结构性的,和 modalRoots 一样:它守的是"新写的复制按钮会不会又绕过去",
// 而那种事没有任何运行时信号能提前告诉你。
const ALLOWED = ['src/lib/clipboard.ts']

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sourceFiles(p)
    return /\.(ts|tsx)$/.test(e.name) ? [p] : []
  })
}

/** 注释里提到这个 API 是可以的 —— 要找的是**调用**。 */
function isComment(line: string): boolean {
  const t = line.trim()
  return t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')
}

test('复制一律走 lib/clipboard,不直接调 navigator.clipboard', () => {
  const offenders: string[] = []

  // 相对仓库根跑 —— 与 modalRoots / autofocus 同一个约定(Playwright 从 frontend/ 启动)。
  for (const file of sourceFiles('src')) {
    const rel = file.replace(/\\/g, '/')
    if (ALLOWED.includes(rel)) continue
    const lines = fs.readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, i) => {
      if (isComment(line)) return
      if (/navigator\.clipboard/.test(line)) offenders.push(`${rel}:${i + 1}  ${line.trim()}`)
    })
  }

  expect(offenders, `这些地方绕过了 lib/clipboard:\n${offenders.join('\n')}`).toEqual([])
})
