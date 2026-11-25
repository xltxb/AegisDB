import { test, expect } from '@playwright/test'

import { makeCompletionSource, isSchemaChange } from '../../src/lib/termCompletion'
import type { ConnectionSchema } from '../../src/types'

// 终端补全的数据源:表清单一次取回、按会话缓存;列按表取、按表缓存;DDL 之后与切库之后作废。
// 各引擎"限定名"指的东西不同:MySQL 是库、PostgreSQL 是 schema、Oracle 是 owner。

const MYSQL: ConnectionSchema = { connectionId: 1, databases: [
  { name: 'shop', tables: [{ name: 'orders' }, { name: 'customers' }] },
  { name: 'hr', tables: [{ name: 'staff' }] },
] }
const PG: ConnectionSchema = { connectionId: 1, databases: [
  { name: 'appdb', tables: [], schemas: [{ name: 'public', tables: [{ name: 'orders' }] }, { name: 'audit', tables: [{ name: 'log' }] }] },
] }

function setup(engine: string, schema: ConnectionSchema, db: string) {
  const calls = { schema: 0, columns: [] as string[] }
  let cur = db
  const src = makeCompletionSource({
    engine,
    database: () => cur,
    databases: () => schema.databases.map((d) => d.name),
    metas: ['\\dt'],
    fetchSchema: async () => { calls.schema++; return schema },
    fetchColumns: async (_db, table) => { calls.columns.push(table); return table.endsWith('orders') ? ['id', 'amount'] : [] },
  })
  return { src, calls, use: (d: string) => { cur = d } }
}

test('MySQL:当前库的表、`库.` 的表;表清单只取一次', async () => {
  const { src, calls } = setup('MySQL 8.0', MYSQL, 'shop')
  expect(await src.tables('')).toEqual(['orders', 'customers'])
  expect(await src.tables('hr')).toEqual(['staff'])
  expect(await src.tables('HR')).toEqual(['staff'])
  expect(await src.tables('nope')).toEqual([])
  expect(calls.schema).toBe(1)
  expect(src.quote).toBe('`')
})

test('PostgreSQL:不限定是 public;`schema.` 是那个 schema 的表', async () => {
  const { src } = setup('PostgreSQL 16', PG, 'appdb')
  expect(await src.tables('')).toEqual(['orders'])
  expect(await src.tables('audit')).toEqual(['log'])
  expect(src.quote).toBe('"')
})

test('列按表缓存;DDL 之后与切库之后重新取', async () => {
  const { src, calls, use } = setup('MySQL 8.0', MYSQL, 'shop')
  expect(await src.columns('orders')).toEqual(['id', 'amount'])
  await src.columns('ORDERS')
  await src.tables('')
  expect(calls.columns).toEqual(['orders'])
  expect(calls.schema).toBe(1)
  src.invalidate()
  await src.columns('orders')
  await src.tables('')
  expect(calls.columns).toEqual(['orders', 'orders'])
  expect(calls.schema).toBe(2)
  use('hr')
  await src.columns('orders')
  expect(calls.columns.length).toBe(3) // 换了库,缓存键不同
})

test('取不到(网络 / 权限)当作没有候选,不抛出', async () => {
  const src = makeCompletionSource({
    engine: 'MySQL 8.0', database: () => 'shop', databases: () => [], metas: [],
    fetchSchema: async () => { throw new Error('boom') },
    fetchColumns: async () => { throw new Error('boom') },
  })
  expect(await src.tables('')).toEqual([])
  expect(await src.columns('orders')).toEqual([])
})

test('哪些语句会改变表结构(执行后作废缓存)', () => {
  for (const s of ['CREATE TABLE t (id int)', '  alter table t add c int', 'DROP TABLE t', 'rename table a to b', 'TRUNCATE t']) {
    expect(isSchemaChange(s), s).toBe(true)
  }
  for (const s of ['SELECT * FROM create_log', 'INSERT INTO t VALUES (1)', '\\dt']) {
    expect(isSchemaChange(s), s).toBe(false)
  }
})
