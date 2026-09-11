import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'
import { isWideChar } from '../../src/lib/textWidth'
import { highlightSqlAnsi } from '../../src/lib/sqlHighlight'

// A minimal terminal screen model: enough of xterm's behaviour to tell whether a
// redraw actually REPLACED what it drew before, or merely drew again below it.
// Asserting on the rendered screen keeps these tests about behaviour rather than
// about which escape sequences the editor happens to emit.
class Screen {
  rows: string[][] = [[]]
  row = 0
  col = 0
  constructor(readonly cols: number) {}

  private cell(r: number, c: number) {
    while (this.rows.length <= r) this.rows.push([])
    const line = this.rows[r]
    while (line.length <= c) line.push(' ')
    return line
  }

  write(s: string) {
    for (let i = 0; i < s.length; i++) {
      const ch = s[i]
      if (ch === '\x1b' && s[i + 1] === '[') {
        const m = /^\x1b\[([0-9;]*)([A-Za-z])/.exec(s.slice(i))
        if (!m) { i++; continue }
        const n = parseInt(m[1] || '0', 10) || (m[1] ? 0 : 1)
        switch (m[2]) {
          case 'm': break // colour — no screen effect
          case 'A': this.row = Math.max(0, this.row - (parseInt(m[1] || '1', 10) || 1)); break
          case 'B': this.row += parseInt(m[1] || '1', 10) || 1; break
          case 'C': this.col += parseInt(m[1] || '1', 10) || 1; break
          case 'D': this.col = Math.max(0, this.col - (parseInt(m[1] || '1', 10) || 1)); break
          case 'K': { // erase in line (0 = to end, 2 = whole line)
            const line = this.cell(this.row, 0)
            if (n === 2) line.length = 0
            else line.length = Math.min(line.length, this.col)
            break
          }
          case 'J': { // erase in display (0 = cursor to end)
            const line = this.cell(this.row, 0)
            line.length = Math.min(line.length, this.col)
            this.rows.length = this.row + 1
            break
          }
          default: break
        }
        i += m[0].length - 1
        continue
      }
      if (ch === '\r') { this.col = 0; continue }
      if (ch === '\n') { this.row++; this.col = 0; continue }
      if (ch === '\b') { this.col = Math.max(0, this.col - 1); continue } // move left, erase nothing
      // 宽字符占两格,和真实终端一致。模型若按一格算,就永远复现不出"按字符数
      // 算列宽"这类错位 —— 测试会跟着被测代码一起错。
      const w = isWideChar(ch.codePointAt(0) || 0) ? 2 : 1
      this.cell(this.row, this.col)[this.col] = ch
      if (w === 2) this.cell(this.row, this.col + 1)[this.col + 1] = ''
      this.col += w
      if (this.col >= this.cols) { this.row++; this.col = 0 }
    }
  }

  /** Non-blank screen lines, trailing spaces trimmed. */
  lines(): string[] {
    return this.rows.map((r) => r.join('').replace(/\s+$/, '')).filter((l) => l !== '')
  }
  /** Everything currently displayed, wrapping removed. */
  text(): string {
    return this.rows.map((r) => r.join('')).join('').replace(/\s+$/, '')
  }
}

function editorOn(cols: number) {
  const screen = new Screen(cols)
  let handler: (d: string) => void = () => {}
  const term = {
    cols,
    onData(cb: (d: string) => void) { handler = cb },
    write(s: string) { screen.write(s) },
    clear() { screen.rows = [[]]; screen.row = 0; screen.col = 0 },
  }
  const editor = new LineEditor(term as any, {
    prompt: () => '> ',
    promptLen: () => 2,
    contPrompt: () => '. ',
    contPromptLen: () => 2,
    onSubmit: () => {},
    // 与生产一致:终端是接了语法高亮的。高亮会让更多按键走 redraw 而不是快捷路径,
    // 而这类缺陷正是在 redraw 上暴露的,不接就测不到。
    highlight: highlightSqlAnsi,
  })
  editor.start()
  return { screen, editor, type: (d: string) => handler(d) }
}

// The reported symptom: holding backspace on a long statement filled the terminal
// with dozens of near-identical copies of the line. redraw() erased only the
// CURRENT row, but a buffer wider than the terminal occupies several rows and
// leaves the cursor on the last one — so each keystroke cleared that row, wrote
// the whole prompt+buffer again, wrapped again, and pushed a fresh copy downward.
test('editing a line wider than the terminal leaves exactly one copy on screen', () => {
  const { screen, type } = editorOn(40)
  const sql = 'SELECT id, name, email, created_at FROM users WHERE tenant = 42'
  type(sql)

  // Precondition: this really does wrap.
  expect(sql.length + 2).toBeGreaterThan(40)

  for (let i = 0; i < 10; i++) type('\x7f') // ten backspaces

  const expected = '> ' + sql.slice(0, sql.length - 10)
  expect(screen.text()).toBe(expected)
  // And no stale duplicate of the prompt is left behind.
  expect(screen.text().split('> ').length - 1).toBe(1)
})

test('the cursor ends up where the text does after wrapped edits', () => {
  const { screen, type } = editorOn(40)
  const sql = 'SELECT a, b, c, d, e, f, g FROM some_table WHERE x = 1'
  type(sql)
  type('\x7f')

  const expected = '> ' + sql.slice(0, -1)
  const pos = expected.length
  expect(screen.row).toBe(Math.floor(pos / 40))
  expect(screen.col).toBe(pos % 40)
})

// Editing in the middle of a wrapped line must also replace, not append.
test('inserting into the middle of a wrapped line replaces the old render', () => {
  const { screen, type } = editorOn(30)
  type('SELECT * FROM a_fairly_long_table_name')
  for (let i = 0; i < 5; i++) type('\x1b[D') // move left five
  type('XYZ')

  const body = 'SELECT * FROM a_fairly_long_tableXYZ_name'
  expect(screen.text()).toBe('> ' + body)
})

// A short line must keep working exactly as before.
test('a line that fits on one row is unaffected', () => {
  const { screen, type } = editorOn(80)
  type('SELECT 1')
  type('\x7f')
  // text() trims trailing blanks, so the removed character just shortens it.
  expect(screen.text()).toBe('> SELECT')
})

// Out-of-band output (a notice, a script result) is printed ABOVE the input line
// while the user is mid-typing. It cleared only the current row, so with a
// wrapped line the earlier rows stayed on screen and the buffer appeared twice.
test('printing above a wrapped line does not leave the old rows behind', () => {
  const { screen, editor, type } = editorOn(40)
  const sql = 'SELECT id, name FROM users WHERE tenant = 42 AND active'
  type(sql)

  editor.printAbove(['· notice line'])

  const text = screen.text()
  expect(text).toContain('· notice line')
  // Exactly one prompt, and the buffer appears once.
  expect(text.split('> ').length - 1).toBe(1)
  expect(text.split(sql).length - 1).toBe(1)
})

// Ctrl+L clears the screen and repaints the input line; it had the same
// single-row assumption in its repaint.
test('Ctrl+L repaints a wrapped line exactly once', () => {
  const { screen, type } = editorOn(40)
  const sql = 'SELECT id, name FROM users WHERE tenant = 42 AND active'
  type(sql)
  type('\x0c') // Ctrl+L
  type('\x7f') // then edit, which must still replace cleanly

  const expected = '> ' + sql.slice(0, -1)
  expect(screen.text()).toBe(expected)
})

// Typing must not repaint the whole line. The wrapped-line fix made every
// keystroke walk to the top of the block, erase to the end of the display and
// rewrite prompt+buffer — correct, but on a long statement that is a full
// repaint per character, which the terminal shows as flicker.
//
// Appending at the end of a line is the overwhelmingly common edit and needs no
// repaint at all: the character can simply be emitted. The cost of one keystroke
// must therefore not grow with what is already typed.
test('appending a character does not repaint the whole line', () => {
  const screen = new Screen(120)
  let handler: (d: string) => void = () => {}
  let written = 0
  const term = {
    cols: 120,
    onData(cb: (d: string) => void) { handler = cb },
    write(s: string) { written += s.length; screen.write(s) },
    clear() {},
  }
  const editor = new LineEditor(term as any, {
    prompt: () => '> ', promptLen: () => 2,
    contPrompt: () => '. ', contPromptLen: () => 2,
    onSubmit: () => {},
  })
  editor.start()

  const long = 'SELECT id, name, email FROM users WHERE tenant = 42 AND active = 1'
  handler(long)

  written = 0
  handler('X') // one more character
  expect(written, `one keystroke wrote ${written} chars for a ${long.length}-char line`)
    .toBeLessThan(20)

  // …and the screen is still right.
  expect(screen.text()).toBe('> ' + long + 'X')
})

// 上翻历史命令时输入行错乱:一条 40 个字符的中文语句占 58 列,而重绘按字符个数算
// 列宽,于是它以为这行没折行,重绘时少上移一行 —— 旧内容的第一行连同提示符一起留
// 在屏幕上,看起来像凭空多出一个命令行。
//
// 换成显示宽度就对了。这条用例锁住的是"换掉一行更宽的内容之后,屏幕上只剩一份"。
test('recalling a wrapped CJK command then a shorter one leaves one line', () => {
  const { screen, editor, type } = editorOn(40)
  const cjk = "SELECT '华东仓补货订单明细汇总' AS 名称;"
  const short = 'SELECT 1;'

  type(cjk + '\r'); editor.resume()
  type(short + '\r'); editor.resume()

  type('\x1b[A') // 上翻:short
  type('\x1b[A') // 再上翻:中文那条(它会折行)
  type('\x1b[B') // 下翻回 short

  // 已提交的命令会留在屏幕上,那是正常回显,所以不能按提示符个数判断。
  // 真正的判据是那条中文命令出现了几次:回显一次是对的,再多出来的就是没被擦掉的
  // 残留 —— 修复前它正好多出一行(折行的第一行连着提示符一起留下)。
  const cjkLines = screen.lines().filter((l) => l.includes('华东仓补货'))
  expect(cjkLines.length).toBe(1)
  // 当前输入行就是 short 本身,没有前一条的尾巴。
  expect(screen.lines().pop()).toBe('> ' + short)
})

// 同一根因的另一面:宽字符的退格要退两格、擦两格,只擦一格会留下半个汉字。
test('backspacing a wide character clears both of its cells', () => {
  const { screen, type } = editorOn(40)
  type('ab中')
  type('\x7f')
  expect(screen.text()).toBe('> ab')
})

// 提交一条折行的语句之后,它的回显被下一行输入吃掉。
//
// renderedRow 记的是"上一次渲染时光标停在输入块的第几行",redraw 靠它先上移再擦。
// 但提交语句时光标已经通过 \r\n 走到全新的一行,输入块结束了 —— 这个数却没有清零。
// 于是下一次 redraw 会带着上一条命令的行数往上移,擦掉的是**别人的**内容:上一条
// 命令的回显、它的结果,都在那几行里。
//
// 触发条件是"上一条输入折过行",所以长语句、多行语句、含中文的语句都会中招,而短的
// 英文语句不会 —— 正是它一直没被发现的原因。上翻历史命令时最明显,因为召回必然重绘。
test('submitting a wrapped line does not let the next redraw eat its echo', () => {
  const { screen, editor, type } = editorOn(40)
  const long = 'SELECT id, name, email, status, amount FROM orders WHERE tenant = 42 AND status IN (1,2,3) ORDER BY id DESC;'
  expect(long.length + 2).toBeGreaterThan(40 * 2) // 确实折了不止一行

  type(long + '\r')
  const echoed = screen.lines()
  editor.resume()

  // 新提示符上随便敲点会触发重绘的东西(带高亮时一个空格就够)。
  type('SELECT 1;')

  // 上一条命令的回显必须原样还在。
  for (const l of echoed) expect(screen.lines()).toContain(l)
})
