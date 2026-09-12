import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'

// 光标与删除要按**字符**走,不是按 UTF-16 单元。
//
// emoji、生僻字这些基本平面之外的字符,在 JS 字符串里占两个 UTF-16 单元(一对代理)。
// 按单元挪光标,一次 ← 就停在了这一对的正中间:再插入的字符插进了半个字符里,退格
// 删掉的是半个字符 —— 缓冲里于是留下一个孤立代理,它既显示不出来,又会随语句一起
// 发到目标库。列宽也跟着错:dispWidth 按**码点**算(一个 emoji 两格),而切开之后
// 那半个代理只算一格,光标位置从此一路漂移。
//
// dispWidth 一直是按码点算的 —— 这里把光标也统一到码点上,两边不再分叉。

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

/** 缓冲里不该出现落单的代理 —— 它显示不出来,却会被提交到目标库。 */
function hasLoneSurrogate(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c >= 0xd800 && c <= 0xdbff) {
      const next = s.charCodeAt(i + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true
      i++
    } else if (c >= 0xdc00 && c <= 0xdfff) {
      return true
    }
  }
  return false
}

test('退格删掉的是整个 emoji,不是半个代理对', () => {
  const { term, submitted } = newEditor()
  term.type('SELECT 😀')
  term.type('\x7f') // Backspace
  term.type('1;\n')

  expect(submitted).toEqual(['SELECT 1;'])
  expect(hasLoneSurrogate(submitted[0])).toBe(false)
})

test('← 一次跨过整个 emoji', () => {
  const { term, submitted } = newEditor()
  term.type('😀X')
  term.type('\x1b[D') // ← 停在 X 前
  term.type('\x1b[D') // ← 再一次:停在 emoji 前,不是它中间
  term.type('Z')
  term.type('\x1b[F')  // 行尾 —— 语句要有分号才算写完
  term.type(';\n')

  expect(submitted).toEqual(['Z😀X;'])
  expect(hasLoneSurrogate(submitted[0])).toBe(false)
})

test('→ 一次也跨过整个 emoji', () => {
  const { term, submitted } = newEditor()
  term.type('😀X')
  term.type('\x1b[H')  // 回到行首
  term.type('\x1b[C')  // → 一次:跨过整个 emoji
  term.type('Z')
  term.type('\x1b[F')
  term.type(';\n')

  expect(submitted).toEqual(['😀ZX;'])
  expect(hasLoneSurrogate(submitted[0])).toBe(false)
})

test('Delete 删掉的是整个 emoji', () => {
  const { term, submitted } = newEditor()
  term.type('A😀B')
  term.type('\x1b[H')   // 行首
  term.type('\x1b[C')   // 跨过 A
  term.type('\x1b[3~')  // Delete:整个 emoji
  term.type('\x1b[F')
  term.type(';\n')

  expect(submitted).toEqual(['AB;'])
  expect(hasLoneSurrogate(submitted[0])).toBe(false)
})
