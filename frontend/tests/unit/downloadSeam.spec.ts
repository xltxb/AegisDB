import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

// 「把一个 Blob 交给浏览器下载」只有一份实现。
//
// 这条规则不是为了整洁,是因为这段十行的代码里藏着两个只在**某些浏览器**上才露面的
// 坑,而它们已经被踩过、修过、写进注释了:
//
//   · `document.body.appendChild(a)` —— 不在文档里的 <a>,click() 在 Firefox 上不
//     触发下载。
//   · `revokeObjectURL` 要延后一拍 —— 同步撤销时 Safari 偶尔在下载真正开始前就丢掉
//     那个 URL,表现为「点了没反应」。
//
// 复制一份就等于把这两条教训留在原地:修好的那份记得,新抄的那份不记得。而它不会
// 报错,只是在某个人的某个浏览器上"点了没反应" —— 最难被报上来的那种故障。
const ALLOWED = ['src/lib/download.ts']

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sourceFiles(p)
    return /\.(ts|tsx)$/.test(e.name) ? [p] : []
  })
}

test('下载一律走 lib/download,不各自拼 <a download>', () => {
  const offenders: string[] = []
  for (const file of sourceFiles('src')) {
    const rel = file.replace(/\\/g, '/')
    if (ALLOWED.includes(rel)) continue
    const lines = fs.readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, i) => {
      const t = line.trim()
      if (t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')) return
      if (/URL\.createObjectURL/.test(line)) offenders.push(`${rel}:${i + 1}  ${t}`)
    })
  }
  expect(offenders, `这些地方自己拼了一份下载:\n${offenders.join('\n')}`).toEqual([])
})
