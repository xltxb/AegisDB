import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'
import type { Completion } from '../../src/lib/sqlComplete'

// LineEditor 的 Tab:单独按一下 Tab 才补全;唯一候选补全,多个候选补到公共前缀、第二次 Tab 列出;
// 结果回来时输入已经变了就不应用;粘贴进来的 Tab 是空白,不触发补全。

function fakeTerm() {
  let handler: (d: string) => void = () => {}
  return {
    cols: 80,
    written: [] as string[],
    onData(cb: (d: string) => void) { handler = cb },
    write(s: string) { this.written.push(s) },
    clear() {},
    type(d: string) { handler(d) },
  }
}

type Completer = (stmt: string, cursor: number) => Promise<Completion | null>

function newEditor(complete: Completer) {
  const term = fakeTerm()
  const submitted: string[] = []
  const editor = new LineEditor(term as any, {
    prompt: () => '> ', promptLen: () => 2, contPrompt: () => '. ', contPromptLen: () => 2,
    onSubmit: (stmt) => submitted.push(stmt),
    complete,
  })
  editor.start()
  return { term, editor, submitted }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

test('唯一候选:补全后接着敲、提交的是补全后的语句', async () => {
  const calls: [string, number][] = []
  const { term, submitted } = newEditor(async (stmt, cursor) => {
    calls.push([stmt, cursor])
    return { replace: 3, text: 'orders', candidates: ['orders'] }
  })
  term.type('SELECT * FROM ord')
  term.type('\t')
  await tick()
  term.type(';\r')
  expect(calls).toEqual([['SELECT * FROM ord', 17]])
  expect(submitted).toEqual(['SELECT * FROM orders;'])
})

test('多个候选:第一次补到公共前缀,第二次 Tab 把候选列出来', async () => {
  let n = 0
  const { term, submitted } = newEditor(async (stmt) => {
    n++
    return stmt.endsWith('ord')
      ? { replace: 3, text: 'order', candidates: ['order_items', 'orders'] }
      : { replace: 5, text: 'order', candidates: ['order_items', 'orders'] }
  })
  term.type('SELECT * FROM ord')
  term.type('\t')
  await tick()
  const before = term.written.join('')
  expect(before).not.toContain('order_items')
  term.type('\t') // 前缀没再变长:这一次列出候选
  await tick()
  const listed = term.written.join('').slice(before.length)
  expect(listed).toContain('order_items')
  expect(listed).toContain('orders')
  term.type('s;\r')
  expect(n).toBe(2)
  expect(submitted).toEqual(['SELECT * FROM orders;'])
})

test('多行语句:补全拿到的是整条语句和光标在其中的位置', async () => {
  const calls: [string, number][] = []
  const { term } = newEditor(async (stmt, cursor) => { calls.push([stmt, cursor]); return null })
  term.type('SELECT *\r')
  term.type('FROM ord')
  term.type('\t')
  await tick()
  expect(calls).toEqual([['SELECT *\nFROM ord', 'SELECT *\nFROM ord'.length]])
})

test('结果回来时输入已经变了:不应用', async () => {
  let resolve!: (c: Completion) => void
  const { term, submitted } = newEditor(() => new Promise((r) => { resolve = r }))
  term.type('SELECT * FROM ord')
  term.type('\t')
  term.type('x') // 人没等,接着敲了
  resolve({ replace: 3, text: 'orders', candidates: ['orders'] })
  await tick()
  term.type(';\r')
  expect(submitted).toEqual(['SELECT * FROM ordx;'])
})

test('粘贴进来的 Tab 是空白,不触发补全,也不把两个词粘在一起', async () => {
  let called = false
  const { term, submitted } = newEditor(async () => { called = true; return null })
  term.type('SELECT\tid\tFROM t;\r')
  await tick()
  expect(called).toBe(false)
  expect(submitted).toEqual(['SELECT id FROM t;'])
})
