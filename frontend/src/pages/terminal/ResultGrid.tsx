import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Table2, Copy, Check, X } from 'lucide-react'
import { copyText } from '@/lib/clipboard'

export interface GridResult { columns: string[]; rows: string[][] }

/**
 * 结果表格。
 *
 * 终端是画在 canvas 上的:那里的文字不在 DOM 里,选不中、搜不到、也复制不走一整列。
 * 核对一列数据、把结果贴进工单的时候要的正是这些,所以同一份结果在下面再给一张
 * **真的 HTML 表**。终端那边只留一行摘要,两处不重复打印同一批行。
 */
export function ResultGrid({ columns, rows, onClose }: GridResult & { onClose: () => void }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)

  // 一列是数值列(右对齐)当且仅当每个非空单元格都解析成数字。空列不算 —— 一列
  // 全是 NULL 时右对齐只是猜。
  const numeric = columns.map((_, i) => {
    let saw = false
    for (const r of rows) {
      const v = r[i]
      if (v == null || v === '') continue
      saw = true
      if (!/^-?\d+(\.\d+)?$/.test(String(v).trim())) return false
    }
    return saw
  })

  async function copyTsv() {
    // 制表符分隔:贴进表格软件就是一张表。单元格里的制表符和换行压成空格,否则
    // 一个带换行的字段会把后面所有行都错开一格。
    const head = columns.join('\t')
    const body = rows.map((r) => r.map((v) => (v ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n')
    if (!(await copyText(head + '\n' + body))) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }

  return (
    <div className="rg">
      <div className="rg-head">
        <Table2 size={14} />
        <span className="rg-t">{t('gridResult')}</span>
        <span className="rg-n">{t('gridRows', { n: rows.length })}</span>
        <div className="rg-acts">
          <button className="rg-btn" title={t('gridCopyTsv')} onClick={copyTsv}>
            {copied ? <Check size={13} /> : <Copy size={13} />}{copied ? t('copied') : 'TSV'}
          </button>
          <button className="rg-btn" title={t('gridClose')} onClick={onClose}><X size={14} /></button>
        </div>
      </div>
      <div className="rg-scroll">
        <table className="rg-table">
          <thead>
            <tr>
              <th className="rg-idx">#</th>
              {columns.map((col, i) => <th key={i} className={clsx(numeric[i] && 'num')}>{col}</th>)}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, ri) => (
              <tr key={ri}>
                <td className="rg-idx">{ri + 1}</td>
                {columns.map((_, ci) => {
                  // `∅` 而不是留白:空字符串与 NULL 在屏幕上长得一样,而它们不是
                  // 一回事 —— 看一眼就要能分出来。
                  const nul = r[ci] == null || r[ci] === ''
                  return <td key={ci} className={clsx(numeric[ci] && 'num', nul && 'nul')}>{nul ? '∅' : r[ci]}</td>
                })}
              </tr>
            ))}
            {!rows.length && (
              <tr><td colSpan={columns.length + 1} className="rg-empty">{t('gridEmpty')}</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
