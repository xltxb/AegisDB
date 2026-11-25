import { test, expect } from '@playwright/test'

import { pickRunSql, statementAt } from '../../src/lib/sqlPick'

// 数据查询页按「执行」交出去哪一段:有选区用选区,否则光标所在语句(分号或空行为界)。
// 2026-11-03 生产报障:四条不带分号、空行隔开的 SELECT,选中一行执行却发了整段。

const A = "select * from g where platform_id='524'"
const B = "select * from g where platform_id='175'"
const FOUR = [A, '', B, '', A, '', A].join('\n')
const at = (text: string, needle: string) => text.indexOf(needle)

test('有选区:只用选区(去掉首尾空白)', () => {
  const s = at(FOUR, B)
  expect(pickRunSql(FOUR, s, s + B.length)).toBe(B)
  expect(pickRunSql(FOUR, s - 1, s + B.length + 1)).toBe(B) // 连带选进前后的换行
  expect(pickRunSql(FOUR, s + B.length, s)).toBe(B) // 反向选
})

test('选区里有多条照样整段交出去 —— 由后端以多条语句拒绝,这里不替人挑', () => {
  expect(pickRunSql('SELECT 1; SELECT 2', 0, 18)).toBe('SELECT 1; SELECT 2')
})

test('选区只有空白:当没选,取光标所在语句', () => {
  const s = at(FOUR, B) - 1 // 第 2 行(空行)那个换行
  expect(pickRunSql(FOUR, s, s + 1)).toBe(B)
})

test('没有选区:空行为界,取光标所在那条', () => {
  expect(statementAt(FOUR, at(FOUR, "'175'"))).toBe(B)
  expect(statementAt(FOUR, 0)).toBe(A)
  expect(statementAt(FOUR, FOUR.length)).toBe(A)
  expect(pickRunSql(FOUR, at(FOUR, B), at(FOUR, B))).toBe(B) // 行首
  expect(pickRunSql(FOUR, at(FOUR, B) + B.length, at(FOUR, B) + B.length)).toBe(B) // 行尾
})

test('只含空格 / 制表符 / \\r 的行也算空行', () => {
  const t = `SELECT 1\n  \t\r\nSELECT 2`
  expect(statementAt(t, t.length)).toBe('SELECT 2')
  expect(statementAt('SELECT 1\r\n\r\nSELECT 2', 0)).toBe('SELECT 1')
})

test('分号为界:光标在哪条取哪条,不带分号', () => {
  const t = 'SELECT 1;\nSELECT 2;\n'
  expect(statementAt(t, 3)).toBe('SELECT 1')
  expect(statementAt(t, 14)).toBe('SELECT 2')
  expect(statementAt('SELECT 1; SELECT 2', 15)).toBe('SELECT 2')
  expect(statementAt('SELECT 1;', 9)).toBe('SELECT 1') // 分号后面
})

test('光标落在两条之间的空白里:取前一条;前面没有就取后一条', () => {
  const t = 'SELECT 1;\n\n\nSELECT 2'
  expect(statementAt(t, 10)).toBe('SELECT 1')
  expect(statementAt('\n\n\nSELECT 2', 0)).toBe('SELECT 2')
})

test('一条语句跨多行(中间没有空行):整条', () => {
  const t = 'SELECT id,\n  name\nFROM t\nWHERE a = 1'
  expect(statementAt(t, 3)).toBe(t)
  expect(statementAt(t, t.length)).toBe(t)
})

test('单条语句与以前一样:整段原样(去首尾空白)', () => {
  expect(pickRunSql('  SELECT 1  \n', 0, 0)).toBe('SELECT 1')
  expect(pickRunSql('SELECT 1;', 9, 9)).toBe('SELECT 1')
})

test('引号、注释、美元引用里的分号与空行不算边界', () => {
  const str = "SELECT 'a;\n\nb' AS x"
  expect(statementAt(str, 0)).toBe(str)
  const ident = 'SELECT "a;b" FROM t'
  expect(statementAt(ident, 0)).toBe(ident)
  const block = 'SELECT 1 /* x;\n\n y */ + 2'
  expect(statementAt(block, 0)).toBe(block)
  const line = 'SELECT 1 -- a; b\n+ 2'
  expect(statementAt(line, 0)).toBe(line)
  const dollar = 'SELECT $$a;\n\nb$$'
  expect(statementAt(dollar, 0)).toBe(dollar)
  const esc = "SELECT 'it''s;' FROM t"
  expect(statementAt(esc, 0)).toBe(esc)
})

test('行注释后面紧跟空行:照样是边界;注释随它下面的语句走', () => {
  const t = 'SELECT 1 -- one\n\n-- two\nSELECT 2'
  expect(statementAt(t, 0)).toBe('SELECT 1 -- one')
  expect(statementAt(t, t.length)).toBe('-- two\nSELECT 2')
})

test('纯注释 / 空白的段不算一条:跳过它取前一条', () => {
  const t = 'SELECT 1\n\n-- 备注\n'
  expect(statementAt(t, t.length)).toBe('SELECT 1')
  expect(statementAt('', 0)).toBe('')
  expect(statementAt('  \n\n -- x\n', 3)).toBe('')
})
