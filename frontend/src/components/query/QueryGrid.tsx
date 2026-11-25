// 数据查询页的结果表格:粘性表头、按值推断的列类型、斑马纹与悬停高亮、单元格省略 + 一键复制、
// 前端分页。行数本来就被授权上限卡住(默认 1000),全在内存里,分页只是为了不一次画几千个格子。
//
// 底栏左边留给父组件放状态(成功 / 行数 / 耗时 / 截断 / 脱敏),右边是分页。
import { memo, useMemo, useState, type HTMLAttributes, type ReactNode } from 'react'
import clsx from 'clsx'
import { Hash, CalendarClock, ToggleLeft, Type, Copy, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react'
import { inferColumnType, type ColumnType } from '@/lib/columnType'
import { copyText } from '@/lib/clipboard'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { useI18n } from '@/hooks/useI18n'
import { useUIStore } from '@/stores/ui'
import './QueryGrid.scoped.css'

// vertical:结尾写了 \G —— 像 mysql 客户端那样一行一块、每列一行(宽表看单行时用)。
// ddl:SHOW CREATE TABLE 的结果(见 lib/bizResultView)—— 画成一整块代码,不塞进单元格。
interface Props extends HTMLAttributes<HTMLDivElement> {
  columns: string[]
  rows: string[][]
  vertical?: boolean
  ddl?: { name: string; ddl: string } | null
  status?: ReactNode
}

const typeIcon = { num: Hash, date: CalendarClock, bool: ToggleLeft, text: Type } as const
const typeKey = { num: 'qTypeNum', date: 'qTypeDate', bool: 'qTypeBool', text: 'qTypeText' } as const
const PAGE_SIZES = [50, 100, 200, 500]

// 服务端把 NULL 与空字符串都送成 ''(cellString),这里分不出来:空串画 ∅ 并在提示里说明,不冒充 NULL
// (f7eec1d 起一律写成 NULL,把真正的空串也标成了 NULL —— 2026-10-26 回归排查)。
const empty = (v: string | null | undefined) => v == null || v === ''
// 单元格只占一行:换行显示成 ↵(完整值在悬停提示里、复制出去的也是完整值)
const oneLine = (v: string) => v.replace(/\r\n|\r|\n/g, ' ↵ ').replace(/\t/g, ' ')

// memo:查询页每敲一个键都会重新渲染,几百行的表格不该跟着比对一遍。
export default memo(function QueryGrid({ columns, rows, vertical, ddl, status, className, ...rest }: Props) {
  const { t } = useI18n()
  const ddlHtml = useMemo(() => ({ __html: ddl ? highlightSqlHtml(ddl.ddl) : '' }), [ddl])

  const types = useMemo<ColumnType[]>(() => columns.map((_, i) => inferColumnType(rows, i)), [columns, rows])

  const [pageSize, setPageSize] = useState(100)
  const [page, setPage] = useState(1)
  // 换了一份结果就回到第一页
  const [shownRows, setShownRows] = useState(rows)
  if (shownRows !== rows) { setShownRows(rows); setPage(1) }
  const pages = Math.max(1, Math.ceil(rows.length / pageSize))
  const start = (page - 1) * pageSize
  const visible = rows.slice(start, start + pageSize)
  const go = (p: number) => setPage(Math.min(pages, Math.max(1, p)))

  // 竖排时列名右对齐成一栏(与 mysql 客户端一样),按最长的列名定宽,封顶免得一个超长列名把值挤没了。
  const keyWidth = `${Math.min(36, Math.max(4, ...columns.map((c) => c.length)) + 1)}ch`

  async function copyCell(v: string | null | undefined) {
    if (await copyText(v ?? '')) useUIStore.getState().notify(t('qCellCopied'), 'success')
  }

  return (
    <div {...rest} className={clsx('qg', className)}>
      {ddl ? (
        <div className="qg-scroll scy scx qg-ddlwrap" data-testid="q-ddl-view">
          <div className="qg-ddlh">
            <span className="qg-ddln">{ddl.name}</span>
            <button className="qg-ddlcopy" data-testid="q-ddl-copy" onClick={() => copyCell(ddl.ddl)}><Copy size={12} />{t('qCopyDdl')}</button>
          </div>
          <pre className="qg-ddl" dangerouslySetInnerHTML={ddlHtml} />
        </div>
      ) : vertical ? (
        <div className="qg-scroll scy scx" data-testid="q-vertical">
          {visible.map((r, ri) => (
            <div key={start + ri} className="qg-rec">
              <div className="qg-rec-h">{`*************************** ${start + ri + 1}. row ***************************`}</div>
              {columns.map((col, ci) => (
                <div key={ci} className="qg-kv">
                  <span className="qg-k" style={{ width: keyWidth }}>{col}:</span>
                  <span className={clsx('qg-v', empty(r[ci]) && 'null')}>{r[ci] == null ? 'NULL' : r[ci] === '' ? '∅' : r[ci]}</span>
                  {!empty(r[ci]) && <button className="qg-copy qg-kv-copy" title={t('qCopyCell')} onClick={() => copyCell(r[ci])}><Copy size={11} /></button>}
                </div>
              ))}
            </div>
          ))}
          {!rows.length && <div className="qg-empty">{t('gridEmpty')}</div>}
        </div>
      ) : (
        <div className="qg-scroll scy scx">
          <table className="qg-table">
            <thead>
              <tr>
                <th className="qg-idx">#</th>
                {columns.map((col, i) => {
                  const Icon = typeIcon[types[i]]
                  const tip = { title: t('qTypeInferred', { type: t(typeKey[types[i]]) }) }
                  return (
                    <th key={i} className={clsx(types[i] === 'num' && 'num') || undefined}>
                      <span className="qg-th">
                        <Icon size={12} className="qg-type" {...tip} />
                        <span className="qg-col" title={col}>{col}</span>
                      </span>
                    </th>
                  )
                })}
              </tr>
            </thead>
            <tbody>
              {visible.map((r, ri) => (
                <tr key={start + ri}>
                  <td className="qg-idx">{start + ri + 1}</td>
                  {columns.map((_, ci) => (
                    <td key={ci} className={clsx(types[ci] === 'num' && 'num', empty(r[ci]) && 'null') || undefined}>
                      <span className="qg-cell" title={empty(r[ci]) ? t('gridEmptyTip') : r[ci]}>{r[ci] == null ? 'NULL' : r[ci] === '' ? '∅' : oneLine(r[ci])}</span>
                      {!empty(r[ci]) && <button className="qg-copy" title={t('qCopyCell')} onClick={() => copyCell(r[ci])}><Copy size={11} /></button>}
                    </td>
                  ))}
                </tr>
              ))}
              {!rows.length && <tr><td colSpan={columns.length + 1} className="qg-empty">{t('gridEmpty')}</td></tr>}
            </tbody>
          </table>
        </div>
      )}
      <div className="qg-foot">
        <div className="qg-status">{status}</div>
        {!ddl && rows.length > PAGE_SIZES[0] && (
          <div className="qg-pager" data-testid="q-pager">
            <select value={pageSize} onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1) }} className="qg-size" aria-label={t('qPageSizeLabel')}>
              {PAGE_SIZES.map((n) => <option key={n} value={n}>{t('qPageSize', { n })}</option>)}
            </select>
            <button className="qg-pbtn" disabled={page === 1} aria-label={t('qPageFirst')} onClick={() => go(1)}><ChevronsLeft size={14} /></button>
            <button className="qg-pbtn" disabled={page === 1} aria-label={t('qPagePrev')} onClick={() => go(page - 1)}><ChevronLeft size={14} /></button>
            <span className="qg-pno">{t('qPageOf', { p: page, total: pages })}</span>
            <button className="qg-pbtn" disabled={page === pages} aria-label={t('qPageNext')} onClick={() => go(page + 1)}><ChevronRight size={14} /></button>
            <button className="qg-pbtn" disabled={page === pages} aria-label={t('qPageLast')} onClick={() => go(pages)}><ChevronsRight size={14} /></button>
          </div>
        )}
      </div>
    </div>
  )
})
