import { test, expect } from '@playwright/test'

import { translateMetaSql, translateDescribe } from '../../src/lib/metaCommand'

// Describing a relation is answered by querying the catalog for its columns, and
// a name that does not exist simply yields ZERO ROWS. The terminal then drew an
// empty bordered grid — indistinguishable from "this relation exists but has no
// columns you can see", so an operator could reasonably conclude the object was
// there. Real psql says: Did not find any relation named "…".
//
// The translator therefore has to tell the caller what an empty result MEANS for
// this particular command, so the terminal can report it instead of rendering a
// header with nothing under it.
test('describing a specific relation carries a not-found notice', () => {
  for (const engine of ['postgres', 'dws', 'gaussdb', 'oracle']) {
    const r = translateMetaSql('\\d g_big_dwd_user_oneid_result_df', engine)
    expect(r, engine).not.toBeNull()
    // The notice must be a TRANSLATABLE descriptor — a message id plus its
    // parameters — not baked-in text. Returning a literal string meant the
    // terminal printed Chinese even with the UI switched to English, because this
    // module has no way to know the active locale.
    expect(r!.emptyNotice, engine).toBeTruthy()
    expect(r!.emptyNotice!.id, engine).toBe('termRelationNotFound')
    expect(r!.emptyNotice!.params, engine).toEqual({ name: 'g_big_dwd_user_oneid_result_df' })
    // No human-language text may appear in this module at all.
    expect(JSON.stringify(r!.emptyNotice), engine).not.toMatch(/[一-鿿]/)
  }
})

// Listing commands legitimately return nothing (an empty schema has no tables),
// so they must NOT claim the object was not found.
test('listing commands have no not-found notice', () => {
  for (const cmd of ['\\dt', '\\dn', '\\l', '\\dv', '\\di', '\\d']) {
    const r = translateMetaSql(cmd, 'postgres')
    expect(r, cmd).not.toBeNull()
    expect(r!.emptyNotice, cmd).toBeUndefined()
  }
})

test('the translations still resolve to the same queries', () => {
  expect(translateMetaSql('\\dt', 'postgres')!.sql).toContain('information_schema.tables')
  expect(translateMetaSql('\\l', 'postgres')!.sql).toContain('pg_database')
  expect(translateMetaSql('\\d users', 'postgres')!.sql).toContain('information_schema.columns')
  expect(translateMetaSql('\\d users', 'mysql')!.sql).toBe('SHOW COLUMNS FROM `users`')
  expect(translateMetaSql('\\dt', 'mysql')!.sql).toContain('SHOW FULL TABLES')
  expect(translateMetaSql('\\d users', 'oracle')!.sql).toContain('all_tab_columns')
  expect(translateMetaSql('\\nope', 'postgres')).toBeNull()
})

// The argument is interpolated into the query as a literal, so its sanitiser is
// a security control: only identifier characters may survive.
test('the identifier sanitiser strips anything that could break out of the literal', () => {
  const hostile = [
    `users' OR '1'='1`,
    `users'; DROP TABLE t; --`,
    'users`',
    'users"',
    'users\\',
    'users; SELECT 1',
  ]
  for (const arg of hostile) {
    const sql = translateMetaSql('\\d ' + arg, 'postgres')!.sql
    // The interpolated table name must consist solely of identifier characters,
    // so nothing can terminate the literal it sits inside.
    const literal = /table_name='([^']*)'/.exec(sql)
    expect(literal, arg).not.toBeNull()
    expect(literal![1], arg).toMatch(/^[A-Za-z0-9_$.]*$/)
    // And the statement stays a single one: no separator survives.
    expect(sql, arg).not.toContain(';')
  }
})

// A schema-qualified name still targets that schema.
test('a schema-qualified relation keeps its schema filter', () => {
  const sql = translateMetaSql('\\d public.users', 'postgres')!.sql
  expect(sql).toContain("table_schema='public'")
  expect(sql).toContain("table_name='users'")
})

// ---------------------------------------------------------------- DESC / DESCRIBE
//
// DESC 是 SQL*Plus 的客户端命令,不是 SQL。原样发给 Oracle 服务端会得到
// ORA-00900: invalid SQL statement —— 而它是每个 DBA 的肌肉记忆,不能只回一句
// 报错了事。MySQL 家族的 DESC 本来就是合法 SQL,那里必须原样放行,别去修一个
// 没坏的东西。
test('DESC 在没有该语法的引擎上被翻译,在 MySQL 家族原样放行', () => {
  for (const engine of ['oracle', 'postgres', 'dws', 'sqlite']) {
    const r = translateDescribe('desc emp', engine)
    expect(r, engine).not.toBeNull()
    expect(r!.sql.toLowerCase(), engine).not.toContain('desc emp')
    expect(r!.emptyNotice?.id, engine).toBe('termRelationNotFound')
  }
  // MySQL / TiDB / PolarDB / MariaDB:DESC 是原生语法
  for (const engine of ['MySQL 8.0', 'TiDB 7', 'PolarDB', 'MariaDB']) {
    expect(translateDescribe('desc emp', engine), engine).toBeNull()
  }
})

test('DESCRIBE 全拼与大小写、结尾分号都认', () => {
  for (const cmd of ['DESCRIBE emp', 'Desc emp;', 'desc   emp  ;']) {
    const r = translateDescribe(cmd, 'oracle')
    expect(r, cmd).not.toBeNull()
    expect(r!.sql, cmd).toContain("'EMP'")
  }
})

// ORDER BY … DESC 不是描述命令 —— 拦错了会把一条正常查询变成目录查询。
test('DESC 作为排序关键字不被误拦', () => {
  for (const cmd of ['SELECT * FROM t ORDER BY id DESC', 'select a desc, b from t']) {
    expect(translateDescribe(cmd, 'oracle'), cmd).toBeNull()
  }
  expect(translateDescribe('desc', 'oracle')).toBeNull() // 没有对象名
})

// v$session 是公共同义词,ALL_TAB_COLUMNS 里的真名是 V_$SESSION。只按名字直查
// 会返回零行 —— 用户看到的是"空表格",而不是这个视图的列。
test('Oracle 的描述要能穿透同义词', () => {
  const r = translateDescribe('desc v$session', 'oracle')
  expect(r).not.toBeNull()
  expect(r!.sql.toLowerCase()).toContain('all_synonyms')
  expect(r!.sql).toContain("'V$SESSION'")
})

// SQL*Plus 的 DESCRIBE 会给出长度/精度;只显示 VARCHAR2 而不说多长,等于没描述。
test('Oracle 的描述带出长度与精度', () => {
  const sql = translateDescribe('desc emp', 'oracle')!.sql.toLowerCase()
  expect(sql).toContain('char_length')
  expect(sql).toContain('data_precision')
})

// owner.table 要按属主限定,否则同名表会混在一起。
test('限定名按属主过滤', () => {
  const r = translateDescribe('desc scott.emp', 'oracle')
  expect(r!.sql).toContain("'SCOTT'")
  expect(r!.sql).toContain("'EMP'")
})

// 参数是拼进 SQL 字符串字面量的,注入必须挡住。
test('对象名里的引号与分号不得穿透', () => {
  const r = translateDescribe(`desc emp'; DROP TABLE users --`, 'oracle')
  // 要么直接不认(原样交给服务端去拒),要么翻译出来但注入不得穿透 —— 两者都安全
  expect(r === null || (!r.sql.includes(';') && !/drop\s+table/i.test(r.sql))).toBe(true)
})
