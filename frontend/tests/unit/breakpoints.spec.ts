import { test, expect } from '@playwright/test'
import { BP_MID, BP_NARROW, breakpointOf } from '../../src/lib/breakpoints'

// 边界取闭区间:768 自己算 narrow,不是 mid。平板竖屏正好是 768,
// 把它判成 mid 等于这套改动对最典型的目标设备不生效。
test('768 及以下是 narrow,769 起是 mid', () => {
  expect(breakpointOf(320)).toBe('narrow')
  expect(breakpointOf(BP_NARROW)).toBe('narrow')
  expect(breakpointOf(BP_NARROW + 1)).toBe('mid')
})

test('1080 及以下是 mid,1081 起是 wide', () => {
  expect(breakpointOf(BP_MID)).toBe('mid')
  expect(breakpointOf(BP_MID + 1)).toBe('wide')
  expect(breakpointOf(1920)).toBe('wide')
})

// 视口宽度不会是负数或 NaN,但 `useBreakpoint` 在挂载前读到的可能是 0。
// 0 判成 narrow 而不是抛错 —— 首帧渲染一张只有主干列的表,比白屏好。
test('0 与非法值退回 narrow,不抛错', () => {
  expect(breakpointOf(0)).toBe('narrow')
  expect(breakpointOf(Number.NaN)).toBe('narrow')
})
