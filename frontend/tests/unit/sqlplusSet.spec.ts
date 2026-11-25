import { test, expect } from '@playwright/test'

import { parseSqlPlusSet, serverOutputValue, isShowServerOutput } from '../../src/lib/sqlplusSet'

const ORA = 'Oracle 19c'

test('SQL*Plus 的 SET 在 Oracle 上被识别', () => {
  expect(parseSqlPlusSet('SET SERVEROUTPUT ON', ORA)).toEqual({ option: 'SERVEROUTPUT', value: 'ON' })
  expect(parseSqlPlusSet('set serveroutput on size unlimited;', ORA)).toEqual({ option: 'SERVEROUTPUT', value: 'on size unlimited' })
  expect(parseSqlPlusSet('SET LINESIZE 200', ORA)?.option).toBe('LINESIZE')
  expect(parseSqlPlusSet('-- 注释\nSET DEFINE OFF', ORA)?.option).toBe('DEFINE')
  expect(parseSqlPlusSet('/* x */ set echo off', ORA)?.option).toBe('ECHO')
})

test('Oracle 自己的 SET 语句与别的引擎的 SET 照常下发', () => {
  expect(parseSqlPlusSet('SET TRANSACTION READ ONLY', ORA)).toBeNull()
  expect(parseSqlPlusSet('set role all', ORA)).toBeNull()
  expect(parseSqlPlusSet('SET CONSTRAINTS ALL DEFERRED', ORA)).toBeNull()
  expect(parseSqlPlusSet('SET NAMES utf8mb4', 'MySQL 8.0')).toBeNull()
  expect(parseSqlPlusSet('SET search_path TO app', 'PostgreSQL 15')).toBeNull()
  expect(parseSqlPlusSet("SELECT 'SET X' FROM DUAL", ORA)).toBeNull()
})

test('SERVEROUTPUT 的开关值', () => {
  expect(serverOutputValue({ option: 'SERVEROUTPUT', value: 'ON' })).toBe(true)
  expect(serverOutputValue({ option: 'SERVEROUT', value: 'off' })).toBe(false)
  expect(serverOutputValue({ option: 'SERVEROUTPUT', value: 'on size 1000000' })).toBe(true)
  expect(serverOutputValue({ option: 'SERVEROUTPUT', value: 'maybe' })).toBeNull()
  expect(serverOutputValue({ option: 'LINESIZE', value: 'ON' })).toBeNull()
})

test('SHOW SERVEROUTPUT', () => {
  expect(isShowServerOutput('show serveroutput', ORA)).toBe(true)
  expect(isShowServerOutput('SHOW SERVEROUT;', ORA)).toBe(true)
  expect(isShowServerOutput('show user', ORA)).toBe(false)
  expect(isShowServerOutput('show serveroutput', 'MySQL 8.0')).toBe(false)
})

test('整段脚本里最后一次 SET SERVEROUTPUT 的值', async () => {
  const { lastServerOutputIn } = await import('../../src/lib/sqlplusSet')
  expect(lastServerOutputIn('SET SERVEROUTPUT ON\nBEGIN NULL; END;\n/', ORA)).toBe(true)
  expect(lastServerOutputIn('set serveroutput on\n...\nset serveroutput off', ORA)).toBe(false)
  expect(lastServerOutputIn('SELECT 1 FROM DUAL;', ORA)).toBeNull()
  expect(lastServerOutputIn('SET SERVEROUTPUT ON', 'MySQL 8.0')).toBeNull()
})

test('Oracle 语句结束规则:SET / EXEC 回车即结束,PL/SQL 块等 END; 或 /', async () => {
  const { oracleStatementComplete: done, isOraclePlsql } = await import('../../src/lib/sqlplusSet')
  expect(done('set serveroutput on', ORA)).toBe(true)
  expect(done("exec dbms_output.put_line('x')", ORA)).toBe(true)
  expect(done('begin', ORA)).toBe(false)
  expect(done("begin\n  dbms_output.put_line('x');", ORA)).toBe(false) // 块里的分号不算
  expect(done("begin\n  for i in 1..3 loop\n    null;\n  end loop;", ORA)).toBe(false) // END LOOP; 不算
  expect(done("begin\n  if 1=1 then null; end if;", ORA)).toBe(false)
  expect(done("begin\n  null;\nend;", ORA)).toBe(true)
  expect(done("begin for i in 1..3 loop null; end loop; end;", ORA)).toBe(true)
  expect(done("create or replace procedure p as\nbegin\n  null;\nend p;", ORA)).toBe(true)
  expect(done("declare\n n number;\nbegin\n null;\nend;\n/", ORA)).toBe(true)
  expect(done('select 1 from dual', ORA)).toBeUndefined() // 普通 SQL 按默认规则
  expect(done('begin', 'MySQL 8.0')).toBeUndefined()
  expect(isOraclePlsql('BEGIN NULL; END;', ORA)).toBe(true)
  expect(isOraclePlsql('CREATE OR REPLACE PACKAGE BODY pk AS END;', ORA)).toBe(true)
  expect(isOraclePlsql('SELECT 1 FROM DUAL', ORA)).toBe(false)
  expect(isOraclePlsql('BEGIN;', 'PostgreSQL 15')).toBe(false)
})
