// SQL*Plus 的 SQL 缓冲区与行编辑器:LIST / APPEND / CHANGE / DEL / INPUT(sqlplus-compat 工单 11)。
//
// 缓冲区里是上一条下发的 SQL / PL/SQL(展开替换变量之前的原文),/ 与 RUN 重跑它。行编辑器改的就是它:
// 改完敲 / 跑的是改过的那条。和 SQL*Plus 一样有"当前行":LIST 之后是列出的最后一行,APPEND /
// CHANGE 作用在当前行上,INPUT 加在当前行之后。

export interface SqlBuffer {
  lines: string[]
  cur: number // 1 起;缓冲区空时 0
}

export const emptyBuffer = (): SqlBuffer => ({ lines: [], cur: 0 })

export function setBuffer(text: string): SqlBuffer {
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  return { lines, cur: lines.length }
}

export const bufferText = (b: SqlBuffer) => b.lines.join('\n')

// SQL*Plus 的报错原文(SP2-xxxx)。终端照打 —— 熟悉 SQL*Plus 的人认得这些编号。
export const ERR_EMPTY = 'SP2-0223: No lines in SQL buffer.'
export const ERR_RANGE = 'SP2-0226: Invalid line number'
export const ERR_NOT_FOUND = 'SP2-0023: String not found.'

/** 行号范围:''(全部)、n、m n、*(当前行)、LAST、m *、* n、m LAST。返回 [起, 止](1 起,含两端)或报错。 */
export function parseRange(b: SqlBuffer, arg: string, allWhenEmpty = true): [number, number] | string {
  if (!b.lines.length) return ERR_EMPTY
  const toks = arg.trim().split(/\s+/).filter(Boolean)
  const one = (t: string): number | null => {
    const u = t.toUpperCase()
    if (u === '*') return b.cur
    if (u === 'L' || u === 'LAST') return b.lines.length
    return /^\d+$/.test(t) ? Number(t) : null
  }
  let from: number | null
  let to: number | null
  if (!toks.length) {
    if (!allWhenEmpty) return [b.cur, b.cur]
    from = 1; to = b.lines.length
  } else if (toks.length === 1) {
    from = to = one(toks[0])
  } else {
    from = one(toks[0]); to = one(toks[1])
  }
  if (from === null || to === null || from < 1 || to > b.lines.length || from > to) return ERR_RANGE
  return [from, to]
}

/** LIST:列出,当前行标 *(SQL*Plus 的写法:"  1* select …")。列出的最后一行成为当前行。 */
export function listBuffer(b: SqlBuffer, arg: string): { lines: string[]; buf: SqlBuffer } | string {
  const r = parseRange(b, arg)
  if (typeof r === 'string') return r
  const cur = r[1]
  const lines = []
  for (let n = r[0]; n <= r[1]; n++) lines.push(`${String(n).padStart(4)}${n === cur ? '*' : ' '} ${b.lines[n - 1]}`)
  return { lines, buf: { ...b, cur } }
}

/** APPEND 文字:接在当前行末尾。SQL*Plus 只吃一个空格分隔,要以空格开头的写两个空格。 */
export function appendBuffer(b: SqlBuffer, text: string): SqlBuffer | string {
  if (!b.lines.length) return ERR_EMPTY
  const lines = [...b.lines]
  lines[b.cur - 1] += text
  return { lines, cur: b.cur }
}

/**
 * CHANGE /旧/新/:当前行里第一处"旧"换成"新"。分隔符是第一个字符(/ 之外的标点也行);
 * 没写"新"就是删掉"旧"。找不到报 SP2-0023。
 */
export function changeBuffer(b: SqlBuffer, arg: string): SqlBuffer | string {
  if (!b.lines.length) return ERR_EMPTY
  const s = arg.replace(/^\s+/, '')
  if (!s) return 'SP2-0024: Nothing to change.'
  const sep = s[0]
  const parts = s.slice(1).split(sep)
  const oldText = parts[0] ?? ''
  const newText = parts[1] ?? ''
  if (!oldText) return 'SP2-0024: Nothing to change.'
  const line = b.lines[b.cur - 1]
  const at = line.toLowerCase().indexOf(oldText.toLowerCase())
  if (at < 0) return ERR_NOT_FOUND
  const lines = [...b.lines]
  lines[b.cur - 1] = line.slice(0, at) + newText + line.slice(at + oldText.length)
  return { lines, cur: b.cur }
}

/** DEL [n | m n | * | LAST]:删掉;不带参数删当前行。删完当前行是被删范围的下一行(没有就上一行)。 */
export function delBuffer(b: SqlBuffer, arg: string): SqlBuffer | string {
  const r = parseRange(b, arg, false)
  if (typeof r === 'string') return r
  const lines = [...b.lines.slice(0, r[0] - 1), ...b.lines.slice(r[1])]
  const cur = lines.length ? Math.min(r[0], lines.length) : 0
  return { lines, cur }
}

/** INPUT 文字:加在当前行之后,成为新的当前行。 */
export function inputBuffer(b: SqlBuffer, text: string): SqlBuffer {
  const lines = [...b.lines.slice(0, b.cur), text, ...b.lines.slice(b.cur)]
  return { lines, cur: b.cur + 1 }
}

/** SAVE 的文件内容:缓冲区 + 单独一行 /(SQL*Plus 就是这样存的,GET / @ 回来能直接跑)。 */
export const saveText = (b: SqlBuffer) => bufferText(b) + '\n/\n'

/** GET 读回来的文件:去掉结尾单独一行的 /(SAVE 时加的)。 */
export function getText(text: string): string {
  return text.replace(/\r\n?/g, '\n').replace(/\n\s*\/\s*\n*$/, '').replace(/\n+$/, '')
}

/** SAVE / GET 的文件名:没写扩展名补 .sql(SQL*Plus 的默认后缀)。 */
export const withSqlExt = (name: string) => (/\.[^/\\.]+$/.test(name) ? name : name + '.sql')
