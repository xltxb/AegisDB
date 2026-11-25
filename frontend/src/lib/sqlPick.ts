/**
 * 数据查询页按「执行」时到底执行哪一段(2026-11-03 生产报障)。
 *
 *   有选区     → 只执行选中的文本。
 *   没有选区   → 执行光标所在的那条语句:分号或空行(只含空白的一行)为界。
 *
 * 之前发的是整个编辑器的内容。数据查询只收单条语句:各条之间写了分号,被后端以"多条语句"
 * 拒绝;没写分号、只用空行隔开,几条 SELECT 就拼成一句发给数据库,报 syntax error。
 *
 * 和 lib/sqlCount 一样,这只决定"把哪段文本交出去",不是判定的一环 —— 交出去的文本照样
 * 过后端的只读判定(那边按 sqlutil.SplitStatements 拆,多于一条就拒)。这里切错了最多是
 * 发错一段、被拒或报语法错,不会让任何东西绕过网关。
 *
 * 引号、注释、PostgreSQL 美元引用里的分号和空行不算边界;引号只认成对转义,不认反斜杠
 * (与 sqlCount、后端同一个理由)。
 */

interface Seg { start: number; end: number }

/** 把文本按分号 / 空行切成若干段([start, end),不含分号)。 */
function segments(src: string): Seg[] {
  const out: Seg[] = []
  let start = 0
  const cut = (end: number, next: number) => { out.push({ start, end }); start = next }
  let i = 0
  while (i < src.length) {
    const ch = src[i]
    if (ch === '-' && src[i + 1] === '-') {
      while (i < src.length && src[i] !== '\n') i++
      continue // 停在换行上,下面照常判断空行
    }
    if (ch === '/' && src[i + 1] === '*') {
      const close = src.indexOf('*/', i + 2)
      i = close < 0 ? src.length : close + 2
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      i++
      while (i < src.length) {
        if (src[i] === ch) {
          if (src[i + 1] === ch) { i += 2; continue }
          break
        }
        i++
      }
      i++
      continue
    }
    if (ch === '$') {
      const m = /^\$[A-Za-z_\u0080-￿][A-Za-z0-9_\u0080-￿]*\$|^\$\$/.exec(src.slice(i, i + 64))
      if (m) {
        const end = src.indexOf(m[0], i + m[0].length)
        i = end < 0 ? src.length : end + m[0].length
        continue
      }
    }
    if (ch === ';') {
      cut(i, i + 1)
      i++
      continue
    }
    if (ch === '\n') {
      // 下一行只有空白:这里就是边界
      let j = i + 1
      while (j < src.length && (src[j] === ' ' || src[j] === '\t' || src[j] === '\r')) j++
      if (j < src.length && src[j] === '\n') {
        cut(i, j)
        i = j
        continue
      }
    }
    i++
  }
  out.push({ start, end: src.length })
  return out
}

/** 去掉注释后还有没有内容(纯注释、纯空白的段不算一条语句)。 */
function hasSql(s: string): boolean {
  return s.replace(/--[^\n]*/g, '').replace(/\/\*[\s\S]*?(\*\/|$)/g, '').trim() !== ''
}

/** 光标所在的那条语句。光标落在两条之间的空白里,取前一条;前面没有就取后一条。 */
export function statementAt(text: string, pos: number): string {
  const segs = segments(text ?? '').filter((s) => hasSql(text.slice(s.start, s.end)))
  if (!segs.length) return ''
  const p = Math.max(0, Math.min(pos, text.length))
  const hit = segs.find((s) => s.start <= p && p <= s.end)
    ?? [...segs].reverse().find((s) => s.end <= p)
    ?? segs[0]
  return text.slice(hit.start, hit.end).trim()
}

/** 按「执行」/「导出」时交出去的 SQL:有选区用选区,否则光标所在语句。 */
export function pickRunSql(text: string, selStart: number, selEnd: number): string {
  const src = text ?? ''
  if (selEnd !== selStart) {
    const picked = src.slice(Math.min(selStart, selEnd), Math.max(selStart, selEnd)).trim()
    if (picked) return picked
  }
  return statementAt(src, selEnd)
}
