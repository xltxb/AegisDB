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
import type { Completion } from './sqlComplete'

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
  /** 语句结束的另一套规则(例如 Oracle:SET 以换行结束、PL/SQL 块要到 END; 或 /)。
   *  返回 true / false 覆盖默认判断,undefined 表示按默认(以分号结束)。 */
  isComplete?: (s: string) => boolean | undefined
  /** Tab 补全:stmt 是整条语句(多行语句含前面已敲完的行),cursor 是光标在其中的位置。
   *  没有可补的返回 null。见 lib/sqlComplete。 */
  complete?: (stmt: string, cursor: number) => Promise<Completion | null>
}

/** 候选多到这个数就只列前面这些,再说一句还有多少。 */
const MAX_LISTED = 120

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
  // Tab 补全:上一次 Tab 之后输入的样子。第二次 Tab 时输入没变 = 前缀已经补不下去了,列出候选。
  private tabState = ''
  private tabSeq = 0
  // ask() 正在等的那一行回答(SQL*Plus 的 Enter value for x: / ACCEPT)。
  private asking: { resolve: (v: string | null) => void; hide: boolean; buf: string } | null = null

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

  /** 在终端里问一句话、读一行回答 —— SQL*Plus 替换变量的 `Enter value for x:` 与 ACCEPT。
   *
   *  只在一条语句处理到一半(busy)时调用:回答不进历史、不当语句提交。hide 时不回显(ACCEPT … HIDE)。
   *  Ctrl+C 得到 null,由调用方当"取消这条语句"处理。回答之后同一段输入里剩下的(粘贴的后续
   *  语句)照旧排进队列,等 resume() 再跑 —— 不会被当成下一个变量的回答。 */
  ask(question: string, hide = false): Promise<string | null> {
    return new Promise((resolve) => {
      this.asking = { resolve, hide, buf: '' }
      this.term.write(question)
    })
  }

  private askData(data: string) {
    const a = this.asking!
    for (let i = 0; i < data.length; i++) {
      const ch = data[i]
      if (ch === '\r' || ch === '\n') {
        if (ch === '\r' && data[i + 1] === '\n') i++
        this.asking = null
        this.term.write('\r\n')
        const rest = data.slice(i + 1)
        if (rest) this.queued += rest
        a.resolve(a.buf)
        return
      }
      if (ch === '\x03') {
        this.asking = null
        this.queued = ''
        this.term.write('^C\r\n')
        a.resolve(null)
        return
      }
      if (ch === '\x7f' || ch === '\b') {
        if (!a.buf) continue
        const last = Array.from(a.buf).pop() || ''
        a.buf = a.buf.slice(0, a.buf.length - last.length)
        if (!a.hide) {
          const cells = isWideChar(last.codePointAt(0) || 0) ? 2 : 1
          this.term.write('\b'.repeat(cells) + ' '.repeat(cells) + '\b'.repeat(cells))
        }
        continue
      }
      if (ch === '\x1b') { // 方向键之类:回答里不支持光标移动,整段跳过
        const m = data.slice(i).match(/^\x1b\[[0-9;]*[A-Za-z~]/)
        if (m) i += m[0].length - 1
        continue
      }
      if (ch < ' ') continue
      a.buf += ch
      if (!a.hide) this.term.write(ch)
    }
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

  // 把光标挪到 buf 里的位置 cur,不重画。
  //
  // 从前左右键直接往终端写一个"左移 / 右移一格":终端的左移到了行首就停住,不会回到上一行,
  // 于是在折行的长语句里,光标卡在第二行开头,之后编辑器以为它在上一行、终端上它却还在这一行,
  // 再打字就错位;宽字符占两格,移一格也会落在半个汉字上。这里按光标在整段输入里的**显示列**
  // 算出目标行列,跨行时先上下移再定列,并把 renderedRow 记成新的行。
  private moveTo(cur: number) {
    const cols = this.cols()
    const p = this.curPromptLen()
    const from = p + dispWidth(this.buf.slice(0, this.cur))
    const to = p + dispWidth(this.buf.slice(0, cur))
    this.cur = cur
    const fromRow = this.renderedRow
    const toRow = Math.floor(to / cols)
    const toCol = to % cols
    if (toRow === fromRow) {
      const d = toCol - (from % cols)
      if (d > 0) this.term.write(`\x1b[${d}C`)
      else if (d < 0) this.term.write(`\x1b[${-d}D`)
    } else {
      this.term.write(toRow < fromRow ? `\x1b[${fromRow - toRow}A` : `\x1b[${toRow - fromRow}B`)
      this.term.write('\r')
      if (toCol > 0) this.term.write(`\x1b[${toCol}C`)
    }
    this.renderedRow = toRow
  }
  // 一次移一个字符;代理对(emoji 等)是一个字符,不能从中间劈开。
  private moveLeft() {
    if (this.cur <= 0) return
    const low = this.buf.charCodeAt(this.cur - 1)
    const pair = this.cur >= 2 && low >= 0xdc00 && low <= 0xdfff
    this.moveTo(this.cur - (pair ? 2 : 1))
  }
  private moveRight() {
    if (this.cur >= this.buf.length) return
    const high = this.buf.charCodeAt(this.cur)
    const pair = this.cur + 1 < this.buf.length && high >= 0xd800 && high <= 0xdbff
    this.moveTo(this.cur + (pair ? 2 : 1))
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
    // 先把光标挪到整段输入的末尾再换行。光标可能停在折行输入的中间(翻出历史、回到开头补几个
    // 字再回车):在那里换行,结果会从下一行起把输入的后半段盖掉一半,盖不到的尾巴留在屏幕上,
    // 和结果搅在一起。
    this.moveTo(this.buf.length)
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
    const custom = this.opts.isComplete?.(full)
    const complete = custom !== undefined ? custom : (full.startsWith('\\') || full.endsWith(';') || /\\[gG]$/.test(full))
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
    if (this.asking) { this.askData(data); return }
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
        else if (rest.startsWith('\x1b[C')) { this.moveRight(); i += 2 }
        else if (rest.startsWith('\x1b[D')) { this.moveLeft(); i += 2 }
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
      // 单独按下的 Tab 是补全;粘贴进来的一段文本里的 Tab 只是空白 —— 从前直接丢掉,
      // 于是 `SELECT<Tab>id` 粘进来变成了 `SELECTid`。
      if (ch === '\t') {
        if (data === '\t') this.tab()
        else this.insert(' ')
        continue
      }
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

  // 输入此刻的样子:补全结果回来时用它确认人没在这期间接着敲。
  private inputState() {
    return `${this.inCont ? 1 : 0}\u0000${this.pending}\u0000${this.buf}\u0000${this.cur}`
  }

  /** Tab:补全光标处的词。唯一候选补全;多个候选补到公共前缀,再按一次 Tab 列出来。 */
  private async tab() {
    const complete = this.opts.complete
    if (!complete || this.busy) return
    const stmt = (this.pending ? this.pending + '\n' : '') + this.buf
    const cursor = (this.pending ? this.pending.length + 1 : 0) + this.cur
    const state = this.inputState()
    const again = state === this.tabState
    const seq = ++this.tabSeq
    let r: Completion | null = null
    try {
      r = await complete(stmt, cursor)
    } catch {
      return // 取不到候选(网络、权限):什么都不做,和没有候选一样
    }
    // 结果回来之前人又敲了东西、又按了 Tab、或者提交了:这份结果作废
    if (seq !== this.tabSeq || this.busy || this.inputState() !== state || !r) return

    const start = this.cur - r.replace
    const current = this.buf.slice(start, this.cur)
    if (r.text !== current) {
      this.buf = this.buf.slice(0, start) + r.text + this.buf.slice(this.cur)
      this.cur = start + r.text.length
      this.redraw()
      this.tabState = r.candidates.length > 1 ? this.inputState() : ''
      return
    }
    if (r.candidates.length > 1) {
      if (again) this.printCandidates(r.candidates)
      else this.tabState = state
    }
  }

  // 候选按终端宽度排成几列,印在输入行上方(输入行原样重画在下面)。
  private printCandidates(cands: string[]) {
    const shown = cands.slice(0, MAX_LISTED)
    const width = Math.max(...shown.map((c) => dispWidth(c))) + 2
    const perRow = Math.max(1, Math.floor(this.cols() / width))
    const lines: string[] = []
    for (let i = 0; i < shown.length; i += perRow) {
      lines.push(shown.slice(i, i + perRow).map((c) => c + ' '.repeat(Math.max(0, width - dispWidth(c)))).join('').trimEnd())
    }
    if (cands.length > shown.length) lines.push(`… (+${cands.length - shown.length})`)
    this.printAbove(lines)
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
    this.moveTo(this.buf.length) // 同 submitLine:^C 跟在整段输入后面,不插进中间
    this.term.write('^C\r\n')
    this.renderedRow = 0
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
