import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'

// A terminal stand-in: LineEditor only ever calls onData/write/clear on it.
function fakeTerm() {
  let handler: (d: string) => void = () => {}
  return {
    written: [] as string[],
    onData(cb: (d: string) => void) {
      handler = cb
    },
    write(s: string) {
      this.written.push(s)
    },
    clear() {},
    /** deliver keystrokes / a paste exactly as xterm would */
    type(d: string) {
      handler(d)
    },
  }
}

function newEditor() {
  const term = fakeTerm()
  const submitted: string[] = []
  const editor = new LineEditor(term as any, {
    prompt: () => '> ',
    promptLen: () => 2,
    contPrompt: () => '. ',
    contPromptLen: () => 2,
    onSubmit: (stmt) => submitted.push(stmt.trim()),
  })
  editor.start()
  return { term, editor, submitted }
}

test('a pasted batch runs its statements one after another', () => {
  const { term, editor, submitted } = newEditor()
  term.type('SELECT 1;\nSELECT 2;\nSELECT 3;\n')

  expect(submitted).toEqual(['SELECT 1;'])
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;', 'SELECT 2;'])
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;', 'SELECT 2;', 'SELECT 3;'])
})

// EF4: when a pasted batch hits an approval prompt, an MFA prompt, or a hard
// denial, the user is shown a dialog for THAT statement. Choosing "cancel" means
// "stop this batch" — but resume() unconditionally replayed the stashed
// remainder, so the terminal printed "cancelled, not executed" and then
// immediately ran the next statements. Someone who pasted a migration, saw a
// DROP in the approval dialog and cancelled would still have the rest execute.
test('cancelling a statement abandons the rest of the pasted batch', () => {
  const { term, editor, submitted } = newEditor()
  term.type('SELECT 1;\nDROP TABLE orders;\nSELECT 3;\n')

  expect(submitted).toEqual(['SELECT 1;'])

  // The user cancels at the prompt raised for the statement in flight.
  editor.discardQueued()
  editor.resume()

  expect(submitted).toEqual(['SELECT 1;'])

  // The editor is usable afterwards, and a fresh statement still runs.
  term.type('SELECT 9;\n')
  expect(submitted).toEqual(['SELECT 1;', 'SELECT 9;'])
})

// Ctrl+C already dropped the batch; that behaviour must not regress.
test('Ctrl+C abandons the rest of the pasted batch', () => {
  const { term, editor, submitted } = newEditor()
  term.type('SELECT 1;\nSELECT 2;\n')
  expect(submitted).toEqual(['SELECT 1;'])

  term.type('\x03') // Ctrl+C while busy
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;'])
})

// 多行语句的历史记录。两件事一起坏过:
//
//  1. 每敲一次回车就记一条,于是一条六行的语句在历史里留下六个越来越长的前缀,
//     上翻要在半截 SQL 里一路翻过去。
//  2. 记进去时把换行压成空格,而换行是 `--` 注释的终止符 —— 注释于是吃掉了它后面
//     的整条语句。首次执行是好的(那时换行还在),上翻再执行就只剩半截,Oracle 报
//     ORA-00936: missing expression。
test('a multi-line statement is remembered once, and stays executable', () => {
  const submitted: string[] = []
  let handler: (d: string) => void = () => {}
  const term = { cols: 100, onData(cb: (d: string) => void) { handler = cb }, write() {}, clear() {} }
  const editor = new LineEditor(term as any, {
    prompt: () => '> ', promptLen: () => 2, contPrompt: () => '. ', contPromptLen: () => 2,
    onSubmit: (s: string) => submitted.push(s),
  })
  editor.start()

  for (const l of [
    'SELECT u.username FROM dba_users u',
    'WHERE u.username NOT IN (',
    '  -- 系统自带/官方工具',
    "  'SYS', 'SYSTEM'",
    ')',
    'ORDER BY u.username;',
  ]) handler(l + '\r')
  editor.resume()

  // 六行只留下一条历史,不是六个前缀。
  expect((editor as any).history).toHaveLength(1)

  // 上翻并回车:送出去的必须还是一条完整可执行的语句。
  handler('\x1b[A')
  handler('\r')
  const replayed = submitted[1]
  expect(replayed).toContain("'SYS', 'SYSTEM'")
  expect(replayed).toContain('ORDER BY u.username;')
  expect(replayed).not.toContain('--')
})
