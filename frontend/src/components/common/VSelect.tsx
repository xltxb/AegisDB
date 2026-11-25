import { useEffect, useRef, useState, type CSSProperties, type FocusEvent, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { ChevronDown, Search } from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import { useEvent } from '@/hooks/useEvent'
import './VSelect.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  value: string
  onChange: (v: string) => void
  options: string[]
  // searchable turns the menu into filter-then-pick. Off by default: the short
  // dropdowns (engine, env, policy, retention) are faster to scan than to type
  // into, and a search box on four options is just noise. Turn it on where the
  // list grows with the deployment — the database instances, above all.
  searchable?: boolean
  // height matches the control to the form around it; pages mixing VSelect with
  // native inputs need the two to line up.
  height?: string
  searchPlaceholder?: string
  // creatable(配合 searchable):选项之外还能直接用输入的值 —— 候选是现场读出来的、可能不全
  // 的地方用(结构对比的库名)。输入不在选项里时,菜单末尾多一项「使用 xxx」。
  creatable?: boolean
  // value 为空时控件里显示的淡色提示
  placeholder?: string
  // floating:菜单按视口定位(position: fixed),不受外层 overflow 裁切,高度按视口剩余空间收、下面放不下
  // 就往上开。默认关:只在放在会裁切的卡片里的下拉上打开(数据查询页,2026-10-26 —— 库一多,菜单底部被
  // 下半屏的结果区盖住,滑到底也看不到最后几个)。
  floating?: boolean
}

export default function VSelect({
  value, onChange, options, searchable = false, height = '40px', searchPlaceholder = '',
  creatable = false, placeholder = '', floating = false, className, ...rest
}: Props) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [kw, setKw] = useState('')
  const root = useRef<HTMLDivElement>(null)
  const box = useRef<HTMLInputElement>(null)
  const menuEl = useRef<HTMLDivElement>(null)
  const [menuStyle, setMenuStyle] = useState<CSSProperties>({})
  const [listStyle, setListStyle] = useState<CSSProperties>({})

  const q = kw.trim()
  const shown = !searchable || !q ? options : options.filter((o) => o.toLowerCase().includes(q.toLowerCase()))
  // 可以直接使用的输入值:开了 creatable、输入非空、且不与某个选项完全相同
  const custom = creatable && searchable && q && !options.includes(q) ? q : ''

  // ---- floating 定位 ----
  const place = useEvent(() => {
    if (!floating || !root.current) return
    const r = root.current.getBoundingClientRect()
    const vh = window.innerHeight
    const chrome = (searchable ? 46 : 0) + 16 // 搜索框 + 内边距
    const below = vh - r.bottom - 8
    const above = r.top - 8
    const up = below < 220 && above > below
    const room = (up ? above : below) - chrome
    setListStyle({ maxHeight: `${Math.max(120, Math.min(360, room))}px` })
    setMenuStyle({
      position: 'fixed', left: `${Math.round(r.left)}px`, minWidth: `${Math.round(r.width)}px`, zIndex: 60, right: 'auto',
      ...(up ? { top: 'auto', bottom: `${Math.round(vh - r.top + 4)}px` } : { top: `${Math.round(r.bottom + 4)}px`, bottom: 'auto' }),
    })
  })
  // 外面滚动 / 窗口变化时跟着控件走;菜单自己的列表滚动不算
  useEffect(() => {
    if (!open || !floating) return
    const onChangeViewport = (e?: Event) => {
      if (e && e.target instanceof Node && menuEl.current?.contains(e.target)) return
      place()
    }
    window.addEventListener('resize', onChangeViewport)
    window.addEventListener('scroll', onChangeViewport, true)
    return () => {
      window.removeEventListener('resize', onChangeViewport)
      window.removeEventListener('scroll', onChangeViewport, true)
    }
  }, [open, floating, place])
  useEffect(() => { if (open && searchable) box.current?.focus() }, [open, searchable])

  function toggle() {
    if (!open) { setKw(''); place() }
    setOpen(!open)
  }
  function close() { setOpen(false); setKw('') }
  function pick(o: string) { onChange(o); close() }

  // React's onBlur is focusout: it bubbles and carries relatedTarget, so the menu
  // closes only when focus genuinely leaves the component — not when the search
  // box inside it takes focus.
  function onBlur(e: FocusEvent) {
    if (e.relatedTarget && root.current?.contains(e.relatedTarget as Node)) return
    close()
  }

  // Enter takes the first remaining match — typing enough to narrow the list to one
  // and pressing Enter is the whole point of a searchable select.
  // creatable 时,与输入完全相同的选项优先,再是第一个匹配项,没有匹配才用输入值本身。
  function onEnter() {
    if (creatable && q && options.includes(q)) pick(q)
    else if (shown.length) pick(shown[0])
    else if (custom) pick(custom)
  }

  return (
    <div
      {...rest} ref={root} className={clsx('vsel', className)} tabIndex={0} onBlur={onBlur}
      onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }}
    >
      <div className="control" style={{ height }} onClick={toggle}>
        <span className={clsx('val', !value && 'ph')}>{value || placeholder}</span>
        <ChevronDown size={14} className="chev" />
      </div>
      {open && (
        <div ref={menuEl} className="menu" style={floating ? menuStyle : undefined}>
          {searchable && (
            <div className="sbox">
              <Search size={13} color="var(--text-faint)" />
              <input
                ref={box} value={kw} onChange={(e) => setKw(e.target.value)}
                placeholder={searchPlaceholder || t('selSearchPh')} spellCheck={false}
                onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); onEnter() } }}
              />
            </div>
          )}
          <div className="scy list" style={floating ? listStyle : undefined}>
            {/* preventDefault on mousedown keeps focus where it is, so picking an option
                never trips blur before the click lands. */}
            {shown.map((o) => (
              <div
                key={o} className={clsx('opt', o === value && 'active')}
                onMouseDown={(e) => e.preventDefault()} onClick={() => pick(o)}
              >{o}</div>
            ))}
            {custom && (
              <div className="opt custom" data-testid="vsel-custom" onMouseDown={(e) => e.preventDefault()} onClick={() => pick(custom)}>
                {t('selUse', { v: custom })}
              </div>
            )}
            {!shown.length && !custom && <div className="nores">{t('selNoMatch')}</div>}
          </div>
        </div>
      )}
    </div>
  )
}
