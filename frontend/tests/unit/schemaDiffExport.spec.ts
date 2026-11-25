import { test, expect } from '@playwright/test'
import { diffToMarkdown, diffToCSV, diffRows, columnPairs, indexMarks } from '../../src/lib/schemaDiffExport'
import type { SchemaDiffResp } from '../../src/types'

// 表结构对比报告的导出:Markdown 贴变更单 / 群聊,CSV 逐条差异。文案由外面传入,这里用键名本身。
const t = (k: string, n?: Record<string, unknown>) => (n ? `${k}${JSON.stringify(n)}` : k)

const R: SchemaDiffResp = {
  left: { connectionId: 1, instance: 'orders-prod', env: 'prod', engine: 'MySQL 8.0', database: 'orders', readAt: '2026-09-29T10:00:00Z', tables: 3 },
  right: { connectionId: 2, instance: 'orders-uat', env: 'uat', engine: 'MySQL 8.0', database: 'orders', readAt: '2026-09-29T10:00:01Z', tables: 3 },
  diff: {
    summary: { left: 3, right: 3, same: 1, onlyLeft: 1, onlyRight: 1, changed: 1 },
    same: ['t_user'], onlyLeft: ['t_legacy'], onlyRight: ['t_new'],
    changed: [{
      table: 't_order',
      comment: { left: '订单', right: '订单|表' },
      columns: [
        { column: 'note', status: 'changed', changes: [{ attr: 'type', left: 'varchar(100)', right: 'varchar(200)' }] },
        { column: 'remark', status: 'onlyRight', right: '=cmd() NOT NULL' },
      ],
      indexes: [{ index: 'idx_status', status: 'onlyRight', right: '(status)' }],
    }],
  },
}

test('差异摊成行:表属性、列、索引各一行,只在一边的另一边为空', () => {
  const rows = diffRows(R.diff!.changed[0], t)
  expect(rows.map((r) => [r.object, r.attr, r.left, r.right])).toEqual([
    ['sdTable', 'sdAttr_comment', '订单', '订单|表'],
    ['sdColumn note', 'sdAttr_type', 'varchar(100)', 'varchar(200)'],
    ['sdColumn remark', 'sdExists', '', '=cmd() NOT NULL'],
    ['sdIndex idx_status', 'sdExists', '', '(status)'],
  ])
})

test('Markdown:汇总、三组、每张不同表一张对照表;竖线转义,不撑坏表格', () => {
  const md = diffToMarkdown(R, t)
  expect(md).toContain('orders-prod(prod) / orders')
  expect(md).toContain('- t_legacy')
  expect(md).toContain('- t_new')
  expect(md).toContain('### t_order')
  expect(md).toContain('| sdColumn note | sdAttr_type | varchar(100) | varchar(200) |')
  expect(md).toContain('订单\\|表')
})

test('按 schema 比时报告头带上 schema;一边是空库时带上提示', () => {
  const r: SchemaDiffResp = { ...R, emptySide: 'right', left: { ...R.left, schema: 'app' }, right: { ...R.right, schema: 'app_uat' } }
  const md = diffToMarkdown(r, t)
  expect(md).toContain('orders-prod(prod) / orders.app')
  expect(md).toContain('orders-uat(uat) / orders.app_uat')
  expect(md).toContain('> sdEmpty_right')
  expect(diffToCSV(r, t).split('\r\n')[0]).toContain('orders.app_uat')
})

test('CSV:带 BOM,逐条一行,只在一边的表也各占一行;防公式注入', () => {
  const csv = diffToCSV(R, t)
  expect(csv.startsWith('﻿')).toBe(true)
  const lines = csv.trim().split('\r\n')
  expect(lines).toHaveLength(1 + 2 + 4) // 表头 + 两张只在一边的表 + t_order 的四条
  expect(lines[1]).toBe('t_legacy,sdTable,sdExists,sdYes,')
  expect(csv).toContain("'=cmd() NOT NULL") // 以 = 开头的单元格前加 '
})

test('两边一致:Markdown 说一致,不列空的分组', () => {
  const same: SchemaDiffResp = { ...R, diff: { summary: { left: 1, right: 1, same: 1, onlyLeft: 0, onlyRight: 0, changed: 0 }, same: ['t_user'], onlyLeft: [], onlyRight: [], changed: [] } }
  const md = diffToMarkdown(same, t)
  expect(md).toContain('sdAllSame{"n":1}')
  expect(md).not.toContain('## ')
})

// ---- 左右对照 ----

const col = (name: string, dataType: string, ordinal: number) => ({ name, dataType, nullable: true, hasDefault: false, default: '', comment: '', ordinal })
const TD = {
  table: 't_order',
  columns: [
    { column: 'note', status: 'changed' as const, changes: [{ attr: 'type', left: 'varchar(100)', right: 'varchar(200)' }] },
    { column: 'remark', status: 'onlyRight' as const, right: 'text NULL' },
  ],
  indexes: [{ index: 'idx_status', status: 'onlyRight' as const, right: '(status)' }],
  leftDef: { name: 't_order', kind: 'table', comment: '', primaryKey: ['id'], columns: [col('id', 'bigint', 1), col('NOTE', 'varchar(100)', 2)],
    indexes: [{ name: 'PRIMARY', columns: ['id'], unique: true, primary: true }] },
  rightDef: { name: 't_order', kind: 'table', comment: '', primaryKey: ['id'], columns: [col('id', 'bigint', 1), col('note', 'varchar(200)', 2), col('remark', 'text', 3)],
    indexes: [{ name: 'PRIMARY', columns: ['id'], unique: true, primary: true }, { name: 'idx_status', columns: ['status'], unique: false }] },
}

test('列对照:按左边顺序,只在右边的接后面;哪格高亮由服务端差异决定;MySQL 名字不分大小写', () => {
  const rows = columnPairs(TD, true)
  expect(rows.map((r) => [r.name, r.only ?? '', [...r.diff].join()])).toEqual([
    ['id', '', ''], ['NOTE', '', 'type'], ['remark', 'right', ''],
  ])
  // 区分大小写的引擎:NOTE 与 note 是两列
  expect(columnPairs(TD, false).map((r) => r.only ?? '')).toEqual(['', 'left', 'right', 'right'])
})

test('索引对照:两边各列自己的,主键索引不列,按服务端差异标色', () => {
  const m = indexMarks(TD, true)
  expect(m.left).toEqual([])
  expect(m.right.map((x) => [x.index.name, x.status])).toEqual([['idx_status', 'only']])
})

test('没有默认值时按读者语言说,不是空白', () => {
  const r = diffRows({ table: 't', columns: [{ column: 'c', status: 'changed', changes: [{ attr: 'default', left: '0', right: '' }] }] }, t)
  expect(r[0].right).toBe('sdNoDefault')
})
