import { test, expect } from '@playwright/test'
import { isCopyShortcut, type CopyKeyEvent } from '../../src/lib/copyShortcut'

// Ctrl+C 在终端里是「复制」还是「中断」。
//
// 原先无条件当中断,于是想复制一段 SQL 的人会发现自己**正在打的那一行被清掉了** ——
// 要执行的东西没了,屏幕上只留下一个 ^C。几乎所有终端的约定是同一条:选中了东西
// 就是复制,没选中才是中断。
//
// 这组用例钉的是那条规则的边界。判错的两个方向不对称:
//   - 该复制却中断了 = 用户丢掉了正在写的语句(就是这次要修的);
//   - 该中断却复制了 = 用户按第二下就好(选区已被清掉)。
// 所以边界要往"只有明确是复制时才复制"上收。

const ev = (o: Partial<CopyKeyEvent> = {}): CopyKeyEvent => ({
  key: 'c', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...o,
})

test('有选区时 Ctrl+C 是复制', () => {
  expect(isCopyShortcut(ev({ ctrlKey: true }), true)).toBe(true)
})

// 这是回归的落脚点:没有选区就必须还是中断,否则终端里再也按不出中断了。
test('没有选区时 Ctrl+C 仍然是中断', () => {
  expect(isCopyShortcut(ev({ ctrlKey: true }), false)).toBe(false)
})

test('Cmd+C 一并算作复制', () => {
  expect(isCopyShortcut(ev({ metaKey: true }), true)).toBe(true)
})

// Ctrl+Shift+C / Ctrl+Alt+C 在不少终端里另有含义,不该被这条规则吃掉。
test('带 Shift 或 Alt 时不算', () => {
  expect(isCopyShortcut(ev({ ctrlKey: true, shiftKey: true }), true)).toBe(false)
  expect(isCopyShortcut(ev({ ctrlKey: true, altKey: true }), true)).toBe(false)
})

// 开着 CapsLock 的人按下的是 'C'。
test('大写 C 也算', () => {
  expect(isCopyShortcut(ev({ key: 'C', ctrlKey: true }), true)).toBe(true)
})

test('别的键不算', () => {
  for (const key of ['v', 'x', 'a', 'Control', 'l']) {
    expect(isCopyShortcut(ev({ key, ctrlKey: true }), true), key).toBe(false)
  }
})

// 光按 c 不带任何修饰键 —— 那是在往命令行里打字,不能被当成复制吞掉。
test('不带修饰键的 c 是普通输入', () => {
  expect(isCopyShortcut(ev(), true)).toBe(false)
})
