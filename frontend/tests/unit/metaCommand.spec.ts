import { test, expect } from '@playwright/test'

import { translateMetaSql } from '../../src/lib/metaCommand'

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
