import { test, expect } from '@playwright/test'

import { flattenStatement } from '../../src/lib/sqlFlatten'

// 用户报的原始故障:一条带 -- 注释的多行 Oracle 查询,首次执行正常,上翻再执行报
// ORA-00936: missing expression。因为历史记录把换行压成空格,而换行正是 -- 注释的
// 终止符 —— 压平之后注释吃掉了它后面的整条语句,服务端只收到半截 `... NOT IN (`。
test('line comment inside a multi-line statement does not swallow the rest', () => {
  const sql = [
    'SELECT u.username FROM dba_users u',
    'WHERE u.username NOT IN (',
    '  -- 系统自带/官方工具',
    "  'SYS', 'SYSTEM'",
    ')',
    'ORDER BY u.username;',
  ].join('\n')

  const flat = flattenStatement(sql)

  expect(flat).not.toContain('--')
  expect(flat).toContain('/* 系统自带/官方工具 */')
  // 注释之后的内容必须还在语句里,而不是被注释掉。
  expect(flat).toContain("'SYS', 'SYSTEM'")
  expect(flat).toContain('ORDER BY u.username;')
  expect(flat.includes('\n')).toBe(false)
})

// 字符串字面量里的 -- 不是注释,不能动它 —— 改写了就改变了查询的值。
test('a double dash inside a string literal is left alone', () => {
  expect(flattenStatement("SELECT 'a -- b' AS s;")).toBe("SELECT 'a -- b' AS s;")
  expect(flattenStatement('SELECT "col -- x" FROM t;')).toBe('SELECT "col -- x" FROM t;')
  // 重复引号是转义,不是字符串结束。
  expect(flattenStatement("SELECT 'it''s -- fine' FROM t;")).toBe("SELECT 'it''s -- fine' FROM t;")
})

// 已有的块注释原样保留;里面的 -- 不是行注释的开头。
test('an existing block comment passes through untouched', () => {
  expect(flattenStatement('SELECT /* keep -- this */ 1;')).toBe('SELECT /* keep -- this */ 1;')
})

// 注释正文里的 */ 会提前把块注释关掉,后面的文字就漏成了 SQL。
test('a comment body containing the block terminator is defused', () => {
  const flat = flattenStatement('SELECT 1 -- a */ b\nFROM t;')
  expect(flat).toBe('SELECT 1 /* a * / b */ FROM t;')
})

// 没有注释时就是老老实实把换行压成空格。
test('plain multi-line SQL just collapses', () => {
  expect(flattenStatement('SELECT 1\nFROM t\nWHERE x = 2;')).toBe('SELECT 1 FROM t WHERE x = 2;')
})
