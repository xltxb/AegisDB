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

// Keywords across the engines the gateway speaks (MySQL/PG/Oracle/SQLite).
// Uppercased lookup; matching is case-insensitive.
const KEYWORDS = new Set((
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
).toUpperCase().split(/\s+/))

const wordRe = /^[A-Za-z_][A-Za-z0-9_$]*/
const numRe = /^(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?/

/** Tokenize one SQL text. Never throws; anything unrecognised is 'plain'. */
export function tokenizeSql(sql: string): Token[] {
  const out: Token[] = []
  let i = 0
  const push = (text: string, kind: Kind) => { if (text) out.push({ text, kind }) }
  while (i < sql.length) {
    const rest = sql.slice(i)
    // comments
    if (rest.startsWith('--')) {
      const nl = rest.indexOf('\n')
      const end = nl < 0 ? rest.length : nl
      push(rest.slice(0, end), 'cmt'); i += end; continue
    }
    if (rest.startsWith('/*')) {
      const close = rest.indexOf('*/')
      const end = close < 0 ? rest.length : close + 2
      push(rest.slice(0, end), 'cmt'); i += end; continue
    }
    // psql/mysql client meta-command at line start
    if (rest[0] === '\\' && (i === 0 || sql[i - 1] === '\n')) {
      const nl = rest.indexOf('\n')
      const end = nl < 0 ? rest.length : nl
      push(rest.slice(0, end), 'meta'); i += end; continue
    }
    // quoted strings / quoted identifiers (kept single-token; \' and '' escapes)
    const q = rest[0]
    if (q === "'" || q === '"' || q === '`') {
      let j = 1
      while (j < rest.length) {
        if (rest[j] === '\\' && q === "'") { j += 2; continue }
        if (rest[j] === q) { if (q === "'" && rest[j + 1] === q) { j += 2; continue } j++; break }
        j++
      }
      push(rest.slice(0, j), 'str'); i += j; continue
    }
    // numbers
    const nm = numRe.exec(rest)
    if (nm && !/[A-Za-z0-9_]/.test(sql[i - 1] ?? '')) {
      push(nm[0], 'num'); i += nm[0].length; continue
    }
    // words → keyword or plain identifier
    const wm = wordRe.exec(rest)
    if (wm) {
      push(wm[0], KEYWORDS.has(wm[0].toUpperCase()) ? 'kw' : 'plain')
      i += wm[0].length; continue
    }
    // anything else, one char at a time (operators, punctuation, whitespace)
    push(rest[0], 'plain'); i++
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
