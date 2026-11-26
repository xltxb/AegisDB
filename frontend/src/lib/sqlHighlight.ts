// sqlHighlight — one small SQL tokenizer, two renderers.
//
// toAnsi() colours the terminal input line (used by LineEditor.redraw, so the
// printable length MUST equal the input length — colours only, no reflow), and
// toHtml() colours the DDL/source viewer (escapes everything, wraps tokens in
// classed <span>s). One tokenizer, so the two views never disagree on what a
// keyword is.

import { ANSI } from './sqlResult'

type Kind = 'kw' | 'str' | 'num' | 'cmt' | 'meta' | 'plain'
interface Token { text: string; kind: Kind }

// Keywords across the engines the gateway speaks (MySQL/PG/Oracle).
// Uppercased lookup; matching is case-insensitive.
// Also the keyword candidates for Tab completion (lib/termCompletion), so what
// completes is exactly what highlights.
export const SQL_KEYWORDS = [...new Set((
  'select insert update delete from where and or not in is null like between exists ' +
  'join inner left right full outer cross on using group by having order asc desc limit offset ' +
  'union all distinct as case when then else end create alter drop truncate rename table view ' +
  'index sequence trigger function procedure package body database schema column constraint ' +
  'primary key foreign references unique check default values set into begin declare cursor ' +
  'if elsif elseif loop while for return returns returning language plpgsql sql deterministic ' +
  'grant revoke commit rollback savepoint transaction start explain analyze show describe desc use ' +
  'with recursive replace temporary temp exists cascade restrict comment engine charset collate ' +
  'varchar char text int integer bigint smallint tinyint decimal numeric float double real date ' +
  'time timestamp datetime interval boolean bool blob clob number nvarchar2 varchar2 serial ' +
  'auto_increment identity generated always stored virtual partition cluster or replace'
).toUpperCase().split(/\s+/))]
const KEYWORDS = new Set(SQL_KEYWORDS)

// 粘性(y)正则:从 lastIndex 处原地匹配,不切子串。
const wordRe = /[A-Za-z_][A-Za-z0-9_$]*/y
const numRe = /(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?/y
const identCharRe = /[A-Za-z0-9_]/

/**
 * Tokenize one SQL text. Never throws; anything unrecognised is 'plain'.
 *
 * Linear in the input: every rule matches in place at index i (startsWith / indexOf with a start
 * position, sticky regexes). The first version took `sql.slice(i)` for every token — quadratic, so the
 * query editor froze on a few thousand lines and the DDL viewer crawled on a large package body
 * (2026-10-26). Token boundaries and kinds are unchanged.
 */
export function tokenizeSql(sql: string): Token[] {
  const out: Token[] = []
  const n = sql.length
  let i = 0
  const push = (end: number, kind: Kind) => { if (end > i) out.push({ text: sql.slice(i, end), kind }); i = end }
  while (i < n) {
    const ch = sql[i]
    // comments
    if (ch === '-' && sql[i + 1] === '-') {
      const nl = sql.indexOf('\n', i)
      push(nl < 0 ? n : nl, 'cmt'); continue
    }
    if (ch === '/' && sql[i + 1] === '*') {
      const close = sql.indexOf('*/', i)
      push(close < 0 ? n : close + 2, 'cmt'); continue
    }
    // psql/mysql client meta-command at line start
    if (ch === '\\' && (i === 0 || sql[i - 1] === '\n')) {
      const nl = sql.indexOf('\n', i)
      push(nl < 0 ? n : nl, 'meta'); continue
    }
    // quoted strings / quoted identifiers (kept single-token; \' and '' escapes)
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1
      while (j < n) {
        if (sql[j] === '\\' && ch === "'") { j += 2; continue }
        if (sql[j] === ch) { if (ch === "'" && sql[j + 1] === ch) { j += 2; continue } j++; break }
        j++
      }
      push(Math.min(j, n), 'str'); continue
    }
    // numbers
    numRe.lastIndex = i
    const nm = numRe.exec(sql)
    if (nm && !identCharRe.test(sql[i - 1] ?? '')) {
      push(i + nm[0].length, 'num'); continue
    }
    // words → keyword or plain identifier
    wordRe.lastIndex = i
    const wm = wordRe.exec(sql)
    if (wm) {
      push(i + wm[0].length, KEYWORDS.has(wm[0].toUpperCase()) ? 'kw' : 'plain'); continue
    }
    // anything else, one char at a time (operators, punctuation, whitespace)
    push(i + 1, 'plain')
  }
  return out
}

const ANSI_BY_KIND: Record<Kind, string> = {
  kw: ANSI.cyan,
  str: ANSI.yellow,
  num: ANSI.magenta,
  cmt: ANSI.gray,
  meta: ANSI.green,
  plain: '',
}

/** ANSI-coloured text for xterm. Printable length is unchanged. */
export function highlightSqlAnsi(sql: string): string {
  let out = ''
  for (const t of tokenizeSql(sql)) {
    out += t.kind === 'plain' ? t.text : ANSI_BY_KIND[t.kind] + t.text + ANSI.reset
  }
  return out
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** HTML with classed spans (sqlh-kw / -str / -num / -cmt / -meta) for the source viewer. */
export function highlightSqlHtml(sql: string): string {
  let out = ''
  for (const t of tokenizeSql(sql)) {
    const esc = escapeHtml(t.text)
    out += t.kind === 'plain' ? esc : `<span class="sqlh-${t.kind}">${esc}</span>`
  }
  return out
}
