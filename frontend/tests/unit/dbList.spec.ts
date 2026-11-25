import { test, expect } from '@playwright/test'

import { parseDbList } from '../../src/lib/dbList'

test('parseDbList 按逗号 / 空白分隔,库名里的 s 原样保留', () => {
  expect(parseDbList('orders')).toEqual(['orders'])
  expect(parseDbList('sales_db, orders，users\nstats  sys')).toEqual(['sales_db', 'orders', 'users', 'stats', 'sys'])
  expect(parseDbList('a, a ,b')).toEqual(['a', 'b'])
  expect(parseDbList('  ')).toEqual([])
})
