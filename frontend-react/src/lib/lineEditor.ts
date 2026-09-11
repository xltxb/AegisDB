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
import { dispWidth, isWideChar } from './textWidth'
import { flattenStatement } from './sqlFlatten'

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
  /** ANSI syntax colouring for the input line. MUST keep the printable length
   *  unchanged (colours only) — all cursor math runs on the raw buffer. */
  highlight?: (s: string) => string
}

/**
 * 返回 `rest` 开头那条转义序列的长度(至少 1,即 ESC 本身)。
 *
 * CSI: `ESC [` 后跟参数字节 0x30–0x3F、中间字节 0x20–0x2F,以 0x40–0x7E 收尾。
 * SS3: `ESC O` 再跟一个字节(F1–F4 等)。
 */
export function escapeLength(rest: string): number {
  if (rest[1] === '[') {
    let i = 2
    while (i < rest.length && /[\x30-\x3f\x20-\x2f]/.test(rest[i])) i++
    return i < rest.length && /[\x40-\x7e]/.test(rest[i]) ? i + 1 : rest.length
  }
  if (rest[1] === 'O' && rest.length >= 3) return 3
  return 1
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

  /** 是否正在等一条语句的回执。调用方据此判断 Ctrl+C 该发"取消执行"还是只清当前行。 */
  isBusy(): boolean { return this.busy }

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

  /** Type `text` as if it had been pasted: echoed at the prompt, then submitted
   *  by the newline it ends with, statement by statement for a batch.
   *
   *  Used by the snippet hotkeys, and going through the same door as a paste is
   *  the point of it. There is no path here that hands a statement to the server
   *  without onSubmit seeing it, so a snippet cannot end up running against a
   *  connection without the risk check the caller does in onSubmit. */
  feed(text: string) {
    if (!text) return
    this.onData(text)
  }

  /** 把整段文本作为**一条命令**提交 —— 不按 `;` 拆开。
   *
   *  粘贴一批语句时走它。逐条提交会让安全的那几条先跑掉、只有高危那条去等审批,
   *  于是审批人看到的是一条脱离上下文的语句,而库里已经变了一半 —— 一批语句本来
   *  就是一件事,该整批看、整批批、整批执行。
   *
   *  判定与执行仍然是逐条的,只是都发生在**服务端**:整批走一次判定取最严裁决,
   *  下发时一条一条给驱动(见 service.execCommand)。这里改的只是"提交的粒度"。 */
  submitBatch(text: string) {
    const stmt = text.trim()
    if (!stmt || this.busy) return
    const entry = flattenStatement(stmt)
    if (entry && this.history[this.history.length - 1] !== entry) this.history.push(entry)
    // 先把当前输入块整个擦掉(它可能不止一行),再回显这次提交的全文 —— 屏幕和会话
    // 日志都要留下"到底提交了什么",这是事后唯一能对照的东西。
    if (this.renderedRow > 0) this.term.write(`\x1b[${this.renderedRow}A`)
    this.term.write('\r\x1b[0J')
    this.renderedRow = 0
    const lines = stmt.split('\n')
    const paint = (l: string) => this.opts.highlight?.(l) ?? l
    this.term.write(this.opts.prompt() + paint(lines[0]) + '\r\n')
    for (const l of lines.slice(1)) this.term.write(this.opts.contPrompt() + paint(l) + '\r\n')
    this.busy = true
    this.reset()
    this.opts.onSubmit(stmt)
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
    this.term.write(this.curPrompt() + (this.opts.highlight?.(this.buf) ?? this.buf))

    const promptLen = this.curPromptLen()
    // 列数按**显示宽度**算,不是字符个数。一条 40 个字符的中文语句占 58 列;
    // 按个数算会把折行算少,重绘时就少上移一行,旧内容的第一行连同提示符一起留在
    // 屏幕上 —— 上翻历史命令时最容易撞见,因为那一下整行内容都被换掉了。
    const end = promptLen + dispWidth(this.buf)
    // A buffer ending exactly at the right edge leaves the terminal in "pending
    // wrap": the cursor is still on the last full row rather than the next one,
    // which would make the row arithmetic below off by one. Emit one space to
    // commit the wrap so both agree. It sits past the text and is erased by the
    // next redraw.
    if (end > 0 && end % cols === 0) this.term.write(' ')

    const target = promptLen + dispWidth(this.buf.slice(0, this.cur))
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
    const before = this.curPromptLen() + dispWidth(this.buf)
    const appending = this.cur === this.buf.length
    this.buf = this.buf.slice(0, this.cur) + s + this.buf.slice(this.cur)
    this.cur += s.length
    const after = this.curPromptLen() + dispWidth(this.buf)

    // Typing at the end of a line, without crossing a row boundary, needs no
    // repaint at all — the characters can simply be emitted where the cursor
    // already is. Redrawing for it means rewriting prompt+buffer on EVERY
    // keystroke, which on a long statement the terminal shows as flicker.
    //
    // The row must be unchanged (so the remembered row stays valid) and the text
    // must not land exactly on the right edge, where the terminal holds a pending
    // wrap that redraw() handles explicitly.
    //
    // With syntax highlighting on, a run that contains a token boundary (space,
    // ';', '(' …) falls through to redraw() so the word just completed gets its
    // colour; mid-word keystrokes keep the fast path and stay flicker-free — a
    // keyword only becomes one when it is finished anyway.
    const boundary = this.opts.highlight && /[^A-Za-z0-9_]/.test(s)
    if (!boundary && appending && after % cols !== 0 && Math.floor(before / cols) === Math.floor(after / cols)) {
      this.term.write(s)
      return
    }
    this.redraw()
  }

  private submitLine() {
    const line = this.buf
    this.pending += (this.pending ? '\n' : '') + line
    this.term.write('\r\n')
    // 这一行结束了,光标已经在全新的一行上,输入块不再有任何一行在光标上方。
    // 不清零的话,下一次 redraw 会带着**上一条**语句的行数往上移,然后 \x1b[0J
    // 从那里往下擦 —— 擦掉的是上一条命令的回显和它的结果。只有上一条折过行时
    // 才会发作,所以长语句、多行语句、含中文的语句中招,短英文语句不会。
    this.renderedRow = 0
    const full = this.pending.trim()

    if (full === '') {
      this.inCont = false
      this.pending = ''
      this.reset()
      this.term.write(this.opts.prompt())
      return
    }
    // A statement completes on ';', a leading backslash meta-command, or a MySQL
    // display terminator \g (horizontal) / \G (vertical).
    const complete = full.startsWith('\\') || full.endsWith(';') || /\\[gG]$/.test(full)
    if (complete) {
      // 只有**完整的语句**进历史。从前每敲一次回车就记一条,于是一条六行的语句会
      // 在历史里留下六个越来越长的前缀,上翻时要在半截 SQL 里一路翻过去才找得到
      // 真正执行过的那条。
      //
      // 压平必须是语义安全的:换行是 `--` 注释的终止符,直接换成空格会让注释吃掉
      // 它后面的一切 —— 首次执行没事(那时换行还在),上翻再执行就变成半截语句。
      // 见 flattenStatement。
      const entry = flattenStatement(full)
      if (entry && this.history[this.history.length - 1] !== entry) this.history.push(entry)

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
        else {
          // 未识别的转义序列:**整条吞掉**,不能只跳过 ESC 引导符。
          //
          // 只跳 ESC 的话,后面的 `[`、`1`、`;`、`2`、`C` 会被当成可打印字符逐个
          // 写进 SQL 缓冲 —— 按一下 Shift+→ 就在语句里留下 `[1;2C`,F1 留下 `OP`,
          // 然后随语句一起提交到目标库。
          //
          // CSI(`ESC [` … 终止符在 @~ 之间)与 SS3(`ESC O` + 一个字母)覆盖了
          // 绝大多数键盘会发出的序列;再认不出来的,至少把 ESC 自己丢掉。
          i += escapeLength(rest) - 1
        }
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
    const before = this.curPromptLen() + dispWidth(this.buf)
    const removed = this.buf[this.cur - 1] ?? ''
    const atEnd = this.cur === this.buf.length
    this.buf = this.buf.slice(0, this.cur - 1) + this.buf.slice(this.cur)
    this.cur--
    // Same reasoning as insert: deleting the last character of a line that does
    // not sit on a row boundary is "back up, blank it, back up" — no repaint.
    //
    // 宽字符占两格,要退两格、擦两格。只擦一格会留下半个汉字的残影,而那半格之后
    // 还会被当成一格参与计算,错位一路带下去。删掉它之后如果正好落在行边界上,
    // 这条快捷路径就不成立了(光标要跨行回去),交给 redraw。
    const cells = isWideChar(removed.codePointAt(0) || 0) ? 2 : 1
    if (atEnd && before % cols !== 0 && (before - cells) % cols !== 0) {
      this.term.write('\b'.repeat(cells) + ' '.repeat(cells) + '\b'.repeat(cells))
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
