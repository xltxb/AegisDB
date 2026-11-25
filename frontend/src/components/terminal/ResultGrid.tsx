import { useMemo, useRef, useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { X, Copy, Check, Table2 } from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import './ResultGrid.scoped.css'

// more:后端还暂存着后续行(结果分批显示),表格底部给「加载更多」;loading 时按钮锁住
interface Props extends HTMLAttributes<HTMLDivElement> {
  columns: string[]
  rows: string[][]
  more?: boolean
  onClose: () => void
  onMore: () => void
}

// 单元格只占一行:值里的换行显示成 ↵。DWS 的 pgxc_wlm_session_info 这类视图里有整段 SQL、执行
// 计划(几百行),按原样换行会把整行撑到几千像素高;单元格又是垂直居中,其它列的值全落在屏幕
// 下方很远处 —— 表头下面看起来一片空白(2026-10 生产反馈)。完整值在悬停提示里,复制 TSV 也是完整的。
const oneLine = (v: string) => String(v).replace(/\r\n|\r|\n/g, ' ↵ ').replace(/\t/g, ' ')

export default function ResultGrid({ columns, rows, more, onClose, onMore, className, ...rest }: Props) {
  const { t } = useI18n()
  const [loadingMore, setLoadingMore] = useState(false)
  const loadingLock = useRef(false)
  function loadMore() {
    if (loadingLock.current) return
    loadingLock.current = true
    setLoadingMore(true)
    onMore()
    // 加载完成后父组件会换掉 rows;兜底 3 秒后解锁,免得请求失败时按钮一直灰着
    setTimeout(() => { loadingLock.current = false; setLoadingMore(false) }, 3000)
  }

  // A column is numeric (right-aligned) when every non-empty cell parses as a number.
  const numeric = useMemo(() =>
    columns.map((_, i) => {
      let saw = false
      for (const r of rows) {
        const v = r[i]
        if (v == null || v === '') continue
        saw = true
        if (!/^-?\d+(\.\d+)?$/.test(String(v).trim())) return false
      }
      return saw
    }), [columns, rows])

  const [copied, setCopied] = useState(false)
  async function copyTsv() {
    const head = columns.join('\t')
    const body = rows.map((r) => r.map((v) => (v ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n')
    try {
      await navigator.clipboard.writeText(head + '\n' + body)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch { /* ignore */ }
  }

  // 拖分隔条时父组件逐帧重渲染,只有高度在变;表格本体不跟着重算
  const table = useMemo(() => (
    <table className="rgtable">
      <thead>
        <tr>
          <th className="rgidx">#</th>
          {columns.map((col, i) => <th key={i} className={clsx(numeric[i] && 'num')}>{col}</th>)}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, ri) => (
          <tr key={ri}>
            <td className="rgidx">{ri + 1}</td>
            {columns.map((_, ci) => {
              const empty = r[ci] == null || r[ci] === ''
              return (
                <td key={ci} className={clsx(numeric[ci] && 'num', empty && 'null')} title={r[ci] || undefined}>
                  {empty ? '∅' : oneLine(r[ci])}
                </td>
              )
            })}
          </tr>
        ))}
        {!rows.length && <tr><td colSpan={columns.length + 1} className="rgempty">{t('gridEmpty')}</td></tr>}
      </tbody>
    </table>
  ), [columns, rows, numeric, t])

  const CopyIcon = copied ? Check : Copy
  return (
    <div {...rest} className={clsx('rg', className)}>
      <div className="rghead">
        <Table2 size={14} color="var(--accent-text)" />
        <span className="rgt">{t('gridResult')}</span>
        <span className="rgn">{t('gridRows', { n: rows.length })}</span>
        <div className="rgacts">
          <button className="rgbtn" title={t('gridCopyTsv')} onClick={copyTsv}><CopyIcon size={13} />{copied ? t('copied') : 'TSV'}</button>
          <button className="rgbtn" title={t('gridClose')} onClick={() => onClose()}><X size={14} /></button>
        </div>
      </div>
      <div className="rgscroll">
        {table}
        {more && (
          <div className="rgmore">
            <button className="rgbtn" disabled={loadingMore} data-testid="grid-load-more" onClick={loadMore}>{t('gridLoadMore')}</button>
          </div>
        )}
      </div>
    </div>
  )
}
