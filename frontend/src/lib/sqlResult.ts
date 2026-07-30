// sqlResult — turn a (simulated) SELECT into a psql-style aligned table for the
// xterm terminal. The backend executor is a simulation (see backend
// gateway/executor.go): it returns a row COUNT and latency but no result set,
// so we synthesise plausible demo rows here purely for presentation. Columns are
// parsed from the SQL when explicit; values are heuristic by column name.

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

// isWideChar reports whether a code point occupies two terminal cells (CJK, kana,
// fullwidth forms), so alignment holds for mixed ASCII/CJK data.
function isWideChar(cp: number): boolean {
  return (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe4f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    (cp >= 0x1f300 && cp <= 0x1faff) ||
    (cp >= 0x20000 && cp <= 0x3fffd)
  )
}
// dispWidth is the on-screen column count of a string (CJK = 2).
function dispWidth(s: string): number {
  let w = 0
  for (const ch of s) w += isWideChar(ch.codePointAt(0) || 0) ? 2 : 1
  return w
}
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

// maxColWidth caps a single column's display width so one huge value (e.g. a long
// text/JSON cell) can't blow up the layout — use \G / \x for full wide values.
const maxColWidth = 60

// renderTable produces a fully-boxed, aligned, ANSI-coloured grid (┌┬┐ / ├┼┤ /
// └┴┘). Widths are measured in display columns (CJK-aware) so mixed ASCII/CJK
// rows stay aligned; cells are single-lined and truncated at maxColWidth.
export function renderTable(t: SynthTable): string[] {
  const clean = (s: string) => (s ?? '').replace(/[\r\n\t]+/g, ' ')
  const heads = t.columns.map((h) => truncateDisp(sanitizeCell(h), maxColWidth))
  const cells = t.rows.map((r) => t.columns.map((_, i) => truncateDisp(sanitizeCell(r[i] ?? ''), maxColWidth)))
  const widths = heads.map((h, i) => {
    let w = dispWidth(h)
    for (const r of cells) w = Math.max(w, dispWidth(r[i]))
    return w
  })
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
  return out
}
