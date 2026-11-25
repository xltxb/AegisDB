import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'

// ask():SQL*Plus 替换变量的 Enter value for x: 与 ACCEPT(sqlplus-compat 第一批)。

function setup() {
  let handler: (d: string) => void = () => {}
  const term = {
    written: [] as string[],
    onData(cb: (d: string) => void) { handler = cb },
    write(s: string) { this.written.push(s) },
    clear() {},
    type(d: string) { handler(d) },
  }
  const submitted: string[] = []
  const editor = new LineEditor(term as any, {
    prompt: () => '> ', promptLen: () => 2, contPrompt: () => '. ', contPromptLen: () => 2,
    onSubmit: (s) => submitted.push(s.trim()),
  })
  editor.start()
  return { term, editor, submitted }
}

test('问一行:回显、退格、回车给出回答,不当成语句提交', async () => {
  const { term, editor, submitted } = setup()
  term.type('select &x from dual;\r')
  expect(submitted).toEqual(['select &x from dual;'])
  const p = editor.ask('Enter value for x: ')
  term.type('4')
  term.type('3')
  term.type('\x7f')
  term.type('2\r')
  expect(await p).toBe('42')
  expect(term.written.join('')).toContain('Enter value for x: ')
  expect(term.written.join('')).toContain('43\b \b2')
  expect(submitted).toEqual(['select &x from dual;'])
})

test('HIDE 不回显;Ctrl+C 得到 null', async () => {
  const { term, editor } = setup()
  term.type('accept p hide\r')
  const before = term.written.length
  const p = editor.ask('pwd: ', true)
  term.type('s3cret\r')
  expect(await p).toBe('s3cret')
  expect(term.written.slice(before).join('')).not.toContain('s3cret')

  const q = editor.ask('x: ')
  term.type('ab\x03')
  expect(await q).toBeNull()
})

test('回答之后同一段粘贴里剩下的语句排进队列,resume 之后照常提交', async () => {
  const { term, editor, submitted } = setup()
  term.type('select &x from dual;\r')
  const p = editor.ask('x: ')
  term.type('1\rselect 2 from dual;\r')
  expect(await p).toBe('1')
  editor.resume()
  expect(submitted).toEqual(['select &x from dual;', 'select 2 from dual;'])
})
