import { test, expect } from '@playwright/test'

import { extractTables, tablesLabel } from '../../src/lib/sqlTables'

// The list shows WHICH TABLES a command touches instead of the command itself:
// the command can be a whole script, while "which tables" is the thing a reviewer
// scans a queue for.
test('finds the table of each ordinary statement shape', () => {
  const cases: Array<[string, string[]]> = [
    ['SELECT * FROM orders', ['orders']],
    ['select id from public.orders where x=1', ['public.orders']],
    ['INSERT INTO orders (a) VALUES (1)', ['orders']],
    ['UPDATE orders SET a=1 WHERE id=2', ['orders']],
    ['DELETE FROM orders WHERE id=2', ['orders']],
    ['DROP TABLE orders', ['orders']],
    ['TRUNCATE TABLE sessions', ['sessions']],
    ['ALTER TABLE orders ADD COLUMN x INT', ['orders']],
    ['CREATE TABLE new_orders (id INT)', ['new_orders']],
    ['RENAME TABLE a TO b', ['a', 'b']],
  ]
  for (const [sql, want] of cases) {
    expect(extractTables(sql), sql).toEqual(want)
  }
})

test('collects every table in a join, in order, without repeats', () => {
  expect(extractTables('SELECT * FROM orders o JOIN users u ON o.uid=u.id LEFT JOIN items i ON i.oid=o.id'))
    .toEqual(['orders', 'users', 'items'])
  expect(extractTables('SELECT * FROM orders JOIN orders o2 ON 1=1')).toEqual(['orders'])
})

test('reads every statement of a batch', () => {
  expect(extractTables('SELECT * FROM a; DELETE FROM b WHERE id=1')).toEqual(['a', 'b'])
})

// Quoted identifiers are normal for reserved words; the quotes are not part of
// the name a reviewer recognises.
test('unwraps quoted identifiers', () => {
  expect(extractTables('SELECT * FROM `order`')).toEqual(['order'])
  expect(extractTables('SELECT * FROM "order"')).toEqual(['order'])
  expect(extractTables('SELECT * FROM [order]')).toEqual(['order'])
})

// A table name lifted out of a comment or a string literal would be wrong, and
// keywords must never be mistaken for names.
test('ignores comments, string literals and keywords', () => {
  expect(extractTables("SELECT * FROM orders -- FROM secret_table")).toEqual(['orders'])
  expect(extractTables("SELECT * FROM orders /* FROM other */")).toEqual(['orders'])
  expect(extractTables("SELECT * FROM orders WHERE note='FROM fake'")).toEqual(['orders'])
  expect(extractTables('SELECT * FROM (SELECT 1)')).toEqual([])
})

// MongoDB commands name a collection rather than a table.
test('reads the collection out of a MongoDB command', () => {
  expect(extractTables('db.orders.find({a:1})')).toEqual(['orders'])
  expect(extractTables('db.getCollection("orders").drop()')).toEqual(['orders'])
  expect(extractTables('db.dropDatabase()')).toEqual([])
})

test('returns nothing rather than guessing when it cannot tell', () => {
  expect(extractTables('')).toEqual([])
  expect(extractTables('SHOW DATABASES')).toEqual([])
  expect(extractTables('\\dt')).toEqual([])
})

// The list cell has finite room: show a few and say how many more.
test('labels a long list compactly', () => {
  expect(tablesLabel(['a', 'b'])).toBe('a, b')
  expect(tablesLabel(['a', 'b', 'c', 'd', 'e'], 3)).toBe('a, b, c +2')
  expect(tablesLabel([])).toBe('—')
})
