// sqlTables — which tables (or MongoDB collections) a command touches.
//
// The approval queue and the audit log list one row per command, and a command
// may be an entire script. Showing it inline forces a choice between truncating
// it (hiding the part that matters) and wrecking the row. What a reviewer
// actually scans a queue for is WHICH DATA is involved, so the row carries the
// tables and the full command lives in the detail card.
//
// This is a display aid, never a security control: it is a best-effort read of
// the text, and it returns nothing rather than guessing when it cannot tell. The
// risk engine does its own parsing and does not consult this.

/** Keywords that introduce a table name, and how many names may follow. */
const SINGLE = ['from', 'join', 'into', 'update', 'table']
/** `RENAME TABLE a TO b` — the target after TO is a table too. */
const ALSO_AFTER = ['to']

/** Words that can follow an introducer but are not table names. */
const NOT_A_NAME = new Set([
  'select', 'table', 'if', 'exists', 'not', 'only', 'lateral', 'unnest', 'values',
  'dual', 'where', 'set', 'on', 'using', 'as', 'outer', 'inner', 'left', 'right',
  'full', 'cross', 'natural', 'join', 'order', 'group', 'by', 'limit', 'offset',
])

/** strip removes comments and string literals so a name can never be lifted out
 *  of prose or data. Quoted IDENTIFIERS are kept — they are names. */
function strip(sql: string): string {
  let out = ''
  for (let i = 0; i < sql.length; i++) {
    const c = sql[i]
    if (c === '-' && sql[i + 1] === '-') {
      while (i < sql.length && sql[i] !== '\n') i++
      out += ' '
      continue
    }
    if (c === '/' && sql[i + 1] === '*') {
      i += 2
      while (i + 1 < sql.length && !(sql[i] === '*' && sql[i + 1] === '/')) i++
      i++
      out += ' '
      continue
    }
    if (c === "'") { // string literal — replaced wholesale
      i++
      while (i < sql.length && sql[i] !== "'") i++
      out += " '' "
      continue
    }
    out += c
  }
  return out
}

/** unquote strips identifier quoting (`x`, "x", [x]).
 *
 *  It also reports whether the name WAS quoted: quoting is exactly how a reserved
 *  word is used as an identifier, so a quoted `order` is a table while a bare
 *  ORDER is the start of ORDER BY. Without that distinction the blocklist below
 *  would discard legitimately-quoted names. */
function unquote(word: string): { name: string; quoted: boolean } {
  let w = word.trim().replace(/[;,()]+$/g, '')
  const quoted =
    (w.startsWith('`') && w.endsWith('`')) ||
    (w.startsWith('"') && w.endsWith('"')) ||
    (w.startsWith('[') && w.endsWith(']'))
  if (quoted) w = w.slice(1, -1)
  return { name: w, quoted }
}

const NAME_RE = /^[A-Za-z_][A-Za-z0-9_$.]*$/

/** mongoCollection reads the collection out of `db.<name>.<method>(…)` or
 *  `db.getCollection("<name>")`. */
function mongoCollection(cmd: string): string[] {
  const viaHelper = /\bdb\s*\.\s*getCollection\s*\(\s*["'`]([^"'`]+)["'`]\s*\)/.exec(cmd)
  if (viaHelper) return [viaHelper[1]]
  const direct = /\bdb\s*\.\s*([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*[A-Za-z_]/.exec(cmd)
  return direct ? [direct[1]] : []
}

/** extractTables returns the tables/collections a command touches, in the order
 *  they appear, without repeats. */
export function extractTables(command: string): string[] {
  const cmd = (command || '').trim()
  if (!cmd) return []
  if (/\bdb\s*\./.test(cmd)) return mongoCollection(cmd) // MongoDB shell syntax

  const words = strip(cmd)
    .replace(/([(),;])/g, ' $1 ')
    .split(/\s+/)
    .filter(Boolean)

  const out: string[] = []
  const seen = new Set<string>()
  for (let i = 0; i < words.length; i++) {
    const kw = words[i].toLowerCase().replace(/[;,()]/g, '')
    if (!SINGLE.includes(kw) && !ALSO_AFTER.includes(kw)) continue
    // `DELETE FROM` / `INSERT INTO` / `UPDATE x` / `... TABLE x` all place the
    // name immediately after the keyword.
    const { name, quoted } = unquote(words[i + 1] || '')
    const lower = name.toLowerCase()
    if (!name || !NAME_RE.test(name)) continue
    if (!quoted && NOT_A_NAME.has(lower)) continue
    if (!seen.has(lower)) {
      seen.add(lower)
      out.push(name)
    }
  }
  return out
}

/** tablesLabel renders a compact cell: the first few names, then how many more. */
export function tablesLabel(tables: string[], max = 3): string {
  if (!tables.length) return '—'
  if (tables.length <= max) return tables.join(', ')
  return `${tables.slice(0, max).join(', ')} +${tables.length - max}`
}
