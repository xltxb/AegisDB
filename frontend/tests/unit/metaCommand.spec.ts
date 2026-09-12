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
  // 这里曾经钉的是 information_schema.columns —— 而它的 data_type 只给基础类型名,
  // varchar(128) 变成 "character varying",numeric(38,6) 变成 "numeric"。那是这条
  // 命令最要紧的信息,所以现在走 pg_catalog + format_type,和 psql 给的一致。
  expect(translateMetaSql('\\d users', 'postgres')!.sql).toContain('format_type')
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
    const literal = /relname='([^']*)'/.exec(sql)
    expect(literal, arg).not.toBeNull()
    expect(literal![1], arg).toMatch(/^[A-Za-z0-9_$.]*$/)
    // And the statement stays a single one: no separator survives.
    expect(sql, arg).not.toContain(';')
  }
})

// A schema-qualified name still targets that schema.
test('a schema-qualified relation keeps its schema filter', () => {
  const sql = translateMetaSql('\\d public.users', 'postgres')!.sql
  expect(sql).toContain("nspname='public'")
  expect(sql).toContain("relname='users'")
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

// psql 的 \\d 与平台的 \\d 对不上:平台丢了类型的长度与精度,也没有索引段。
//
// 类型丢精度不是"少显示一点",是**给错信息** —— 一个把 numeric(38,6) 读成
// numeric 的人,会以为这一列没有标度。
test('\\d 的类型带长度与精度(不是光一个基础类型名)', () => {
  const m = translateMetaSql('\\d t_order', 'dws')!
  expect(m).toBeTruthy()
  // format_type 给的就是 psql 那一列:character varying(128) / numeric(38,6)
  expect(m.sql).toContain('format_type')
  // information_schema.columns 的 data_type 正是丢精度的那条路,不能再用
  expect(m.sql).not.toContain('information_schema.columns')
})

test('\\d 带上排序规则一列', () => {
  const m = translateMetaSql('\\d t_order', 'postgres')!
  expect(m.sql).toContain('Collation')
})

test('\\d 有第二段:索引', () => {
  const m = translateMetaSql('\\d t_order', 'dws')!
  expect(m.follow).toBeTruthy()
  expect(m.follow!.sql).toContain('pg_get_indexdef')
  // 两段查的必须是同一张表
  expect(m.follow!.sql).toContain("c.relname='t_order'")
})

test('\\d 带 schema 时两段都按 schema 过滤', () => {
  const m = translateMetaSql('\\d public.t_order', 'dws')!
  expect(m.sql).toContain("n.nspname='public'")
  expect(m.follow!.sql).toContain("n.nspname='public'")
})

// 名字仍然要被 ident() 洗过 —— 它进的是字符串字面量,这条是安全边界不是格式化。
test('\\d 的表名不会把引号或分号带进 SQL', () => {
  const m = translateMetaSql("\\d t'; DROP TABLE x; --", 'dws')!
  expect(m.sql).not.toContain(';')
  expect(m.sql).not.toContain("'; DROP")
})

// 列表类命令没有第二段 —— 只有"描述一张表"才既有列又有索引。
test('列表类命令不带第二段', () => {
  for (const cmd of ['\\dt', '\\l', '\\dn', '\\du']) {
    expect(translateMetaSql(cmd, 'dws')!.follow).toBeUndefined()
  }
})

// SQLite 走自己的分支,不落进 MySQL 兜底。
//
// 兜底的意思是「认不出的引擎当 MySQL」,而 SQLite 连 SHOW 都不认 —— 敲 \dt 换回来
// 的是一句语法错误。开发库与全部演示实例都是 SQLite,等于这套元命令在最常用的环境
// 里不可用,所以这几条要钉住。
test('SQLite 的元命令不发 SHOW', () => {
  for (const engine of ['sqlite', 'SQLite']) {
    expect(translateMetaSql('\\dt', engine)?.sql).toContain('sqlite_master')
    expect(translateMetaSql('\\dt', engine)?.sql).not.toContain('SHOW')
    expect(translateMetaSql('\\conninfo', engine)?.sql).toContain('sqlite_version()')
    expect(translateMetaSql('\\di', engine)?.sql).toContain('sqlite_master')
    expect(translateMetaSql('\\di orders', engine)?.sql).toContain('pragma_index_list')
  }
})

// 内部表不该出现在 \dt 里:它们不是用户的表,列出来只会让人以为自己的库多了几张。
test('SQLite 的表清单排除 sqlite_ 内部表', () => {
  expect(translateMetaSql('\\dt', 'sqlite')?.sql).toContain("NOT LIKE 'sqlite_%'")
})

// \du / \dg 在 SQLite 上没有对应物。返回 null 让它原样送去被拒绝,好过编一条
// 查得出东西但答非所问的 SQL。
test('SQLite 上没有用户概念的命令返回 null', () => {
  expect(translateMetaSql('\\du', 'sqlite')).toBeNull()
  expect(translateMetaSql('\\dg', 'sqlite')).toBeNull()
})

// 其它引擎不受影响。
test('SQLite 分支不影响 MySQL 与 PG', () => {
  expect(translateMetaSql('\\dt', 'mysql')?.sql).toContain('SHOW FULL TABLES')
  expect(translateMetaSql('\\dt', 'postgres')?.sql).toContain('information_schema')
})

// 引擎家族只有一张表,就是 lib/engines 那张。
//
// 这个文件原先自己用正则又判了一遍(`/mysql|tidb|mariadb|polardb/`),而那份正则漏掉
// 了那张表最要紧的一条:PolarDB 的两个版本标签里都带 polardb,得先看 PostgreSQL 标记。
// 于是 PolarDB for PostgreSQL 在这里被当成 MySQL 家族,DESC 原样发过去 —— PG 不认
// DESC,回来的是一句语法错误,而用户敲的是他每天都在敲的那条命令。
test('PolarDB for PostgreSQL 的 DESC 按 PG 翻译,不当作 MySQL 放行', () => {
  const pg = translateDescribe('desc orders', 'PolarDB for PostgreSQL')
  expect(pg, 'PG 版应当翻译成目录查询,而不是原样放过去').not.toBeNull()
  expect(pg!.sql.toLowerCase()).toContain('information_schema.columns')

  // MySQL 版不翻译 —— 那里 DESC 本来就是合法语法。
  expect(translateDescribe('desc orders', 'PolarDB for MySQL')).toBeNull()
})
