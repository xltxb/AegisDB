import { test, expect } from '@playwright/test'

import { markerUid } from '../../src/lib/session'

// 2026-11 白盒审计 V-16:localStorage 里只有会话标记 "<uid>.<csrf>",不是令牌。
test('会话标记里读得出用户 id,认不出就是 0', () => {
  expect(markerUid('42.abcdef')).toBe(42)
  expect(markerUid('')).toBe(0)
  expect(markerUid('not-a-marker')).toBe(0)
  // 升级前的 JWT 不算标记:第一段不是数字
  expect(markerUid('eyJhbGciOiJI.eyJ1aWQiOjF9.sig')).toBe(0)
})
