// 表结构对比报告的导出:Markdown(贴变更单 / 群聊)与 CSV(逐条差异)。
//
// 纯函数,文案从外面传进来(t),这样中英文界面导出各自语言的报告,测试也不用起 vue-i18n。

import type { SchemaDiffResp, SchemaDiffColumn, SchemaDiffIndex, SchemaDiffTable } from '@/types'
import { csvCell } from '@/lib/csv'

type T = (key: string, named?: Record<string, unknown>) => string

// 报告头上的一边:按 schema 比时带上 schema,否则两边看起来是同一个库
const side = (s: SchemaDiffResp['left']) => `${s.instance}(${s.env}) / ${s.database}${s.schema ? '.' + s.schema : ''}`

/** 一处属性的说法:type → 类型 …… */
export const attrKey = (attr: string) => `sdAttr_${attr}`

export function diffToMarkdown(r: SchemaDiffResp, t: T): string {
  const d = r.diff
  if (!d) return ''
  const s = d.summary
  const out: string[] = []
  out.push(`# ${t('sdReportTitle')}`, '')
  out.push(`- ${t('sdLeft')}:${side(r.left)} · ${t('sdTablesN', { n: s.left })} · ${r.left.readAt}`)
  out.push(`- ${t('sdRight')}:${side(r.right)} · ${t('sdTablesN', { n: s.right })} · ${r.right.readAt}`)
  out.push(`- ${t('sdSame')} ${s.same} · ${t('sdOnlyLeft')} ${s.onlyLeft} · ${t('sdOnlyRight')} ${s.onlyRight} · ${t('sdChanged')} ${s.changed}`, '')
  if (r.emptySide) out.push(`> ${t('sdEmpty_' + r.emptySide)}`, '')
  if (d.onlyLeft.length) out.push(`## ${t('sdOnlyLeft')}(${d.onlyLeft.length})`, '', ...d.onlyLeft.map((x) => `- ${x}`), '')
  if (d.onlyRight.length) out.push(`## ${t('sdOnlyRight')}(${d.onlyRight.length})`, '', ...d.onlyRight.map((x) => `- ${x}`), '')
  if (d.changed.length) {
    out.push(`## ${t('sdChanged')}(${d.changed.length})`, '')
    for (const td of d.changed) {
      out.push(`### ${td.table}`, '')
      out.push(`| ${t('sdObject')} | ${t('sdAttr')} | ${t('sdLeft')} | ${t('sdRight')} |`, '|---|---|---|---|')
      for (const row of diffRows(td, t)) out.push(`| ${row.object} | ${row.attr} | ${md(row.left)} | ${md(row.right)} |`)
      out.push('')
    }
  }
  if (!d.onlyLeft.length && !d.onlyRight.length && !d.changed.length) out.push(t('sdAllSame', { n: s.same }), '')
  return out.join('\n')
}

/** CSV:表、对象、属性、左值、右值 —— 一条差异一行;只在一边的表也各占一行。 */
export function diffToCSV(r: SchemaDiffResp, t: T): string {
  const d = r.diff
  if (!d) return ''
  const rows: string[][] = [[t('sdTable'), t('sdObject'), t('sdAttr'), t('sdLeft') + ' ' + side(r.left), t('sdRight') + ' ' + side(r.right)]]
  for (const x of d.onlyLeft) rows.push([x, t('sdTable'), t('sdExists'), t('sdYes'), ''])
  for (const x of d.onlyRight) rows.push([x, t('sdTable'), t('sdExists'), '', t('sdYes')])
  for (const td of d.changed) for (const row of diffRows(td, t)) rows.push([td.table, row.object, row.attr, row.left, row.right])
  return '﻿' + rows.map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

export interface DiffRow { object: string; attr: string; left: string; right: string }

/** 一张表的差异摊成行:表属性、主键、列、索引 —— 页面上的对照表与导出用同一份。 */
export function diffRows(td: NonNullable<SchemaDiffResp['diff']>['changed'][number], t: T): DiffRow[] {
  const rows: DiffRow[] = []
  if (td.kind) rows.push({ object: t('sdTable'), attr: t('sdAttr_kind'), left: td.kind.left, right: td.kind.right })
  if (td.comment) rows.push({ object: t('sdTable'), attr: t('sdAttr_comment'), left: td.comment.left, right: td.comment.right })
  if (td.primaryKey) rows.push({ object: t('sdPrimaryKey'), attr: t('sdAttr_columns'), left: td.primaryKey.left, right: td.primaryKey.right })
  for (const c of td.columns || []) {
    const obj = `${t('sdColumn')} ${c.column}`
    if (c.status === 'onlyLeft') rows.push({ object: obj, attr: t('sdExists'), left: c.left || '', right: '' })
    else if (c.status === 'onlyRight') rows.push({ object: obj, attr: t('sdExists'), left: '', right: c.right || '' })
    else for (const ch of c.changes || []) {
      // 没有默认值时服务端给空串(不写死中文),这里按读者语言说
      const v = (x: string) => (ch.attr === 'default' && x === '' ? t('sdNoDefault') : x)
      rows.push({ object: obj, attr: t(attrKey(ch.attr)), left: v(ch.left), right: v(ch.right) })
    }
  }
  for (const ix of td.indexes || []) {
    rows.push({ object: `${t('sdIndex')} ${ix.index}`, attr: ix.status === 'changed' ? t('sdAttr_columns') : t('sdExists'), left: ix.left || '', right: ix.right || '' })
  }
  return rows
}

// Markdown 表格里的竖线与换行会把表格撑坏
const md = (s: string) => (s || '').replace(/\|/g, '\\|').replace(/\r?\n/g, ' ')

// ---------------------------------------------------------------- 左右对照


export interface ColumnPairRow {
  name: string
  left?: SchemaDiffColumn
  right?: SchemaDiffColumn
  /** 只在一边 */
  only?: 'left' | 'right'
  /** 两边都有、但不同的属性(type / nullable / default / comment / position) */
  diff: Set<string>
}

/**
 * 列的左右对照行:左边按原顺序,只在右边的接在后面。哪一格高亮由服务端的差异说了算
 * (归一化之后的判断),这里不自己再比一遍 —— 两处各比各的,迟早会说不一样的话。
 * caseInsensitive:MySQL 的列名不区分大小写(与服务端 nameKey 同一个规则)。
 */
export function columnPairs(td: SchemaDiffTable, caseInsensitive: boolean): ColumnPairRow[] {
  const key = (n: string) => (caseInsensitive ? n.toLowerCase() : n)
  const byOrd = (cols: SchemaDiffColumn[] = []) => [...cols].sort((a, b) => a.ordinal - b.ordinal)
  const diffs = new Map<string, Set<string>>()
  for (const c of td.columns || []) {
    if (c.status === 'changed') diffs.set(key(c.column), new Set((c.changes || []).map((x) => x.attr)))
  }
  const right = new Map(byOrd(td.rightDef?.columns).map((c) => [key(c.name), c]))
  const rows: ColumnPairRow[] = []
  const seen = new Set<string>()
  for (const l of byOrd(td.leftDef?.columns)) {
    const k = key(l.name)
    seen.add(k)
    const r = right.get(k)
    rows.push({ name: l.name, left: l, right: r, only: r ? undefined : 'left', diff: diffs.get(k) || new Set() })
  }
  for (const [k, r] of right) {
    if (!seen.has(k)) rows.push({ name: r.name, right: r, only: 'right', diff: new Set() })
  }
  return rows
}

export interface IndexMark {
  index: SchemaDiffIndex
  /** 服务端给的差异:只在这一边、定义变了;没有差异为空 */
  status?: 'only' | 'changed'
}

/**
 * 索引的左右对照:两边各列自己的索引,按服务端的差异标色。不在前端按名字配对 —— 勾了"忽略索引名"
 * 或 Oracle 系统命名的索引,服务端是按列组成配的,前端按名字配只会配错。支撑主键的索引不列
 * (主键单独一行)。
 */
export function indexMarks(td: SchemaDiffTable, caseInsensitive: boolean): { left: IndexMark[]; right: IndexMark[] } {
  const key = (n: string) => (caseInsensitive ? n.toLowerCase() : n)
  const st = new Map<string, string>()
  for (const d of td.indexes || []) st.set(d.status + ':' + key(d.index), d.status)
  const mark = (side: 'onlyLeft' | 'onlyRight') => (ix: SchemaDiffIndex): IndexMark => {
    const k = key(ix.name)
    if (st.has(side + ':' + k)) return { index: ix, status: 'only' }
    if (st.has('changed:' + k)) return { index: ix, status: 'changed' }
    return { index: ix }
  }
  return {
    left: (td.leftDef?.indexes || []).filter((i) => !i.primary).map(mark('onlyLeft')),
    right: (td.rightDef?.indexes || []).filter((i) => !i.primary).map(mark('onlyRight')),
  }
}

export const showIndexDef = (i?: SchemaDiffIndex) => (i ? (i.unique ? 'UNIQUE ' : '') + '(' + i.columns.join(', ') + ')' : '')
