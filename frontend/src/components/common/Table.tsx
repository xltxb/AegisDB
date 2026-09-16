import type { ReactNode } from 'react'
import clsx from 'clsx'
import { useBreakpoint } from '@/hooks/useBreakpoint'
import { gridTemplate, visibleCols, type ColSpec } from '@/lib/tableColumns'

export interface Column<Row> extends ColSpec {
  head: ReactNode
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
  // 窄屏收列,而不是把八列压进 768px。被收起的值由各页自己的展开行交代 ——
  // 这里只负责「轨道与单元格始终一致」这一条。
  const bp = useBreakpoint()
  const cols = visibleCols(columns, bp)
  const tpl = gridTemplate(cols)
  return (
    <div className="c-table">
      <div className="c-thead" style={{ gridTemplateColumns: tpl }}>
        {cols.map((c) => <div key={c.key}>{c.head}</div>)}
      </div>
      {!rows.length && <div className="c-empty">{empty}</div>}
      {rows.map((r) => (
        <div
          key={rowKey(r)}
          className={clsx('c-trow', onRowClick && 'clickable')}
          style={{ gridTemplateColumns: tpl }}
          onClick={onRowClick ? () => onRowClick(r) : undefined}
        >
          {cols.map((c) => (
            <div key={c.key} className={clsx('c-td', c.mono && 'mono')}>{c.cell(r)}</div>
          ))}
        </div>
      ))}
    </div>
  )
}
