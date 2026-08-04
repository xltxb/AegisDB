// LineEditor — a small readline for xterm.js tuned for a SQL console.
//
// Model (psql-style): editing operates on the CURRENT physical line only;
// completed lines are frozen above. A statement is submitted when the
// accumulated text ends with ';' or is a backslash meta-command. This keeps
// cursor math single-line (robust) while still supporting multi-line SQL.
//
// While a submitted statement runs, the editor is "busy" and ignores input
// until the caller invokes resume(). Ctrl+C always works.

import type { Terminal } from '@xterm/xterm'

export interface LineEditorOpts {
  /** ANSI-coloured primary prompt, e.g. `orders ❯ ` */
  prompt: () => string
  /** visible (printable) length of the primary prompt */
  promptLen: () => number
  /** ANSI-coloured continuation prompt, e.g. `      ... ` */
  contPrompt: () => string
  contPromptLen: () => number
  /** called with a complete statement; editor is busy until resume() */
  onSubmit: (stmt: string) => void
  /** called on Ctrl+C so the caller can abort in-flight work */
  onInterrupt?: () => void
}

export class LineEditor {
  private buf = ''
  private cur = 0
  private pending = '' // earlier physical lines of a multi-line statement
  private inCont = false
  private busy = false
  private history: string[] = []
  private hidx = 0 // == history.length means "editing a fresh draft"
  private draft = ''
  // When a paste contains several statements, only the first can run immediately;
  // the rest of that pasted chunk is stashed here and re-fed on resume(), so a
  // batch of copied SQL executes one statement after another.
  private queued = ''

  constructor(private term: Terminal, private opts: LineEditorOpts) {
    term.onData((d) => this.onData(d))
  }

  /** Print the primary prompt and start accepting input. */
  start() {
    this.busy = false
    this.inCont = false
    this.pending = ''
    this.queued = ''
    this.reset()
    this.term.write(this.opts.prompt())
  }

  /** True while a submitted statement is executing (input is ignored). */
  get running() {
    return this.busy
  }

  /** Insert output lines above the current input line, then redraw the prompt.
   *  Use for out-of-band output (script results, notices) while the editor is idle. */
  printAbove(lines: string[]) {
    // Erase the WHOLE input block first — it may span several rows, and clearing
    // only the current one left the earlier rows on screen with the output
    // printed under them, so the half-typed statement appeared twice (see redraw).
    if (this.renderedRow > 0) this.term.write(`\x1b[${this.renderedRow}A`)
    this.term.write('\r\x1b[0J')
    this.renderedRow = 0
    for (const l of lines) this.term.write(l + '\r\n')
    if (!this.busy) this.redraw()
  }

  /** Abandon the remainder of a pasted batch.
   *
   *  A paste of several statements runs one at a time, with the rest stashed
   *  until resume(). When the statement in flight raises an approval or MFA
   *  prompt and the user cancels — or the gateway denies it — the user means
   *  "stop this batch", but resume() replays the remainder regardless: the
   *  terminal printed "cancelled, not executed" and then ran the next statements
   *  anyway (EF4). Callers handling a cancellation must call this before
   *  resume(). Ctrl+C already does the same thing.
   *  @returns how many characters of pending input were dropped, so the caller
   *  can tell the user something was discarded. */
  discardQueued(): number {
    const n = this.queued.length
    this.queued = ''
    return n
  }

  /** Resume input after a submitted statement finished. */
  resume() {
    if (!this.busy) return
    this.busy = false
    this.inCont = false
    this.pending = ''
    this.reset()
    this.term.write('\r\n' + this.opts.prompt())
    // Continue a pasted multi-statement batch: re-feed the stashed remainder,
    // which echoes + submits the next statement (and re-stashes what's left).
    if (this.queued) {
      const rest = this.queued
      this.queued = ''
      this.onData(rest)
    }
  }

  private reset() {
    this.buf = ''
    this.cur = 0
    this.hidx = this.history.length
    this.draft = ''
  }

  private curPromptLen() {
    return this.inCont ? this.opts.contPromptLen() : this.opts.promptLen()
  }
  private curPrompt() {
    return this.inCont ? this.opts.contPrompt() : this.opts.prompt()
  }

  // renderedRow is which row of the last render the cursor was left on, so the
  // next redraw knows how far UP the block it has to go before erasing.
  private renderedRow = 0

  private cols() {
    return Math.max(1, this.term.cols || 80)
  }

  // Redraw the current input line, which may occupy SEVERAL terminal rows.
  //
  // The old version erased with `\r\x1b[2K` — carriage return plus "erase this
  // row". That only holds while the line fits on one row. Once prompt+buffer is
  // wider than the terminal it wraps, and the cursor sits on the LAST row: the
  // erase cleared just that row, the rewrite wrapped again and landed BELOW the
  // stale copy, so every keystroke left another copy behind. Holding backspace on
  // a long statement filled the screen with dozens of near-identical lines.
  //
  // So: walk back up to the first row of what was drawn, erase from there to the
  // end of the display, redraw, then place the cursor.
  private redraw() {
    const cols = this.cols()
    if (this.renderedRow > 0) this.term.write(`\x1b[${this.renderedRow}A`)
    this.term.write('\r\x1b[0J')
    this.term.write(this.curPrompt() + this.buf)

    const promptLen = this.curPromptLen()
    const end = promptLen + this.buf.length
    // A buffer ending exactly at the right edge leaves the terminal in "pending
    // wrap": the cursor is still on the last full row rather than the next one,
    // which would make the row arithmetic below off by one. Emit one space to
    // commit the wrap so both agree. It sits past the text and is erased by the
    // next redraw.
    if (end > 0 && end % cols === 0) this.term.write(' ')

    const target = promptLen + this.cur
    const endRow = Math.floor(end / cols)
    const targetRow = Math.floor(target / cols)
    const targetCol = target % cols
    if (endRow > targetRow) this.term.write(`\x1b[${endRow - targetRow}A`)
    this.term.write('\r')
    if (targetCol > 0) this.term.write(`\x1b[${targetCol}C`)
    this.renderedRow = targetRow
  }

  private insert(s: string) {
    const cols = this.cols()
    const before = this.curPromptLen() + this.buf.length
    const appending = this.cur === this.buf.length
    this.buf = this.buf.slice(0, this.cur) + s + this.buf.slice(this.cur)
    this.cur += s.length
    const after = this.curPromptLen() + this.buf.length

    // Typing at the end of a line, without crossing a row boundary, needs no
    // repaint at all — the characters can simply be emitted where the cursor
    // already is. Redrawing for it means rewriting prompt+buffer on EVERY
    // keystroke, which on a long statement the terminal shows as flicker.
    //
    // The row must be unchanged (so the remembered row stays valid) and the text
    // must not land exactly on the right edge, where the terminal holds a pending
    // wrap that redraw() handles explicitly.
    if (appending && after % cols !== 0 && Math.floor(before / cols) === Math.floor(after / cols)) {
      this.term.write(s)
      return
    }
    this.redraw()
  }

  private submitLine() {
    const line = this.buf
    this.pending += (this.pending ? '\n' : '') + line
    this.term.write('\r\n')
    const full = this.pending.trim()

    if (full === '') {
      this.inCont = false
      this.pending = ''
      this.reset()
      this.term.write(this.opts.prompt())
      return
    }
    // remember non-empty statements for history (collapse multi-line to spaces)
    if (line.trim()) {
      const entry = full.replace(/\s*\n\s*/g, ' ')
      if (this.history[this.history.length - 1] !== entry) this.history.push(entry)
    }

    // A statement completes on ';', a leading backslash meta-command, or a MySQL
    // display terminator \g (horizontal) / \G (vertical).
    const complete = full.startsWith('\\') || full.endsWith(';') || /\\[gG]$/.test(full)
    if (complete) {
      const stmt = this.pending
      this.busy = true
      this.reset()
      this.opts.onSubmit(stmt)
    } else {
      this.inCont = true
      this.buf = ''
      this.cur = 0
      this.term.write(this.opts.contPrompt())
    }
  }

  private historyPrev() {
    if (this.hidx === this.history.length) this.draft = this.buf
    if (this.hidx > 0) {
      this.hidx--
      this.buf = this.history[this.hidx]
      this.cur = this.buf.length
      this.redraw()
    }
  }
  private historyNext() {
    if (this.hidx < this.history.length) {
      this.hidx++
      this.buf = this.hidx === this.history.length ? this.draft : this.history[this.hidx]
      this.cur = this.buf.length
      this.redraw()
    }
  }

  private onData(data: string) {
    if (this.busy) {
      // Ctrl+C aborts the running statement and drops any queued paste batch.
      if (data.indexOf('\x03') >= 0) { this.queued = ''; this.opts.onInterrupt?.(); return }
      // Otherwise buffer input received while a statement runs (a paste split
      // across events, or type-ahead) so it isn't lost; resume() replays it.
      this.queued += data
      return
    }
    for (let i = 0; i < data.length; i++) {
      const ch = data[i]
      // ---- escape sequences ----
      if (ch === '\x1b') {
        const rest = data.slice(i)
        if (rest.startsWith('\x1b[A')) { this.historyPrev(); i += 2 }
        else if (rest.startsWith('\x1b[B')) { this.historyNext(); i += 2 }
        else if (rest.startsWith('\x1b[C')) { if (this.cur < this.buf.length) { this.cur++; this.term.write('\x1b[C') } i += 2 }
        else if (rest.startsWith('\x1b[D')) { if (this.cur > 0) { this.cur--; this.term.write('\x1b[D') } i += 2 }
        else if (rest.startsWith('\x1b[H') || rest.startsWith('\x1b[1~')) { this.toHome(); i += rest.startsWith('\x1b[1~') ? 3 : 2 }
        else if (rest.startsWith('\x1b[F') || rest.startsWith('\x1b[4~')) { this.toEnd(); i += rest.startsWith('\x1b[4~') ? 3 : 2 }
        else if (rest.startsWith('\x1b[3~')) { this.del(); i += 3 }
        else { /* unknown escape: skip the introducer */ }
        continue
      }
      // ---- control chars ----
      if (ch === '\r' || ch === '\n') {
        if (ch === '\r' && data[i + 1] === '\n') i++ // swallow CRLF pair
        this.submitLine()
        // If that submitted a statement (now busy), the rest of this chunk is a
        // pasted batch's remaining statements — stash them for resume() to run.
        if (this.busy) {
          const rest = data.slice(i + 1)
          if (rest) this.queued += rest
          return
        }
        continue
      }
      if (ch === '\x7f' || ch === '\b') { this.backspace(); continue } // backspace
      if (ch === '\x03') { this.ctrlC(); continue } // Ctrl+C
      if (ch === '\x0c') { this.ctrlL(); continue } // Ctrl+L
      if (ch === '\x15') { this.killLine(); continue } // Ctrl+U
      if (ch === '\x01') { this.toHome(); continue } // Ctrl+A
      if (ch === '\x05') { this.toEnd(); continue } // Ctrl+E
      if (ch === '\t') { continue } // ignore tab for now
      if (ch < ' ') continue // drop other control chars
      // ---- printable (collect a run) ----
      let run = ch
      while (i + 1 < data.length && data[i + 1] >= ' ' && data[i + 1] !== '\x7f' && data[i + 1] !== '\x1b') {
        run += data[++i]
      }
      // treat embedded newlines already handled above; strip any stray \r
      this.insert(run.replace(/[\r\n]/g, ' '))
    }
  }

  private backspace() {
    if (this.cur <= 0) return
    const cols = this.cols()
    const before = this.curPromptLen() + this.buf.length
    const atEnd = this.cur === this.buf.length
    this.buf = this.buf.slice(0, this.cur - 1) + this.buf.slice(this.cur)
    this.cur--
    // Same reasoning as insert: deleting the last character of a line that does
    // not sit on a row boundary is "back up, blank it, back up" — no repaint.
    if (atEnd && before % cols !== 0) {
      this.term.write('\b \b')
      return
    }
    this.redraw()
  }
  private del() {
    if (this.cur < this.buf.length) {
      this.buf = this.buf.slice(0, this.cur) + this.buf.slice(this.cur + 1)
      this.redraw()
    }
  }
  private toHome() { this.cur = 0; this.redraw() }
  private toEnd() { this.cur = this.buf.length; this.redraw() }
  private killLine() { this.buf = ''; this.cur = 0; this.redraw() }

  private ctrlC() {
    this.term.write('^C\r\n')
    this.inCont = false
    this.pending = ''
    this.queued = '' // abort any remaining pasted-batch statements
    this.reset()
    this.term.write(this.opts.prompt())
    this.opts.onInterrupt?.()
  }

  private ctrlL() {
    this.term.clear()
    // clear() homes the cursor and nothing of ours is on screen any more, so the
    // remembered row would send the next redraw upwards into cleared space.
    this.renderedRow = 0
    this.redraw()
  }
}
