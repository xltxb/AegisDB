// 标签式多选:控件里是已选的标签(可单个去掉),菜单里搜索、勾选,可设上限。用在"选一组名字"的
// 表单里(执行窗口的库)。与 VMultiSelect 不同:那个是列表页的筛选器(只显示汇总,选项带内部取值)。
// 外观与交互沿用 VSelect(同一套控件高度、菜单宽度、搜索框、焦点离开即关闭)。
//
// 已选的值即便不在 options 里也照样显示、可以去掉:编辑一条老记录时,它选过的项可能已经
// 不在候选里了(库改过名、实例暂时连不上),悄悄丢掉等于替人改了数据。
import { useEffect, useRef, useState, type FocusEvent, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { ChevronDown, Search, Check, X } from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import './VTagSelect.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  value: string[]
  onChange: (v: string[]) => void
  options: string[]
  searchable?: boolean
  height?: string
  searchPlaceholder?: string
  placeholder?: string
  /** 最多能选几个;0 = 不限。到上限后没选的项置灰 */
  max?: number
}

export default function VTagSelect({
  value, onChange, options, searchable = false, height = '40px', searchPlaceholder = '', placeholder = '', max = 0, className, ...rest
}: Props) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [kw, setKw] = useState('')
  const root = useRef<HTMLDivElement>(null)
  const box = useRef<HTMLInputElement>(null)

  const q = kw.trim().toLowerCase()
  const shown = !searchable || !q ? options : options.filter((o) => o.toLowerCase().includes(q))
  const picked = (o: string) => value.includes(o)
  const full = max > 0 && value.length >= max

  useEffect(() => { if (open && searchable) box.current?.focus() }, [open, searchable])

  function close() { setOpen(false); setKw('') }
  function toggleMenu() { setKw(''); setOpen(!open) }
  // 勾选顺序就是保存顺序:第一个勾的是"主库"(老界面里只显示一个库的地方显示它)
  const remove = (o: string) => onChange(value.filter((x) => x !== o))
  function toggle(o: string) {
    if (picked(o)) remove(o)
    else if (!full) onChange([...value, o])
  }
  function onBlur(e: FocusEvent) {
    if (e.relatedTarget && root.current?.contains(e.relatedTarget as Node)) return
    close()
  }

  return (
    <div
      {...rest} ref={root} className={clsx('vtag', className)} tabIndex={0} onBlur={onBlur}
      onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }}
    >
      <div className="control" style={{ minHeight: height }} onClick={toggleMenu}>
        <div className="chips">
          {value.map((v) => (
            <span key={v} className="chip" data-testid="vtag-chip">
              <span className="ct">{v}</span>
              <button type="button" className="cx" aria-label={t('selRemove', { v })} onClick={(e) => { e.stopPropagation(); remove(v) }}><X size={11} /></button>
            </span>
          ))}
          {!value.length && <span className="ph">{placeholder}</span>}
        </div>
        <ChevronDown size={14} className="chev" />
      </div>
      {open && (
        <div className="menu">
          {searchable && (
            <div className="sbox">
              <Search size={13} color="var(--text-faint)" />
              <input
                ref={box} value={kw} onChange={(e) => setKw(e.target.value)} placeholder={searchPlaceholder || t('selSearchPh')} spellCheck={false}
                onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); if (shown.length) toggle(shown[0]) } }}
              />
            </div>
          )}
          <div className="scy list">
            {shown.map((o) => (
              <div
                key={o} className={clsx('opt', picked(o) && 'active', full && !picked(o) && 'off')} role="option" aria-selected={picked(o)}
                onMouseDown={(e) => e.preventDefault()} onClick={() => toggle(o)}
              >
                <span className="box">{picked(o) && <Check size={11} />}</span>
                <span className="ot">{o}</span>
              </div>
            ))}
            {!shown.length && <div className="nores">{t('selNoMatch')}</div>}
          </div>
          {max > 0 && <div className="foot">{t('selPickedOf', { n: value.length, max })}</div>}
        </div>
      )}
    </div>
  )
}
