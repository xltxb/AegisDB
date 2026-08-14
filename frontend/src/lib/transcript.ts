// transcript — the terminal session log, and turning it into a file.
//
// The terminal is where an operator works through an incident, and afterwards
// someone has to say what was run and what came back. Selecting hundreds of lines
// out of xterm is not a way to answer that, so the session is recorded here as it
// happens and can be written out as one text file.
//
// Two things this module is careful about, both of which a naive "dump the
// screen" would get wrong:
//
//  1. It records what the SERVER was asked and what it answered, captured at the
//     write funnel — not xterm's scrollback, which is capped by the renderer and
//     carries no timestamps.
//  2. It masks credential literals exactly as the audit log does. A transcript is
//     a file that leaves the building; the audit log deliberately never stores
//     `IDENTIFIED BY 'hunter2'`, and it would be a strange kind of care that
//     redacted the tamper-proof copy and shipped the plaintext one.

/** Matches a single- or double-quoted SQL literal — mirrors sqlutil.quotedVal. */
const QUOTED = `(?:'(?:[^'\\\\]|\\\\.)*'|"(?:[^"\\\\]|\\\\.)*")`

// Mirrors backend/pkg/sqlutil/redact.go. The two must stay in step: this one
// protects the downloaded file, that one protects the audit row, and they are
// shown the same command text.
/** The plugin in IDENTIFIED WITH <plugin>. MySQL takes it bare or quoted, and the
 *  quoted form is the one its documentation shows — so it is the one people
 *  paste. Matching only the bare form let the password through in the clear. */
const AUTH_PLUGIN = `(?:${QUOTED}|[^\\s'"]+)`

// `as` as well as `by`: IDENTIFIED WITH <plugin> AS '<hash>' carries the stored
// password hash, which is still a credential.
const RE_IDENTIFIED_BY = new RegExp(`(identified\\s+(?:with\\s+${AUTH_PLUGIN}\\s+)?(?:by|as)\\s+(?:password\\s+)?)${QUOTED}`, 'gi')
const RE_PASSWORD_FN = new RegExp(`(\\bpassword\\s*\\(\\s*)${QUOTED}(\\s*\\))`, 'gi')
const RE_SET_PASSWORD = new RegExp(`(set\\s+password\\b.*=\\s*)${QUOTED}`, 'gi')
// \b, or `password` also matches the TAIL of `mysql_native_password` and the mask
// lands mid-statement — output that reads as redacted with the secret beside it.
const RE_PASSWORD_KV = new RegExp(`((?:encrypted\\s+)?\\bpassword\\s*=?\\s*)${QUOTED}`, 'gi')

/**
 * Mask credential literals in a SQL command, leaving the rest intact.
 * Same order as the Go implementation — SET PASSWORD before the looser
 * PASSWORD = form, so the greedy match does not eat the specific one.
 */
export function redactSecrets(sql: string): string {
  return sql
    .replace(RE_IDENTIFIED_BY, "$1'***'")
    .replace(RE_PASSWORD_FN, "$1'***'$2")
    .replace(RE_SET_PASSWORD, "$1'***'")
    .replace(RE_PASSWORD_KV, "$1'***'")
}

/**
 * Strip ANSI escape sequences.
 *
 * Terminal output is written with colour and cursor control. A file keeps the
 * text and drops the control bytes — both because they are noise in an editor and
 * because a transcript is read by tools that would otherwise be steered by them.
 */
export function stripAnsi(s: string): string {
  return s
    // OSC — ESC ] ... terminated by BEL or ST (ESC \).
    .replace(/\x1b\][^]*?(?:\x07|\x1b\\)/g, '')
    // CSI — ESC [ params intermediates final. Covers every colour/cursor code.
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    // Any remaining two-byte escape sequence.
    .replace(/\x1b[@-_]/g, '')
    // Bare control bytes that would otherwise steer whatever opens the file.
    // Tabs and newlines are content and stay.
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '')
}

export interface TranscriptEntry {
  at: Date
  /** 'cmd' is what the operator submitted; 'out' is what came back. */
  kind: 'cmd' | 'out'
  text: string
}

export interface TranscriptMeta {
  instance: string
  database?: string
  user?: string
  /** When the file was written. Passed in so rendering stays deterministic. */
  exportedAt: Date
}

/** Kept per session. Beyond this the OLDEST entries are dropped — and the file
 *  says so, because a transcript that quietly starts in the middle is worse than
 *  no transcript. */
export const MAX_ENTRIES = 5000

/**
 * The UTF-8 byte order mark, written at the start of every exported file.
 *
 * It is how a text file states which encoding it is in. Without it a reader has
 * to guess, and on a Chinese Windows the guess is GBK — which is the whole of
 * the "exports open as mojibake" bug. The backend's CSV exports carry the same
 * mark (service/export.go, handler/admin.go); these three are the only files
 * this system hands to a desktop application, and they should agree.
 */
export const UTF8_BOM = '\ufeff'

/**
 * A session recording. Entries go in as they happen; `render` turns them into the
 * text file.
 */
export class Transcript {
  private entries: TranscriptEntry[] = []
  /** How many entries fell off the front of the buffer. */
  private dropped = 0

  /** Record a command the operator submitted. */
  command(sql: string, at: Date): void {
    this.push({ at, kind: 'cmd', text: redactSecrets(sql) })
  }

  /** Record a line of output. */
  output(line: string, at: Date): void {
    // Redacted too: an error message can echo the offending statement back.
    this.push({ at, kind: 'out', text: redactSecrets(stripAnsi(line)) })
  }

  private push(e: TranscriptEntry): void {
    this.entries.push(e)
    if (this.entries.length > MAX_ENTRIES) {
      this.entries.shift()
      this.dropped++
    }
  }

  get length(): number { return this.entries.length }
  get droppedCount(): number { return this.dropped }
  /** Nothing worth exporting yet — the export control stays disabled. */
  get isEmpty(): boolean { return this.entries.length === 0 }

  clear(): void {
    this.entries = []
    this.dropped = 0
  }

  /**
   * Render the file.
   *
   * Commands are marked `>` and indented output sits under them, so the shape of
   * the session survives in a plain text editor. Every line carries a wall-clock
   * time: the point of reading one of these later is usually to line it up
   * against something else that happened.
   */
  render(meta: TranscriptMeta): string {
    const lines: string[] = []
    lines.push('# Vela 数据库网关 · 终端会话日志')
    lines.push(`# 实例: ${meta.instance}${meta.database ? ` · 库: ${meta.database}` : ''}`)
    if (meta.user) lines.push(`# 操作人: ${meta.user}`)
    lines.push(`# 导出时间: ${fmt(meta.exportedAt)}`)
    lines.push(`# 记录条数: ${this.entries.length}`)
    if (this.dropped > 0) {
      // Stated, not silent. Someone reading this must know the beginning is gone.
      lines.push(`# 注意: 会话过长, 已丢弃最早的 ${this.dropped} 条记录, 本文件从中途开始`)
    }
    lines.push('# 口令字面量已按审计同规则打码')
    lines.push('')

    for (const e of this.entries) {
      const stamp = `[${fmtTime(e.at)}]`
      if (e.kind === 'cmd') {
        const [first, ...rest] = e.text.split('\n')
        lines.push(`${stamp} > ${first}`)
        // A multi-line statement keeps its shape under a continuation marker.
        for (const r of rest) lines.push(`${' '.repeat(stamp.length)} . ${r}`)
      } else {
        lines.push(`${' '.repeat(stamp.length)}   ${e.text}`)
      }
    }
    return lines.join('\n') + '\n'
  }

  /**
   * The same text, prefixed with the byte order mark — what actually goes into
   * the downloaded file.
   *
   * The transcript is almost entirely Chinese: its headings, its notices, and
   * whatever the target database returned. Handed a UTF-8 file with no mark,
   * Notepad and most editors on a Chinese Windows fall back to the ANSI code
   * page (GBK) and open every one of those lines as mojibake. Nothing is wrong
   * with the file — the reader guessed — but the operator has an unreadable
   * transcript and no way to tell why.
   *
   * Separate from render() so the rendering stays the plain text the tests
   * compare against, and so the mark is added exactly once, where the file is
   * made.
   */
  renderFile(meta: TranscriptMeta): string {
    return UTF8_BOM + this.render(meta)
  }

  /** Suggested filename: instance + timestamp, safe on every filesystem. */
  filename(meta: TranscriptMeta): string {
    const safe = meta.instance.replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '') || 'session'
    const d = meta.exportedAt
    const p = (n: number) => String(n).padStart(2, '0')
    return `vela-${safe}-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}.log`
  }
}

function p2(n: number): string { return String(n).padStart(2, '0') }
function fmtTime(d: Date): string { return `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}` }
function fmt(d: Date): string {
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${fmtTime(d)}`
}
