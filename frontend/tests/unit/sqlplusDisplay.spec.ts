import { test, expect } from '@playwright/test'

import {
  formatNumber, parseColumnCmd, applyColumnCmd, newDisplayState, applyDisplay, renderPlain, plainLayout,
  applyDisplaySet, showDisplaySetting, showFeedback, tableWidth,
} from '../../src/lib/sqlplusDisplay'
import {
  setBuffer, bufferText, listBuffer, appendBuffer, changeBuffer, delBuffer, inputBuffer, saveText, getText, withSqlExt, emptyBuffer,
  ERR_EMPTY, ERR_NOT_FOUND, ERR_RANGE,
} from '../../src/lib/sqlplusBuffer'
import { parseSqlPlusCommand } from '../../src/lib/sqlplus'

// SQL*Plus 显示设置与行编辑器(sqlplus-compat 第三批)。

test('数字格式掩码', () => {
  expect(formatNumber('1234.5', '999,999.99')).toBe('1,234.50')
  expect(formatNumber('1234567', '9G999G999')).toBe('1,234,567')
  expect(formatNumber('0.5', '9.99')).toBe('.50')
  expect(formatNumber('0.5', '0.99')).toBe('0.50')
  expect(formatNumber('7', '000')).toBe('007')
  expect(formatNumber('-12.3', '99.9')).toBe('-12.3')
  expect(formatNumber('-12.3', '99.9MI')).toBe('12.3-')
  expect(formatNumber('12.3', 'S99.9')).toBe('+12.3')
  expect(formatNumber('12.345', '$99.99')).toBe('$12.35')
  expect(formatNumber('12345', '9.99EEEE')).toBe('1.23E+04')
  // 放不下:整个 #(与 SQL*Plus 一致,不悄悄截掉高位)
  expect(formatNumber('12345', '999')).toBe('####')
  // 不是数:原样
  expect(formatNumber('abc', '999')).toBe('abc')
})

test('COLUMN 命令解析与生效', () => {
  expect(parseColumnCmd('')).toEqual({ kind: 'list' })
  expect(parseColumnCmd('sal')).toEqual({ kind: 'show', col: 'SAL' })
  expect(parseColumnCmd("e.ename format a5 heading 'Emp|Name' null '-'")).toEqual({
    kind: 'set', col: 'ENAME', clear: false, fmt: { format: 'A5', heading: 'Emp Name', nullText: '-' },
  })
  expect(parseColumnCmd('sal for 99,999.99 justify right')).toEqual({ kind: 'set', col: 'SAL', clear: false, fmt: { format: '99,999.99' } })
  expect(parseColumnCmd('x noprint')).toEqual({ kind: 'set', col: 'X', clear: false, fmt: { noprint: true } })
  expect(parseColumnCmd('x format')).toEqual({ kind: 'error' })

  const st = newDisplayState()
  for (const a of ["ename format a5 heading 'Name'", 'sal format 99,999.99', 'secret noprint']) {
    const c = parseColumnCmd(a)
    if (c.kind === 'set') applyColumnCmd(st, c)
  }
  st.nullText = '(null)'
  const r = applyDisplay(['ENAME', 'SAL', 'SECRET', 'COMM'], [['SMITHSON', '800', 'x', ''], ['ALLEN', '1600.5', 'y', '300']], [false, true, false, true], st)
  expect(r.columns).toEqual(['Name', 'SAL', 'COMM'])
  expect(r.rows).toEqual([['SMIT…', '800.00', '(null)'], ['ALLEN', '1,600.50', '300']])
  expect(r.numeric).toEqual([false, true, true])
  // CLEAR
  const c = parseColumnCmd('sal clear')
  if (c.kind === 'set') applyColumnCmd(st, c)
  expect(st.columns.has('SAL')).toBe(false)
})

test('SET 的显示选项', () => {
  const st = newDisplayState()
  expect(applyDisplaySet('LINESIZE', '120', st)).toBe(true)
  expect(st.linesize).toBe(120)
  expect(tableWidth(st, 200)).toBe(120)
  expect(tableWidth(st, 80)).toBe(80)
  expect(applyDisplaySet('PAGES', '0', st)).toBe(true)
  expect(plainLayout(st)).toBe(true)
  expect(applyDisplaySet('FEED', 'off', st)).toBe(true)
  expect(showFeedback(st, 100)).toBe(false)
  expect(applyDisplaySet('FEEDBACK', '6', st)).toBe(true)
  expect(showFeedback(st, 5)).toBe(false)
  expect(showFeedback(st, 6)).toBe(true)
  expect(applyDisplaySet('NUMFORMAT', '999,999', st)).toBe(true)
  expect(applyDisplaySet('COLSEP', "','", st)).toBe(true)
  expect(st.colsep).toBe(',')
  expect(applyDisplaySet('NULL', "'<null>'", st)).toBe(true)
  expect(st.nullText).toBe('<null>')
  expect(applyDisplaySet('LINESIZE', 'wide', st)).toBe('bad')
  expect(applyDisplaySet('SERVEROUTPUT', 'ON', st)).toBe(false) // 不是显示选项,交给别处
  expect(showDisplaySetting('LINES', st)).toBe('linesize 120')
  expect(showDisplaySetting('FEEDBACK', st)).toBe('FEEDBACK ON for 6 or more rows')
  // 默认:跟着网关原来的样子(每次都打行数、用表格)
  const d = newDisplayState()
  expect(showFeedback(d, 1)).toBe(true)
  expect(plainLayout(d)).toBe(false)
})

test('纯文本排版:COLSEP、表头下划线、数字右对齐;HEADING OFF 不打表头', () => {
  const st = newDisplayState()
  st.colsep = ' | '
  const t = applyDisplay(['ID', 'NAME'], [['1', 'a'], ['10', 'bb']], [true, false], st)
  expect(renderPlain(t, st, 80)).toEqual(['ID | NAME', '-- | ----', ' 1 | a', '10 | bb'])
  st.heading = false
  expect(renderPlain(t, st, 80)).toEqual([' 1 | a', '10 | bb'])
})

test('行编辑器:LIST / APPEND / CHANGE / DEL / INPUT', () => {
  let b = setBuffer('select *\nfrom emp\nwhere 1=1')
  expect(b.cur).toBe(3)
  const l = listBuffer(b, '')
  expect(typeof l !== 'string' && l.lines).toEqual(['   1  select *', '   2  from emp', '   3* where 1=1'])
  const l2 = listBuffer(b, '2')
  if (typeof l2 === 'string') throw new Error(l2)
  b = l2.buf
  expect(b.cur).toBe(2)
  b = changeBuffer(b, '/emp/dept/') as typeof b
  expect(b.lines[1]).toBe('from dept')
  expect(changeBuffer(b, '/nope/x/')).toBe(ERR_NOT_FOUND)
  b = appendBuffer(b, ' d') as typeof b
  expect(b.lines[1]).toBe('from dept d')
  b = inputBuffer(b, 'join x on 1=1')
  expect(b.lines).toEqual(['select *', 'from dept d', 'join x on 1=1', 'where 1=1'])
  expect(b.cur).toBe(3)
  b = delBuffer(b, '') as typeof b
  expect(b.lines).toEqual(['select *', 'from dept d', 'where 1=1'])
  b = delBuffer(b, '2 LAST') as typeof b
  expect(bufferText(b)).toBe('select *')
  expect(listBuffer(b, '5')).toBe(ERR_RANGE)
  expect(listBuffer(emptyBuffer(), '')).toBe(ERR_EMPTY)
  // CHANGE 用别的分隔符、删除
  b = changeBuffer(setBuffer('select a, b from t'), '#, b#') as typeof b
  expect(b.lines[0]).toBe('select a from t')
})

test('SAVE / GET 的文件内容', () => {
  expect(saveText(setBuffer('select 1\nfrom dual'))).toBe('select 1\nfrom dual\n/\n')
  expect(getText('select 1\r\nfrom dual\r\n/\r\n')).toBe('select 1\nfrom dual')
  expect(withSqlExt('foo')).toBe('foo.sql')
  expect(withSqlExt('foo.txt')).toBe('foo.txt')
})

test('第三批命令的识别', () => {
  const ORA = 'Oracle 19c'
  expect(parseSqlPlusCommand('l', ORA)).toEqual({ kind: 'list', arg: '' })
  expect(parseSqlPlusCommand('list 2 3', ORA)).toEqual({ kind: 'list', arg: '2 3' })
  expect(parseSqlPlusCommand('a  where 1=1', ORA)).toEqual({ kind: 'append', text: ' where 1=1' })
  expect(parseSqlPlusCommand('c /emp/dept/', ORA)).toEqual({ kind: 'change', arg: '/emp/dept/' })
  expect(parseSqlPlusCommand('del 2', ORA)).toEqual({ kind: 'del', arg: '2' })
  expect(parseSqlPlusCommand('i order by 1', ORA)).toEqual({ kind: 'input', text: 'order by 1' })
  expect(parseSqlPlusCommand('save x', ORA)).toEqual({ kind: 'save', arg: 'x' })
  expect(parseSqlPlusCommand('get x nolist', ORA)).toEqual({ kind: 'get', arg: 'x nolist' })
  expect(parseSqlPlusCommand('help column', ORA)).toEqual({ kind: 'help', topic: 'column' })
  expect(parseSqlPlusCommand('show all', ORA)).toEqual({ kind: 'showLocal', option: 'ALL' })
  expect(parseSqlPlusCommand('show linesize', ORA)).toEqual({ kind: 'showLocal', option: 'LINESIZE' })
  expect(parseSqlPlusCommand('show sqlcode', ORA)).toEqual({ kind: 'showLocal', option: 'SQLCODE' })
  // SQL 不受影响
  for (const s of ['DELETE FROM t', 'INSERT INTO t VALUES (1)', 'ALTER TABLE t ADD c NUMBER', 'LOCK TABLE t IN SHARE MODE', 'SAVEPOINT s', 'CALL p()', 'show user']) {
    expect(parseSqlPlusCommand(s, ORA), s).toBeNull()
  }
})

// 2026-11 白盒审计 V-13:纯文本排版要和另外两种排版一样过滤控制字符,数据里的转义序列到不了 xterm。
test('renderPlain 过滤列名和值里的控制字符', () => {
  const st = newDisplayState()
  st.colsep = ' | '
  const t = { columns: ['ID', 'N\x1b[31mAME'], rows: [['1', '\x1b[2Jforged\x07'], ['2', null as unknown as string]], numeric: [true, false] }
  const lines = renderPlain(t, st, 80)
  expect(lines.join('\n')).not.toMatch(/[\x00-\x09\x0b-\x1f\x7f-\x9f]/)
  expect(lines[0]).toBe('ID | N[31mAME')
  expect(lines[2]).toBe(' 1 | [2Jforged')
})

test('renderPlain 普通数据不受过滤影响(含中文和空值)', () => {
  const st = newDisplayState()
  st.colsep = '|'
  expect(renderPlain({ columns: ['A', '名'], rows: [['x', '中文'], ['y', '']], numeric: [false, false] }, st, 80))
    .toEqual(['A|名', '-|----', 'x|中文', 'y|'])
})
