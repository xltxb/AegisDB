import { test, expect } from '@playwright/test'
import { splitVertical, ddlOf, showCreateSql, bizClientTimeoutMs } from '@/lib/bizResultView'

test.describe('bizResultView', () => {
  test('结尾的 \\G 剥掉并竖排;\\g 剥掉照常横排;中间的不动', () => {
    expect(splitVertical('SELECT * FROM t\\G')).toEqual({ sql: 'SELECT * FROM t', vertical: true })
    expect(splitVertical('SELECT * FROM t \\G;  \n')).toEqual({ sql: 'SELECT * FROM t', vertical: true })
    expect(splitVertical('SELECT * FROM t\\g')).toEqual({ sql: 'SELECT * FROM t', vertical: false })
    expect(splitVertical("SELECT 'a\\G' FROM t")).toEqual({ sql: "SELECT 'a\\G' FROM t", vertical: false })
    expect(splitVertical('SELECT 1')).toEqual({ sql: 'SELECT 1', vertical: false })
    expect(splitVertical('  \\G')).toEqual({ sql: '  \\G', vertical: false }) // 只有 \\G:原样交给后端
  })

  test('SHOW CREATE TABLE / VIEW 的结果认得出来,别的不算', () => {
    expect(ddlOf({ columns: ['Table', 'Create Table'], rows: [['orders', 'CREATE TABLE orders (id int)']] }))
      .toEqual({ name: 'orders', ddl: 'CREATE TABLE orders (id int)' })
    expect(ddlOf({ columns: ['View', 'Create View', 'character_set_client', 'collation_connection'], rows: [['v', 'CREATE VIEW v AS SELECT 1', 'utf8mb4', 'x']] }))
      .toEqual({ name: 'v', ddl: 'CREATE VIEW v AS SELECT 1' })
    expect(ddlOf({ columns: ['id', 'name'], rows: [['1', 'a']] })).toBeNull()
    expect(ddlOf({ columns: ['Table', 'Create Table'], rows: [] })).toBeNull()
    expect(ddlOf(null)).toBeNull()
  })

  test('看 DDL 的语句一律反引号;带反引号的名字不给', () => {
    expect(showCreateSql('g_ads_order')).toBe('SHOW CREATE TABLE `g_ads_order`')
    expect(showCreateSql('Mixed Case')).toBe('SHOW CREATE TABLE `Mixed Case`')
    expect(showCreateSql('a`b')).toBe('')
    expect(showCreateSql('')).toBe('')
  })

  test('浏览器等待时间跟着授权时限走,总比库端时限多 60 秒', () => {
    expect(bizClientTimeoutMs(30)).toBe(360_000) // 短时限也至少等到默认上限 300 秒 + 60
    expect(bizClientTimeoutMs(300)).toBe(360_000)
    expect(bizClientTimeoutMs(600)).toBe(660_000) // 原先写死 330 秒,会在 600 秒的查询跑到一半时断开
    expect(bizClientTimeoutMs(3600)).toBe(3_660_000)
    expect(bizClientTimeoutMs(undefined)).toBe(360_000)
    expect(bizClientTimeoutMs(0)).toBe(360_000)
  })
})
