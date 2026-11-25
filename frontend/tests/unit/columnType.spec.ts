import { test, expect } from '@playwright/test'

import { inferColumnType } from '../../src/lib/columnType'

test('按返回值推断列类型,空值不参与', () => {
  const rows = [
    ['1', '2026-10-21 09:30:00', 'true', '张三', ''],
    ['-2.5', '2026-10-22', 'FALSE', '李四', ''],
    ['', '', '', '', ''],
  ]
  expect(inferColumnType(rows, 0)).toBe('num')
  expect(inferColumnType(rows, 1)).toBe('date')
  expect(inferColumnType(rows, 2)).toBe('bool')
  expect(inferColumnType(rows, 3)).toBe('text')
  expect(inferColumnType(rows, 4)).toBe('text') // 全空:不猜
  expect(inferColumnType([['12'], ['abc']], 0)).toBe('text')
})
