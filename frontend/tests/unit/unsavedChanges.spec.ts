import { test, expect } from '@playwright/test'
import { ref, nextTick } from 'vue'
import { hasUnsavedChanges, setUnsaved, clearUnsaved, useUnsavedFlag, sameForm } from '../../src/lib/unsavedChanges'

// 管理后台空闲锁屏问的那一句"有没有没保存的改动"(2026-10-26 回归排查 10)。登记错了的后果:
// 漏登 → 改到一半被弹回总览、内容丢失(原来的 bug);多登 / 不注销 → 以后每次空闲都锁屏而不是回总览。

test.beforeEach(() => clearUnsaved())

test('按 id 登记与注销,多个来源互不覆盖', () => {
  const a = Symbol('a')
  const b = Symbol('b')
  expect(hasUnsavedChanges()).toBe(false)
  setUnsaved(a, true)
  setUnsaved(b, true)
  setUnsaved(a, false)
  expect(hasUnsavedChanges()).toBe(true) // b 还脏着
  setUnsaved(b, false)
  expect(hasUnsavedChanges()).toBe(false)
})

test('useUnsavedFlag 跟着来源变', async () => {
  const dirty = ref(false)
  useUnsavedFlag(() => dirty.value)
  expect(hasUnsavedChanges()).toBe(false)
  dirty.value = true
  await nextTick()
  expect(hasUnsavedChanges()).toBe(true)
  dirty.value = false
  await nextTick()
  expect(hasUnsavedChanges()).toBe(false)
})

test('clearUnsaved 清空全部(锁屏里放弃修改)', () => {
  setUnsaved(Symbol('x'), true)
  clearUnsaved()
  expect(hasUnsavedChanges()).toBe(false)
})

test('sameForm 按值比较', () => {
  expect(sameForm({ a: 1, b: 'x' }, { a: 1, b: 'x' })).toBe(true)
  expect(sameForm({ a: 1 }, { a: 2 })).toBe(false)
  expect(sameForm([1, 2], [1, 2])).toBe(true)
})
