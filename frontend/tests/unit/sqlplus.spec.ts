import { test, expect } from '@playwright/test'

import {
  parseSqlPlusCommand, sqlPlusLineComplete, newDefineState, parseDefineArg, parseAcceptArg,
  substitute, applySubstSet, showSubstSetting, createdUnit, showErrorsFor, elapsedText,
} from '../../src/lib/sqlplus'
import { parseSqlPlusSet, oracleStatementComplete } from '../../src/lib/sqlplusSet'

// SQL*Plus 客户端命令,终端一侧(sqlplus-compat 第一批)。

const ORA = 'Oracle 19c'
const kind = (s: string, e = ORA) => parseSqlPlusCommand(s, e)?.kind ?? null

test('拒绝绕开网关的命令,缩写与符号写法都认', () => {
  for (const s of ['conn scott/tiger@orcl', 'CONNECT / AS SYSDBA', 'disc', 'host ls', 'ho', '!ls', '$dir',
    'startup', 'SHUTDOWN IMMEDIATE', 'recover database', 'archive log list', 'passw', 'copy from a@x to b@y',
    'ed', 'edit', 'exit', 'EXIT;', 'quit', 'store set x']) {
    expect(kind(s), s).toBe('refuse')
  }
  expect(parseSqlPlusCommand('conn a/b', ORA)).toEqual({ kind: 'refuse', name: 'CONNECT', why: 'Account' })
  expect(parseSqlPlusCommand('!ls', ORA)).toEqual({ kind: 'refuse', name: 'HOST', why: 'Host' })
})

// COLUMN / CLEAR 第三批起真正生效(sqlplus-compat 10),不再是"忽略"
test('报表命令与暂不支持的命令:忽略', () => {
  expect(kind('col ename format a20')).toBe('column')
  expect(kind('clear columns')).toBe('clear')
  for (const s of ['break on deptno', 'comp sum of sal on deptno', 'ttitle x', 'btitle x', 'repheader x', 'pause']) {
    expect(kind(s), s).toBe('ignored')
  }
})

test('PROMPT 原样保留文字,REMARK 是注释', () => {
  expect(parseSqlPlusCommand('prompt 开始建表; 别停;', ORA)).toEqual({ kind: 'prompt', text: '开始建表; 别停;' })
  expect(parseSqlPlusCommand('PRO', ORA)).toEqual({ kind: 'prompt', text: '' })
  expect(kind("rem don't")).toBe('remark')
  expect(kind('REMARK x')).toBe('remark')
})

test('DEFINE / UNDEFINE / ACCEPT / TIMING / RUN / / / SHOW', () => {
  expect(parseSqlPlusCommand('def owner = APP', ORA)).toEqual({ kind: 'define', arg: 'owner = APP' })
  expect(parseSqlPlusCommand('undef a b', ORA)).toEqual({ kind: 'undefine', names: ['a', 'b'] })
  expect(kind('accept x prompt "x:"')).toBe('accept')
  expect(kind('timing start t1')).toBe('timing')
  expect(kind('/')).toBe('slash')
  expect(kind('r')).toBe('run')
  expect(kind('RUN')).toBe('run')
  expect(kind('show errors')).toBe('showErrors')
  expect(kind('SHOW ERR;')).toBe('showErrors')
  expect(parseSqlPlusCommand('show define', ORA)).toEqual({ kind: 'showLocal', option: 'DEFINE' })
  // 带对象的 SHOW ERRORS、SHOW USER 交给网关翻译
  expect(kind('SHOW ERRORS PROCEDURE p')).toBeNull()
  expect(kind('SHOW USER')).toBeNull()
})

test('不是 SQL*Plus 命令:SQL、长得像的词、多行文本、其他引擎', () => {
  for (const s of ['SELECT host FROM t', 'COMMENT ON TABLE t IS \'x\'', 'COMMIT', 'CALL p()', 'hostname', 'conn/x',
    'CREATE TABLE copy_of_t AS SELECT * FROM t', 'RENAME a TO b', 'r x', 'DECLARE x NUMBER; BEGIN NULL; END;']) {
    expect(kind(s), s).toBeNull()
  }
  // 粘贴进来的多行脚本以 DEFINE 开头:不能整段当成一条 DEFINE
  expect(kind('DEFINE x = 1\nSELECT &x FROM dual')).toBeNull()
  for (const e of ['MySQL 8.0', 'PostgreSQL 15']) {
    for (const s of ['exit', 'prompt x', 'copy t from stdin', 'define x', '/']) expect(kind(s, e), `${e}: ${s}`).toBeNull()
  }
})

test('这些命令以换行结束,不等分号', () => {
  for (const s of ['prompt hi', 'define x = 1', 'show user', 'desc emp', '/', 'r', 'conn a/b', 'col x format a10', 'timing start']) {
    expect(sqlPlusLineComplete(s, ORA), s).toBe(true)
    expect(oracleStatementComplete(s, ORA), s).toBe(true)
  }
  // 普通 SQL 照旧等分号;多行的不算
  expect(oracleStatementComplete('select * from emp', ORA)).toBeUndefined()
  expect(sqlPlusLineComplete('select 1\nfrom dual', ORA)).toBe(false)
  expect(sqlPlusLineComplete('prompt hi', 'MySQL 8.0')).toBe(false)
})

// 2026-10 发现的 bug:粘贴一段以 SET SERVEROUTPUT ON 开头的脚本,整段被当成一条 SET 吞掉
test('多行文本不再被当成一条 SET', () => {
  expect(parseSqlPlusSet("SET SERVEROUTPUT ON\nBEGIN DBMS_OUTPUT.PUT_LINE('a'); END;", ORA)).toBeNull()
  expect(parseSqlPlusSet('SET SERVEROUTPUT ON', ORA)?.option).toBe('SERVEROUTPUT')
})

test('DEFINE 的参数', () => {
  expect(parseDefineArg('')).toEqual({ list: true })
  expect(parseDefineArg('owner')).toEqual({ show: 'OWNER' })
  expect(parseDefineArg('owner = APP')).toEqual({ set: 'OWNER', value: 'APP' })
  expect(parseDefineArg("msg = 'hello world'")).toEqual({ set: 'MSG', value: 'hello world' })
  expect(parseDefineArg("q = 'it''s'")).toEqual({ set: 'Q', value: "it's" })
  expect(parseDefineArg('x = a b')).toEqual({ set: 'X', value: 'a' })
  expect(parseDefineArg('bad name = 1')).toEqual({ error: true })
})

test('ACCEPT 的参数', () => {
  expect(parseAcceptArg("pwd CHAR PROMPT 'Password: ' HIDE")).toEqual({ name: 'PWD', type: 'CHAR', prompt: 'Password: ', hide: true })
  expect(parseAcceptArg('n NUMBER DEFAULT 10 PROMPT Enter a number')).toEqual({ name: 'N', type: 'NUMBER', def: '10', prompt: 'Enter a number', hide: false })
  expect(parseAcceptArg('x')).toEqual({ name: 'X', type: 'CHAR', hide: false })
  expect(parseAcceptArg('x NOPROMPT')).toEqual({ name: 'X', type: 'CHAR', prompt: '', hide: false })
  expect(parseAcceptArg('x WHATEVER')).toBeNull()
})

const noAsk = async () => { throw new Error('不该问') }

test('替换变量:已定义的直接展开,字面量里也展开,连接符被吃掉', async () => {
  const st = newDefineState()
  st.vars.set('OWNER', 'APP')
  st.vars.set('TBL', 'orders')
  const r = await substitute("SELECT * FROM &owner..&tbl._bak WHERE note = 'by &owner'", st, noAsk, ORA)
  expect(r.text).toBe("SELECT * FROM APP.orders_bak WHERE note = 'by APP'")
  expect(r.changed).toEqual([{ line: 1, old: "SELECT * FROM &owner..&tbl._bak WHERE note = 'by &owner'", new: r.text }])
})

test('替换变量:未定义的问人;& 只用一次,&& 记下来', async () => {
  const st = newDefineState()
  const asked: string[] = []
  const ask = async (n: string) => { asked.push(n); return n === 'a' ? '1' : '2' }
  const r = await substitute('SELECT &a, &a, &&b, &b FROM dual', st, ask, ORA)
  expect(r.text).toBe('SELECT 1, 1, 2, 2 FROM dual')
  expect(asked).toEqual(['a', 'a', 'b']) // 单 & 每次都问,&& 问一次记住
  expect(st.vars.get('B')).toBe('2')
  expect(st.vars.has('A')).toBe(false)
})

test('替换变量:取消、关掉、换字符、转义', async () => {
  const st = newDefineState()
  expect((await substitute('SELECT &x FROM dual', st, async () => null, ORA)).cancelled).toBe(true)
  // SET DEFINE OFF:原样
  expect(applySubstSet('SET DEFINE OFF', st)).toBe(true)
  expect((await substitute("SELECT 'a&b' FROM dual", st, noAsk, ORA)).text).toBe("SELECT 'a&b' FROM dual")
  // 换成 ^
  expect(applySubstSet("set define '^'", st)).toBe(true)
  st.vars.set('X', '42')
  expect((await substitute('SELECT ^x, 1 & 2 FROM dual', st, noAsk, ORA)).text).toBe('SELECT 42, 1 & 2 FROM dual')
  // 转义
  const st2 = newDefineState()
  applySubstSet('SET ESCAPE ON', st2)
  expect((await substitute("SELECT 'a\\&b' FROM dual", st2, noAsk, ORA)).text).toBe("SELECT 'a&b' FROM dual")
  // 后面不是名字的 & 原样
  expect((await substitute('SELECT a & b FROM dual', newDefineState(), noAsk, ORA)).text).toBe('SELECT a & b FROM dual')
  // VERIFY
  const st3 = newDefineState()
  expect(applySubstSet('set verify off', st3)).toBe(true)
  expect(st3.verify).toBe(false)
  expect(showSubstSetting('VERIFY', st3)).toBe('verify OFF')
  expect(showSubstSetting('DEFINE', newDefineState())).toBe('define "&" (hex 26)')
  // 写错的值不接手(交给 SET 的"已忽略 / 用法"提示)
  expect(applySubstSet('SET DEFINE maybe', newDefineState())).toBe(false)
  expect(applySubstSet('SET LINESIZE 200', newDefineState())).toBe(false)
})

test('替换变量:多行脚本里 DEFINE / SET DEFINE 当场生效,REMARK 不展开', async () => {
  const st = newDefineState()
  const script = 'DEFINE t = orders\nREM &nothing\nSELECT * FROM &t;\nSET DEFINE OFF\nSELECT \'&t\' FROM dual;'
  const r = await substitute(script, st, noAsk, ORA)
  expect(r.text).toBe("DEFINE t = orders\nREM &nothing\nSELECT * FROM orders;\nSET DEFINE OFF\nSELECT '&t' FROM dual;")
  expect(r.changed.map((c) => c.line)).toEqual([3])
})

test('SHOW ERRORS 补上一次 CREATE 的单元', () => {
  expect(createdUnit('CREATE OR REPLACE PACKAGE BODY app.pk_agent AS ...')).toEqual({ type: 'PACKAGE BODY', owner: 'APP', name: 'PK_AGENT' })
  expect(createdUnit('create procedure p_x is begin null; end;')).toEqual({ type: 'PROCEDURE', owner: '', name: 'P_X' })
  expect(createdUnit('-- x\nCREATE OR REPLACE FORCE VIEW v AS SELECT 1 FROM dual')).toEqual({ type: 'VIEW', owner: '', name: 'V' })
  expect(createdUnit('CREATE TABLE t (id NUMBER)')).toBeNull()
  expect(showErrorsFor({ type: 'PACKAGE BODY', owner: 'APP', name: 'PK_AGENT' })).toBe('SHOW ERRORS PACKAGE BODY APP.PK_AGENT')
  expect(showErrorsFor({ type: 'PROCEDURE', owner: '', name: 'P_X' })).toBe('SHOW ERRORS PROCEDURE P_X')
  // 名字带引号:后端不翻译,终端提示人自己写
  expect(showErrorsFor(createdUnit('CREATE PROCEDURE "my proc" IS BEGIN NULL; END;')!)).toBeNull()
})

test('TIMING 的耗时写法', () => {
  expect(elapsedText(1234)).toBe('Elapsed: 00:00:01.23')
  expect(elapsedText(3_723_450)).toBe('Elapsed: 01:02:03.45')
})
