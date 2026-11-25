// SQL*Plus 的显示设置:COLUMN 与 SET LINESIZE / PAGESIZE / HEADING / FEEDBACK / NUMFORMAT / COLSEP / NULL / LONG …
// (sqlplus-compat 工单 10,见 .scratch/sqlplus-compat)。
//
// 不逐字符还原 SQL*Plus(用户的决定):结果照旧是终端的表格 / 网格面板,只把这些设置**兑现**在上面 ——
// 列标题换成 HEADING、数字按 FORMAT 掩码排、NULL 显示成设定的字、NOPRINT 的列不显示、超过 LONG 的
// 截断。设了 COLSEP 或关掉标题(HEADING OFF / PAGESIZE 0)时,换成 SQL*Plus 那种不带边框的纯文本
// (一般是为了把结果复制 / SPOOL 出去当文本用)。
//
// 都是**这个终端标签**的设置,与 SQL*Plus 一个会话一份相同。

import { dispWidth, isWideChar } from './textWidth'
import { sanitizeCell } from './sqlResult'

export interface ColumnFmt {
  format?: string // A20 / 999,999.99 / …
  heading?: string
  nullText?: string
  noprint?: boolean
}

export interface DisplayState {
  columns: Map<string, ColumnFmt> // 列名大写
  linesize: number | null // null = 跟着终端宽度
  pagesize: number
  heading: boolean
  feedback: number | null // null = 网关默认(每次都打行数);0 = OFF;n = 至少 n 行才打
  numformat: string
  numwidth: number
  colsep: string | null // null = 用表格;设了就换成纯文本
  nullText: string
  long: number | null
  wrap: boolean
}

export function newDisplayState(): DisplayState {
  return { columns: new Map(), linesize: null, pagesize: 14, heading: true, feedback: null, numformat: '', numwidth: 10, colsep: null, nullText: '', long: null, wrap: true }
}

// ---------------------------------------------------------------- COLUMN

export type ColumnCmd =
  | { kind: 'list' }
  | { kind: 'show'; col: string }
  | { kind: 'set'; col: string; fmt: ColumnFmt; clear: boolean }
  | { kind: 'error' }

const unq = (s: string) => {
  const q = s[0]
  if ((q === "'" || q === '"') && s.endsWith(q) && s.length >= 2) return s.slice(1, -1).split(q + q).join(q)
  return s
}

/** COLUMN [列 [FORMAT f] [HEADING t] [NULL t] [CLEAR] [NOPRINT|PRINT] …]。不认的选项(JUSTIFY、WRAPPED、NEW_VALUE …)忽略。 */
export function parseColumnCmd(arg: string): ColumnCmd {
  const toks = arg.trim().match(/'(?:[^']|'')*'|"(?:[^"]|"")*"|\S+/g) || []
  if (!toks.length) return { kind: 'list' }
  const col = unq(toks[0] ?? '').toUpperCase().replace(/^.*\./, '')
  if (toks.length === 1) return { kind: 'show', col }
  const fmt: ColumnFmt = {}
  let clear = false
  for (let i = 1; i < toks.length; i++) {
    const w = toks[i].toUpperCase()
    const next = () => (i + 1 < toks.length ? unq(toks[++i]) : null)
    if (/^FOR(M|MA|MAT)?$/.test(w)) { const v = next(); if (v === null) return { kind: 'error' }; fmt.format = v.toUpperCase() }
    else if (/^HEA(D|DI|DIN|DING)?$/.test(w)) { const v = next(); if (v === null) return { kind: 'error' }; fmt.heading = v.replace(/\|/g, ' ') }
    else if (w === 'NULL') { const v = next(); if (v === null) return { kind: 'error' }; fmt.nullText = v }
    else if (/^CLE(A|AR)?$/.test(w)) clear = true
    else if (/^NOPRI(N|NT)?$/.test(w)) fmt.noprint = true
    else if (/^PRI(N|NT)?$/.test(w)) fmt.noprint = false
    else if (/^(JUS|JUST|JUSTI|JUSTIF|JUSTIFY|NEW_V|NEW_VA|NEW_VAL|NEW_VALU|NEW_VALUE|OLD_V|OLD_VA|OLD_VAL|OLD_VALU|OLD_VALUE|ALI|ALIA|ALIAS|LIKE)$/.test(w)) i++ // 带一个参数的,跳过参数
    // WRAPPED / TRUNCATED / WORD_WRAPPED / ON / OFF / FOLD_* … 不带参数,忽略
  }
  return { kind: 'set', col, fmt, clear }
}

export function applyColumnCmd(st: DisplayState, c: Extract<ColumnCmd, { kind: 'set' }>) {
  if (c.clear) { st.columns.delete(c.col); return }
  st.columns.set(c.col, { ...(st.columns.get(c.col) || {}), ...c.fmt })
}

/** COLUMN 的回显(SQL*Plus 的写法)。 */
export function describeColumn(col: string, f: ColumnFmt): string[] {
  const out = [`COLUMN   ${col} ON`]
  if (f.heading !== undefined) out.push(`HEADING  '${f.heading}'`)
  if (f.format) out.push(`FORMAT   ${f.format}`)
  if (f.nullText !== undefined) out.push(`NULL     '${f.nullText}'`)
  if (f.noprint) out.push('NOPRINT')
  return out
}

// ---------------------------------------------------------------- 数字格式掩码

/**
 * 按 SQL*Plus 的数字格式掩码排一个数:9(数字,前导空)、0(前导零)、, 或 G(千分位)、. 或 D(小数点)、
 * $(美元符)、S 开头(带正负号)、MI 结尾(负号在后)、EEEE(科学计数)。放不下时整个显示成 #,与
 * SQL*Plus 一致 —— 宁可让人看见"放不下",不能悄悄截掉高位。不是数的值原样返回。
 */
export function formatNumber(v: string, mask: string): string {
  const s = v.trim()
  if (!/^[-+]?(\d+\.?\d*|\.\d+)(e[-+]?\d+)?$/i.test(s)) return v
  const num = Number(s)
  let m = mask.toUpperCase()
  const eeee = m.endsWith('EEEE')
  if (eeee) m = m.slice(0, -4)
  const mi = m.endsWith('MI')
  if (mi) m = m.slice(0, -2)
  const sign = m.startsWith('S')
  if (sign) m = m.slice(1)
  const dollar = m.includes('$')
  m = m.replace(/\$/g, '')
  const dot = m.search(/[.D]/)
  const intMask = dot >= 0 ? m.slice(0, dot) : m
  const fracMask = dot >= 0 ? m.slice(dot + 1) : ''
  const decimals = (fracMask.match(/[90]/g) || []).length
  const width = mask.length + (sign || mi ? 0 : 1)
  const neg = num < 0
  let body: string
  if (eeee) {
    body = Math.abs(num).toExponential(decimals).toUpperCase().replace(/E([+-])(\d)$/, 'E$10$2')
  } else {
    const [ip, fp = ''] = Math.abs(num).toFixed(decimals).split('.')
    const slots = (intMask.match(/[90]/g) || []).length
    const digits = ip === '0' && !intMask.includes('0') && decimals > 0 ? '' : ip
    if (digits.length > slots) return '#'.repeat(width)
    let out = ''
    let d = digits.length - 1
    const zeroFrom = intMask.indexOf('0')
    for (let k = intMask.length - 1; k >= 0; k--) {
      const ch = intMask[k]
      if (ch === '9' || ch === '0') {
        if (d >= 0) out = digits[d--] + out
        else if (zeroFrom >= 0 && k >= zeroFrom) out = '0' + out
      } else if (ch === ',' || ch === 'G') {
        if (d >= 0) out = ',' + out
      }
    }
    body = out + (decimals ? '.' + fp : '')
  }
  if (dollar) body = '$' + body
  if (mi) return body + (neg ? '-' : '')
  if (sign) return (neg ? '-' : '+') + body
  return (neg ? '-' : '') + body
}

/** A20:字符列的显示宽度。放不下时截断(WRAP ON 时带 …,表示后面还有)。 */
function formatChar(v: string, n: number, wrap: boolean): string {
  if (dispWidth(v) <= n) return v
  let w = 0
  let out = ''
  const room = wrap ? n - 1 : n
  for (const ch of v) {
    const cw = isWideChar(ch.codePointAt(0) || 0) ? 2 : 1
    if (w + cw > room) break
    out += ch
    w += cw
  }
  return wrap ? out + '…' : out
}

// ---------------------------------------------------------------- 套在结果上

export interface Shaped { columns: string[]; rows: string[][]; numeric: boolean[] }

/** 把显示设置套在一个结果集上。numeric 是哪几列是数(由调用方按数据判断)。 */
export function applyDisplay(columns: string[], rows: string[][], numeric: boolean[], st: DisplayState): Shaped {
  const keep: number[] = []
  const fmts = columns.map((c) => st.columns.get(c.toUpperCase().replace(/^.*\./, '')))
  columns.forEach((_, i) => { if (!fmts[i]?.noprint) keep.push(i) })
  const outCols = keep.map((i) => fmts[i]?.heading ?? columns[i])
  const outRows = rows.map((r) => keep.map((i) => {
    let v = r[i] ?? ''
    const f = fmts[i]
    if (v === '') return f?.nullText ?? st.nullText
    if (st.long !== null && v.length > st.long) v = v.slice(0, st.long)
    const mask = f?.format
    if (mask && /^A\d+$/.test(mask)) return formatChar(v, Number(mask.slice(1)), st.wrap)
    if (mask && /[90]/.test(mask)) return formatNumber(v, mask)
    if (!mask && st.numformat && numeric[i]) return formatNumber(v, st.numformat)
    return v
  }))
  return { columns: outCols, rows: outRows, numeric: keep.map((i) => numeric[i] ?? false) }
}

/** 纯文本排版(设了 COLSEP、或关掉了标题时):表头 + 一行 - 下划线 + 数据,列之间用 COLSEP。 */
export function renderPlain(raw: Shaped, st: DisplayState, termCols: number): string[] {
  // 列名和值都来自数据库,谁能写行谁就控制它们。另外两种排版都过 sanitizeCell,这里也要过:
  // 否则查到 CHR(27)||'[2J' 就会清屏、伪造执行结果(sqlResult.ts 的 sanitizeCell 有详细说明)。
  const t: Shaped = { ...raw, columns: raw.columns.map(sanitizeCell), rows: raw.rows.map((r) => r.map((v) => sanitizeCell(v ?? ''))) }
  const sep = st.colsep ?? ' '
  const heading = st.heading && st.pagesize !== 0
  const widths = t.columns.map((h, i) => {
    let w = heading ? dispWidth(h) : 0
    for (const r of t.rows) w = Math.max(w, dispWidth(r[i] ?? ''))
    return w
  })
  const pad = (s: string, w: number, right: boolean) => {
    const gap = ' '.repeat(Math.max(0, w - dispWidth(s)))
    return right ? gap + s : s + gap
  }
  const max = st.linesize ?? (termCols > 0 ? termCols : 0)
  const clip = (line: string) => (max > 0 && dispWidth(line) > max ? formatChar(line, max, false) : line)
  const out: string[] = []
  if (heading) {
    out.push(clip(t.columns.map((h, i) => pad(h, widths[i], t.numeric[i])).join(sep).trimEnd()))
    out.push(clip(widths.map((w) => '-'.repeat(w)).join(sep)))
  }
  for (const r of t.rows) out.push(clip(r.map((v, i) => pad(v ?? '', widths[i], t.numeric[i])).join(sep).trimEnd()))
  return out
}

/** 要不要换成纯文本排版。 */
export const plainLayout = (st: DisplayState) => st.colsep !== null || !st.heading || st.pagesize === 0

/** 表格最多画多宽:SET LINESIZE 设过就取它与终端宽度的较小者。 */
export const tableWidth = (st: DisplayState, termCols: number) => (st.linesize ? Math.min(st.linesize, termCols) : termCols)

/** 这次的行数要不要打(SET FEEDBACK)。 */
export const showFeedback = (st: DisplayState, rows: number) => st.feedback === null || (st.feedback > 0 && rows >= st.feedback)

// ---------------------------------------------------------------- SET / SHOW

/**
 * SET 的显示选项。是这几个就生效并返回 true;值写错返回 'bad';不是这几个返回 false(交给别处)。
 */
export function applyDisplaySet(option: string, value: string, st: DisplayState): boolean | 'bad' {
  const o = option.toUpperCase()
  const raw = value.trim().replace(/;+$/, '')
  const v = raw.toUpperCase()
  const onoff = v === 'ON' ? true : v === 'OFF' ? false : null
  const int = /^\d+$/.test(v) ? Number(v) : null
  const text = unq(raw)
  switch (true) {
    case /^LIN(E|ES|ESI|ESIZ|ESIZE)?$/.test(o): if (int === null || int < 1) return 'bad'; st.linesize = int; return true
    case /^PAGES(I|IZ|IZE)?$/.test(o) || o === 'PAGES': if (int === null) return 'bad'; st.pagesize = int; return true
    case /^HEA(D|DI|DIN|DING)?$/.test(o): if (onoff === null) return 'bad'; st.heading = onoff; return true
    case /^FEED(B|BA|BAC|BACK)?$/.test(o):
      if (onoff !== null) st.feedback = onoff ? 1 : 0
      else if (int !== null) st.feedback = int
      else return 'bad'
      return true
    case /^NUMF(O|OR|ORM|ORMA|ORMAT)?$/.test(o): st.numformat = text.toUpperCase(); return true
    case /^NUM(W|WI|WID|WIDT|WIDTH)?$/.test(o): if (int === null || int < 1) return 'bad'; st.numwidth = int; return true
    case /^COLSEP$/.test(o): st.colsep = text === ' ' ? null : text; return true
    case o === 'NULL': st.nullText = text; return true
    case o === 'LONG': if (int === null || int < 1) return 'bad'; st.long = int; return true
    case /^WRA(P)?$/.test(o): if (onoff === null) return 'bad'; st.wrap = onoff; return true
    case /^(TRIMO|TRIMOU|TRIMOUT|TRIMS|TRIMSP|TRIMSPO|TRIMSPOO|TRIMSPOOL)$/.test(o): return onoff === null ? 'bad' : true
  }
  return false
}

/** SHOW 某个显示选项(SQL*Plus 的写法);不是显示选项返回 null。 */
export function showDisplaySetting(option: string, st: DisplayState): string | null {
  const o = option.toUpperCase()
  if (/^LIN(E|ES|ESI|ESIZ|ESIZE)?$/.test(o)) return `linesize ${st.linesize ?? '(终端宽度)'}`
  if (/^PAGES(I|IZ|IZE)?$/.test(o) || o === 'PAGES') return `pagesize ${st.pagesize}`
  if (/^HEA(D|DI|DIN|DING)?$/.test(o)) return `heading ${st.heading ? 'ON' : 'OFF'}`
  if (/^FEED(B|BA|BAC|BACK)?$/.test(o)) return st.feedback === null ? 'FEEDBACK ON for 1 or more rows' : st.feedback === 0 ? 'feedback OFF' : `FEEDBACK ON for ${st.feedback} or more rows`
  if (/^NUMF(O|OR|ORM|ORMA|ORMAT)?$/.test(o)) return st.numformat ? `numformat "${st.numformat}"` : 'numformat ""'
  if (/^NUM(W|WI|WID|WIDT|WIDTH)?$/.test(o)) return `numwidth ${st.numwidth}`
  if (o === 'COLSEP') return `colsep "${st.colsep ?? ' '}"`
  if (o === 'NULL') return `null "${st.nullText}"`
  if (o === 'LONG') return `long ${st.long ?? 80}`
  if (/^WRA(P)?$/.test(o)) return `wrap : lines will be ${st.wrap ? 'wrapped' : 'truncated'}`
  return null
}

/** SHOW 能在本地回答的显示选项名(给命令识别用)。 */
export const DISPLAY_SHOW = /^(LIN(E|ES|ESI|ESIZ|ESIZE)?|PAGES(I|IZ|IZE)?|HEA(D|DI|DIN|DING)?|FEED(B|BA|BAC|BACK)?|NUMF(O|OR|ORM|ORMA|ORMAT)?|NUM(W|WI|WID|WIDT|WIDTH)?|COLSEP|NULL|LONG|WRA(P)?)$/i

/** SHOW ALL 里显示选项那几行。 */
export function showAllDisplay(st: DisplayState): string[] {
  return ['COLSEP', 'FEEDBACK', 'HEADING', 'LINESIZE', 'LONG', 'NULL', 'NUMFORMAT', 'NUMWIDTH', 'PAGESIZE', 'WRAP'].map((o) => showDisplaySetting(o, st) as string)
}
