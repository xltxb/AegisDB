/**
 * 日期时间选择器 —— 自己画的,不用 <input type="datetime-local">。
 *
 * 换掉原生控件的唯一原因是**语言**:Chromium 的日期面板(月份名、星期表头、"清除/今天")
 * 跟的是**浏览器的界面语言**,不是页面的 `lang`,也不归 i18n 管。所以在一个已经
 * 切成英文的表单里,点开那个框弹出来的仍然是"2026年09月 一 二 三 …"。这不是漏翻,是
 * 一块页面根本改不动的浏览器 chrome —— 想让它跟着界面走,就只能不用它。
 *
 * 月份标题故意写成 `2026-09` 这种数字形式,而不是月份名:这样它对中英文读者都一样准确,
 * 也就不需要再维护十二个月份名的两份译文(以及将来第三份)。星期表头复用已有的
 * dow1..dow7 词条,与「班车」页上的发车日勾选是同一套字。
 *
 * 值的格式与原生控件保持一致(`YYYY-MM-DDTHH:mm`,本地时间),所以调用方那些
 * `new Date(v).toISOString()` 一行都不用改。
 */
import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import { CalendarDays, ChevronLeft, ChevronRight, X } from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import { useEvent } from '@/hooks/useEvent'
import './VDateTime.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}

const pad = (n: number) => String(n).padStart(2, '0')
const ymd = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

/**
 * 六行 × 七列的格子,周一起头。
 *
 * 固定六行是故意的:行数随月份变化时,面板的高度会跟着跳,而人正在里面点日期。
 */
function monthCells(cursor: Date) {
  const first = new Date(cursor.getFullYear(), cursor.getMonth(), 1)
  // getDay() 里周日是 0;这里按 ISO 把周一当第一天。
  const lead = (first.getDay() + 6) % 7
  return Array.from({ length: 42 }, (_, i) => {
    const d = new Date(first)
    d.setDate(first.getDate() - lead + i)
    return { key: ymd(d), day: d.getDate(), outside: d.getMonth() !== cursor.getMonth() }
  })
}

export default function VDateTime({ value, onChange, placeholder, className, ...rest }: Props) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const fieldEl = useRef<HTMLButtonElement>(null)
  const popEl = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: string; left: string }>()
  // 面板当前显示的月份。打开时对齐到已选日期,没选过就对齐到今天。
  const [cursor, setCursor] = useState(() => new Date())

  // 拆出日期与时间两半;值为空时时间给一个 00:00 的起点,而日期留空。
  const datePart = (value || '').slice(0, 10)
  const timePart = (value || '').slice(11, 16) || '00:00'
  const todayKey = ymd(new Date())
  const display = value ? value.replace('T', ' ').slice(0, 16) : ''

  function toggle() {
    if (!open) {
      const d = datePart ? new Date(`${datePart}T00:00:00`) : new Date()
      setCursor(new Date(d.getFullYear(), d.getMonth(), 1))
    }
    setOpen(!open)
  }
  const shiftMonth = (n: number) => setCursor(new Date(cursor.getFullYear(), cursor.getMonth() + n, 1))
  // 还没选日期就先调了时间:补上今天,而不是把这次输入丢掉。
  const setTime = (v: string) => onChange(`${datePart || todayKey}T${v || '00:00'}`)
  function clear() { onChange(''); setOpen(false) }

  /**
   * 面板挂到 body 上,用 fixed 定位。
   *
   * 因为这个控件常常长在一个**会裁剪**的容器里 —— 弹窗的表单区是 overflow:auto,
   * 卡片是 overflow:hidden。用绝对定位跟着字段走的话,面板一伸出容器就被切掉,或者
   * 逼出一条本不该有的滚动条(这正是它被提出来的样子)。挂到 body 就没有祖先能裁它。
   *
   * 位置每次打开时算:下方放得下就朝下,放不下就朝上翻;左右夹在视口里,免得贴着右
   * 边缘的字段把面板顶出屏幕。窗口尺寸变化或页面滚动时重算一次 —— fixed 不会自己
   * 跟着走。
   */
  const place = useEvent(() => {
    const f = fieldEl.current
    if (!f) return
    const r = f.getBoundingClientRect()
    const h = popEl.current?.offsetHeight || 320
    const w = popEl.current?.offsetWidth || 252
    const GAP = 6
    const below = window.innerHeight - r.bottom - GAP
    const top = below >= h ? r.bottom + GAP : Math.max(8, r.top - h - GAP)
    const left = Math.min(Math.max(8, r.left), Math.max(8, window.innerWidth - w - 8))
    setPos({ top: `${top}px`, left: `${left}px` })
  })
  useLayoutEffect(() => { if (open) place() }, [open, cursor, place])

  useEffect(() => {
    if (!open) return
    // 点面板外面关掉。面板已经不在 root 里了(portal 走了),所以两处都要问。
    const onDocDown = (e: MouseEvent) => {
      const n = e.target as Node
      if (root.current?.contains(n) || popEl.current?.contains(n)) return
      setOpen(false)
    }
    document.addEventListener('mousedown', onDocDown)
    window.addEventListener('resize', place)
    // capture:内层滚动容器的滚动不冒泡,不捕获就跟不上。
    window.addEventListener('scroll', place, true)
    return () => {
      document.removeEventListener('mousedown', onDocDown)
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open, place])

  return (
    <div {...rest} ref={root} className={clsx('vdt', className)}>
      <button ref={fieldEl} type="button" className={clsx('field', open && 'on')} onClick={toggle}>
        <CalendarDays size={13} />
        <span className={clsx(!display && 'ph')}>{display || placeholder || t('dtPick')}</span>
        {display && <X className="clr" size={12} onClick={(e) => { e.stopPropagation(); clear() }} />}
      </button>
      {open && createPortal(
        // portal 里的点击在 React 树里会冒泡到外层组件:面板不是它的一部分,拦住。
        <div ref={popEl} className="pop" style={pos} onClick={(e) => e.stopPropagation()}>
          <div className="phead">
            <button type="button" className="nav" onClick={() => shiftMonth(-1)}><ChevronLeft size={14} /></button>
            <span className="ym">{`${cursor.getFullYear()}-${pad(cursor.getMonth() + 1)}`}</span>
            <button type="button" className="nav" onClick={() => shiftMonth(1)}><ChevronRight size={14} /></button>
          </div>
          <div className="dows">{[1, 2, 3, 4, 5, 6, 7].map((n) => <span key={n}>{t(`dow${n}`)}</span>)}</div>
          <div className="grid">
            {monthCells(cursor).map((c) => (
              <button
                key={c.key} type="button"
                className={clsx('cell', c.outside && 'out', c.key === datePart && 'sel', c.key === todayKey && 'today')}
                onClick={() => onChange(`${c.key}T${timePart}`)}
              >{c.day}</button>
            ))}
          </div>
          <div className="pfoot">
            {/* 时间仍用原生 time 控件:它弹出来的是一列数字,没有需要翻译的词。 */}
            <input className="tin" type="time" value={timePart} onChange={(e) => setTime(e.target.value)} />
            <button type="button" className="lnk" onClick={() => onChange(`${todayKey}T${timePart}`)}>{t('dtToday')}</button>
            <button type="button" className="lnk" onClick={clear}>{t('dtClear')}</button>
            <button type="button" className="lnk primary" onClick={() => setOpen(false)}>{t('dtDone')}</button>
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
