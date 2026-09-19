import { test, expect } from '@playwright/test'

import { clipWidth, dispWidth } from '../../src/lib/textWidth'

// clipWidth 存在的理由和 dispWidth 一样:按 slice(0, n) 截中文,截出来的**列数**是
// 字数的两倍。调用方(终端提示符)拿这个串去算光标位置,截错一格,光标就偏一格,
// 而且是每画一次偏一次。

test('截到上限以内,按列数算不是按字数算', () => {
  expect(dispWidth(clipWidth('abcdefghij', 4))).toBeLessThanOrEqual(4)
  expect(clipWidth('abcdefghij', 4)).toBe('abcd')
  // 中文一个字两格:4 格只装得下两个字
  expect(clipWidth('数据库网关', 4)).toBe('数据')
  expect(dispWidth(clipWidth('数据库网关', 4))).toBe(4)
})

test('宽字符跨过边界时宁可少一格,也不吐出半个字符', () => {
  // 5 格装两个中文(4 格)之后还剩 1 格,第三个字要两格 —— 不切开它
  const out = clipWidth('数据库网关', 5)
  expect(out).toBe('数据')
  expect(dispWidth(out)).toBe(4)
})

test('装得下就原样返回', () => {
  expect(clipWidth('orders', 28)).toBe('orders')
  expect(clipWidth('', 8)).toBe('')
})

test('上限为 0 或负数时返回空串,不是半个字符', () => {
  expect(clipWidth('数据库', 0)).toBe('')
  expect(clipWidth('abc', 0)).toBe('')
  expect(clipWidth('abc', -3)).toBe('')
})

test('基本平面之外的字符不会被切成孤立代理', () => {
  // emoji 占两格、两个 UTF-16 单元;1 格装不下,且不能留半个代理
  const out = clipWidth('🙂ab', 1)
  expect(out).toBe('')
  expect([...clipWidth('🙂ab', 2)]).toEqual(['🙂'])
})
