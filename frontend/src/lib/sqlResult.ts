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

// ---- rendering ----------------------------------------------------------

const pad = (s: string, w: number, right: boolean) =>
  right ? ' '.repeat(Math.max(0, w - s.length)) + s : s + ' '.repeat(Math.max(0, w - s.length))

// renderTable produces psql-style aligned, ANSI-coloured lines (no trailing \n).
export function renderTable(t: SynthTable): string[] {
  const widths = t.columns.map((col, i) => {
    let w = col.length
    for (const r of t.rows) w = Math.max(w, r[i]?.length ?? 0)
    return w
  })
  const vert = c(ANSI.gray, '│')
  const cell = (s: string, i: number, right: boolean) => ' ' + pad(s, widths[i], right) + ' '

  const header = t.columns.map((col, i) => c(ANSI.bold, cell(col, i, t.numeric[i]))).join(vert)
  const rule = c(ANSI.gray, widths.map((w) => '─'.repeat(w + 2)).join('┼'))
  const body = t.rows.map((r) =>
    r
      .map((v, i) => (t.numeric[i] ? c(ANSI.cyan, cell(v, i, true)) : cell(v, i, false)))
      .join(vert),
  )
  return [header, rule, ...body]
}
