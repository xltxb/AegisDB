/**
 * 多选下拉。VSelect 的兄弟,不是它的一个开关 —— 两者的取值类型不同(一个 string、
 * 一个 string[]),混成一个组件的话每个调用点都要先分辨自己拿到的是哪一种。
 *
 * 选项用 { v, label } 而不是裸字符串:VSelect 认的是**显示文案**,于是每个调用点都要
 * 写一段"把标签映射回内部取值"的来回。多选再走一遍那条路,映射会写四份。
 */
import { useRef, useState, type FocusEvent, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { ChevronDown, Check } from 'lucide-react'
import './VMultiSelect.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  /** 已选中的内部取值;空数组 = 不筛 */
  value: string[]
  onChange: (v: string[]) => void
  options: { v: string; label: string }[]
  /** 一个都没选时显示的文案 —— 说"全部…",不是"请选择" */
  placeholder: string
  /** 选了 n 项时的文案模板,{n} 会被替换掉 */
  summary?: string
  height?: string
}

export default function VMultiSelect({ value, onChange, options, placeholder, summary = '', height = '36px', className, ...rest }: Props) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)

  // 一项时直接显示那一项的名字:「已选 1 项」要人再点开一次才知道是哪一项,而这一格
  // 本来就放得下。
  const n = value.length
  const label = !n ? placeholder
    : n === 1 ? options.find((o) => o.v === value[0])?.label || placeholder
      : (summary || '{n}').replace('{n}', String(n))

  const pick = (v: string) => onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v])

  // 与 VSelect 同一条:React 的 onBlur 会冒泡(即 focusout)并带着 relatedTarget,所以焦点
  // 真的离开这个组件时才关。
  function onBlur(e: FocusEvent) {
    if (e.relatedTarget && root.current?.contains(e.relatedTarget as Node)) return
    setOpen(false)
  }

  return (
    <div
      {...rest} ref={root} className={clsx('vmsel', className)} tabIndex={0} onBlur={onBlur}
      onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); setOpen(false) } }}
    >
      <div className={clsx('control', n > 0 && 'on')} style={{ height }} onClick={() => setOpen((o) => !o)}>
        <span className="val">{label}</span>
        <ChevronDown size={14} className="chev" />
      </div>
      {open && (
        <div className="menu">
          <div className="scy list">
            {/* mousedown 阻止默认:保住焦点,免得点中一项时先触发 blur */}
            {options.map((o) => (
              <div
                key={o.v} className={clsx('opt', value.includes(o.v) && 'active')}
                onMouseDown={(e) => e.preventDefault()} onClick={() => pick(o.v)}
              >
                <span className="box">{value.includes(o.v) && <Check size={11} />}</span>
                {' '}{o.label}
              </div>
            ))}
          </div>
          {/* 清空只在选过东西时出现:一个永远亮着、点了什么也不会变的按钮是噪音 */}
          {n > 0 && <div className="clr" onMouseDown={(e) => e.preventDefault()} onClick={() => onChange([])}>{placeholder}</div>}
        </div>
      )}
    </div>
  )
}
