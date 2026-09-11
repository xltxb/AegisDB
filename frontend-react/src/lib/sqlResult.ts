// sqlResult — turn a (simulated) SELECT into a psql-style aligned table for the
// xterm terminal. The backend executor is a simulation (see backend
// gateway/executor.go): it returns a row COUNT and latency but no result set,
// so we synthesise plausible demo rows here purely for presentation. Columns are
// parsed from the SQL when explicit; values are heuristic by column name.

import { dispWidth, isWideChar } from './textWidth'

// ---- ANSI helpers -------------------------------------------------------
export const ANSI = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  gray: '\x1b[90m',
}
export const c = (color: string, s: string) => color + s + ANSI.reset

// ---- SQL parsing --------------------------------------------------------

export function isSelect(sql: string): boolean {
  return /^\s*(select|show|desc|describe|explain)\b/i.test(sql)
}

// parseColumns extracts the select-list column labels. `SELECT *` (or anything
// with a bare *) yields [] → caller uses generic columns. Aggregates and aliases
// are kept as their display label.
export function parseColumns(sql: string): string[] {
  const m = /^\s*select\s+([\s\S]+?)\s+from\b/i.exec(sql)
  if (!m) return []
  const list = m[1].trim()
  if (list === '*' || /(^|\W)\*(\W|$)/.test(list)) return []
  const cols = splitTopLevel(list).map((raw) => {
    let col = raw.trim()
    // alias:  expr AS name  |  expr name
    const asMatch = /\s+as\s+([`"\w]+)\s*$/i.exec(col)
    if (asMatch) return clean(asMatch[1])
    // table.column → column
    const dot = /^([`"\w]+)\.([`"\w*]+)$/.exec(col)
    if (dot) return clean(dot[2])
    return clean(col)
  })
  return cols.filter(Boolean)
}

function clean(s: string): string {
  return s.replace(/[`"]/g, '').trim()
}

// splitTopLevel splits on commas that are not inside parentheses (so count(a,b)
// stays one column).
function splitTopLevel(s: string): string[] {
  const out: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of s) {
    if (ch === '(') depth++
    else if (ch === ')') depth = Math.max(0, depth - 1)
    if (ch === ',' && depth === 0) {
      out.push(cur)
      cur = ''
    } else cur += ch
  }
  if (cur.trim()) out.push(cur)
  return out
}

function isAggregate(col: string): boolean {
  return /^\s*(count|sum|avg|min|max)\s*\(/i.test(col)
}

// ---- value synthesis ----------------------------------------------------

const NAMES = ['Alice Chen', 'Bob Li', 'Carol Wu', 'David Zhao', 'Eve Sun', 'Frank Ma', 'Grace Xu', 'Henry Guo']
const STATUSES = ['active', 'paused', 'pending', 'closed']
const DATES = ['2026-06-28', '2026-06-29', '2026-06-30', '2026-07-01']

function synthCell(col: string, i: number): string {
  const k = col.toLowerCase()
  if (isAggregate(col)) return String((i + 1) * 137 + 42)
  if (k === 'id' || k.endsWith('_id') || k.endsWith('id')) return String(1001 + i)
  if (k.includes('email')) {
    const n = NAMES[i % NAMES.length].split(' ')[0].toLowerCase()
    return `${n}@vela.io`
  }
  if (k.includes('name') || k === 'user' || k === 'actor') return NAMES[i % NAMES.length]
  if (k.includes('status') || k.includes('state')) return STATUSES[i % STATUSES.length]
  if (k.endsWith('_at') || k.includes('date') || k.includes('time') || k.includes('created') || k.includes('updated'))
    return DATES[i % DATES.length]
  if (/(amount|price|total|balance|qty|quantity|count|num|score)/.test(k)) return String((i + 1) * 128 + 7)
  if (/^(is_|has_)/.test(k) || k.includes('enabled') || k.includes('active') || k === 'flag')
    return i % 2 === 0 ? 'true' : 'false'
  return `${col}_${i + 1}`
}

function isNumericCol(col: string): boolean {
  const k = col.toLowerCase()
  if (isAggregate(col)) return true
  if (k === 'id' || k.endsWith('_id') || k.endsWith('id')) return true
  return /(amount|price|total|balance|qty|quantity|count|num|score)/.test(k)
}

export interface SynthTable {
  columns: string[]
  rows: string[][]
  numeric: boolean[]
}

// SIMULATED-PATH: exec-result-grid —— 见 docs/simulated-paths.md。
//
// 这张表格是**合成的**:没有凭据的实例不会真的去查,行数由服务端随机给出,内容由这里
// 编。它与真实结果在屏幕上长得一模一样,用户分辨不出 —— 那是明知的欠账,选项写在那份
// 清单的"待补充"里。
//
// synthTable builds display columns + up to `limit` demo rows for a SELECT.
export function synthTable(sql: string, rowCount: number, limit = 12): SynthTable {
  let columns = parseColumns(sql)
  if (columns.length === 0) columns = ['id', 'name', 'status', 'created_at']
  const shown = Math.max(0, Math.min(rowCount, limit))
  const rows: string[][] = []
  for (let i = 0; i < shown; i++) rows.push(columns.map((col) => synthCell(col, i)))
  return { columns, rows, numeric: columns.map(isNumericCol) }
}

// numericByData reports, per column, whether every non-empty cell parses as a
// number — so a real result set is right-aligned by actual content, not by a
// heuristic on the column name.
function numericByData(columns: string[], rows: string[][]): boolean[] {
  return columns.map((_, i) => {
    let sawValue = false
    for (const r of rows) {
      const v = r[i]
      if (v == null || v === '') continue
      sawValue = true
      if (!/^-?\d+(\.\d+)?$/.test(v.trim())) return false
    }
    return sawValue
  })
}

// buildTable makes a renderable table from a REAL result set (columns + rows
// returned by the target DB) — the non-synthetic path.
export function buildTable(columns: string[], rows: string[][]): SynthTable {
  return { columns, rows, numeric: numericByData(columns, rows) }
}

// ---- rendering ----------------------------------------------------------

// truncateDisp cuts a string to a max display width, appending … when clipped.
function truncateDisp(s: string, max: number): string {
  if (dispWidth(s) <= max) return s
  let w = 0
  let out = ''
  for (const ch of s) {
    const cw = isWideChar(ch.codePointAt(0) || 0) ? 2 : 1
    if (w + cw > max - 1) break // leave a cell for …
    out += ch
    w += cw
  }
  return out + '…'
}
// padDisp pads to a display width (CJK-aware), left or right aligned.
function padDisp(s: string, w: number, right: boolean): string {
  const gap = ' '.repeat(Math.max(0, w - dispWidth(s)))
  return right ? gap + s : s + gap
}

// sanitizeCell strips terminal control characters from DATA before it is written
// to xterm.
//
// Cell values and column names come from the target database, so anyone able to
// write a row controls them. term.write() interprets ANSI/OSC escapes, so an
// escape stored in a row could repaint the scrollback the operator is reading —
// erasing the PROD warning banner, forging a success line, faking a prompt — and
// a device-status query (ESC[6n) makes xterm reply on the INPUT channel, so the
// answer lands in the SQL the user is typing. Only the colouring this module adds
// itself may reach the terminal (EF3).
//
// Newlines and tabs become spaces first so single-line layouts stay intact; what
// remains of the C0/C1 ranges is dropped outright.
export const sanitizeCell = (s: string) =>
  (s ?? '').replace(/[\r\n\t]+/g, ' ').replace(/[\x00-\x1f\x7f-\x9f]/g, '')

// sanitizeMultiline is sanitizeCell for vertical (\G) display, which exists
// precisely to show values a horizontal row cannot — a SHOW CREATE TABLE body, a
// JSON blob — so their real newlines carry meaning and must survive. Every other
// control character still goes, and the surviving newlines become CRLF because
// xterm needs the carriage return to start the next line at column 0.
export const sanitizeMultiline = (s: string) =>
  (s ?? '')
    .replace(/\r\n?/g, '\n')
    .replace(/\t/g, ' ')
    .replace(/[\x00-\x09\x0b-\x1f\x7f-\x9f]/g, '')
    .replace(/\n/g, '\r\n')

// renderVertical produces MySQL `\G`-style vertical output: one "column: value"
// pair per line, grouped per row. Ideal for wide/long values (e.g. SHOW CREATE
// TABLE) whose multi-line content would break a horizontal table.
export function renderVertical(columns: string[], rows: string[][]): string[] {
  const w = columns.reduce((m, col) => Math.max(m, col.length), 0)
  const out: string[] = []
  rows.forEach((row, i) => {
    out.push(c(ANSI.gray, `*************************** ${i + 1}. row ***************************`))
    columns.forEach((col, j) => {
      const label = ' '.repeat(Math.max(0, w - col.length)) + col
      const val = sanitizeMultiline(row[j] ?? '')
      out.push(c(ANSI.bold, label) + c(ANSI.gray, ': ') + val)
    })
  })
  return out
}

// fallbackColWidth caps a single column when the caller does not know how wide
// the terminal is — one huge value (a long text/JSON cell) must not blow up the
// layout. Use \G / \x to see wide values in full.
const fallbackColWidth = 60

// minColWidth is the floor a column keeps even when the table cannot fit: below
// this a cell is all ellipsis and carries no information. A table with enough
// columns to hit the floor overflows the terminal and wraps — unavoidable, and
// preferable to rendering columns that say nothing.
const minColWidth = 8

// allocWidths distributes `available` display columns across cells whose natural
// (untruncated) widths are given.
//
// Truncating every column at one fixed number wastes the terminal: `SHOW GRANTS`
// returns a single column of near-identical strings that differ only in their
// tail, so a 60-column cap on a 200-column terminal renders seventeen rows that
// all read `…t_dws_bp_order_user_kind_…` — the data is on screen and still
// unreadable.
//
// So this is a water-fill instead: narrow columns are settled at their natural
// width first, and the space they did not need is redistributed among the
// columns that are still too wide. Each pass recomputes the equal share over the
// unsettled columns, so a table of one wide column plus several narrow ones
// spends nearly the whole terminal on the column that actually varies.
function allocWidths(natural: number[], available: number): number[] {
  const n = natural.length
  if (n === 0) return []
  if (natural.reduce((a, b) => a + b, 0) <= available) return natural.slice()

  const out = new Array<number>(n).fill(0)
  let budget = available
  let unsettled = natural.map((_, i) => i)
  while (unsettled.length > 0) {
    const share = Math.floor(budget / unsettled.length)
    const fits = unsettled.filter((i) => natural[i] <= share)
    if (fits.length === 0) {
      // Every remaining column wants more than its share: split what is left
      // evenly and hand the indivisible remainder to the leftmost columns.
      let extra = budget - share * unsettled.length
      for (const i of unsettled) out[i] = share + (extra-- > 0 ? 1 : 0)
      break
    }
    for (const i of fits) {
      out[i] = natural[i]
      budget -= natural[i]
    }
    unsettled = unsettled.filter((i) => natural[i] > share)
  }
  // Lift columns squeezed under the floor — but never past what they actually
  // need, or a 2-wide `id` would be padded to 8 and push the frame off screen.
  return out.map((w, i) => Math.max(w, Math.min(natural[i], minColWidth)))
}

// renderTable produces a fully-boxed, aligned, ANSI-coloured grid (┌┬┐ / ├┼┤ /
// └┴┘). Widths are measured in display columns (CJK-aware) so mixed ASCII/CJK
// rows stay aligned; cells are single-lined.
//
// termCols is the terminal's current width. Given it, the table may spend the
// whole terminal and truncates only what genuinely does not fit (a table that
// already fits stays at its natural width); omit it — tests, or any caller with
// no terminal in hand — and every column falls back to a fixed cap. Callers pass
// the live value per render, so a resized window is picked up by the next result
// without any resize plumbing here.
/**
 * Render the result as a bordered table.
 *
 * `hint` is called when values had to be cut to fit, with how many. A truncated
 * cell ends in `…`, which says a value continues but not that the full one is
 * still reachable — the data is all here, and `\G` prints it whole. Without the
 * line, the ellipsis reads as "this is all you get" and the way out stays
 * undiscovered. The caller supplies the text so this module stays free of i18n.
 */
export function renderTable(t: SynthTable, termCols = 0, hint?: (n: number) => string): string[] {
  const rawHeads = t.columns.map((h) => sanitizeCell(h))
  const rawCells = t.rows.map((r) => t.columns.map((_, i) => sanitizeCell(r[i] ?? '')))

  // Frame overhead: one '│' per column plus a trailing one, and a space of
  // padding on each side of every cell — 3 cells per column, plus 1.
  const overhead = t.columns.length * 3 + 1
  const natural = rawHeads.map((h, i) => {
    let w = dispWidth(h)
    for (const r of rawCells) w = Math.max(w, dispWidth(r[i]))
    return w
  })
  const widths = termCols > 0
    ? allocWidths(natural, termCols - overhead)
    : natural.map((w) => Math.min(w, fallbackColWidth))

  const heads = rawHeads.map((h, i) => truncateDisp(h, widths[i]))
  // Count the VALUES that lost content. Headers are excluded: a clipped column
  // name is a nuisance, not hidden data, and counting it would overstate what
  // `\G` recovers.
  let cut = 0
  const cells = rawCells.map((r) => r.map((v, i) => {
    const s = truncateDisp(v, widths[i])
    if (s !== v) cut++
    return s
  }))
  const g = (s: string) => c(ANSI.gray, s)
  const vert = g('│')
  const bar = (l: string, m: string, r: string) => g(l + widths.map((w) => '─'.repeat(w + 2)).join(m) + r)
  const rowLine = (vals: string[], head: boolean) =>
    vert + vals.map((v, i) => {
      const cell = ' ' + padDisp(v, widths[i], !head && t.numeric[i]) + ' '
      return (head ? c(ANSI.bold, cell) : t.numeric[i] ? c(ANSI.cyan, cell) : cell) + vert
    }).join('')

  const out = [bar('┌', '┬', '┐'), rowLine(heads, true), bar('├', '┼', '┤')]
  for (const r of cells) out.push(rowLine(r, false))
  out.push(bar('└', '┴', '┘'))
  if (cut > 0 && hint) out.push(hint(cut))
  return out
}
