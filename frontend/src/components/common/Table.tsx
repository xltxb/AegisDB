import type { ReactNode } from 'react'
import clsx from 'clsx'

export interface Column<Row> {
  key: string
  head: ReactNode
  /** grid 列宽,如 '1.5fr' / '96px'(原型各表都用 grid 而不是 table)。 */
  width: string
  cell: (row: Row) => ReactNode
  mono?: boolean
}

/**
 * 表格用 CSS grid 而不是 <table>。
 *
 * 原型里每张表的列宽都是 `1.5fr 1.1fr …` 这种比例,而 <table> 的列宽由内容决定,
 * 同一张表在不同数据下会跳来跳去 —— 那正是"刷新一下列就变宽了"的来源。
 */
export function Table<Row>({
  columns, rows, rowKey, empty, onRowClick,
}: {
  columns: Column<Row>[]
  rows: Row[]
  rowKey: (r: Row) => string | number
  empty?: ReactNode
  onRowClick?: (r: Row) => void
}) {
  const tpl = columns.map((c) => c.width).join(' ')
  return (
    <div className="c-table">
      <div className="c-thead" style={{ gridTemplateColumns: tpl }}>
        {columns.map((c) => <div key={c.key}>{c.head}</div>)}
      </div>
      {!rows.length && <div className="c-empty">{empty}</div>}
      {rows.map((r) => (
        <div
          key={rowKey(r)}
          className={clsx('c-trow', onRowClick && 'clickable')}
          style={{ gridTemplateColumns: tpl }}
          onClick={onRowClick ? () => onRowClick(r) : undefined}
        >
          {columns.map((c) => (
            <div key={c.key} className={clsx('c-td', c.mono && 'mono')}>{c.cell(r)}</div>
          ))}
        </div>
      ))}
    </div>
  )
}
