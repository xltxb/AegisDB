// 数据查询页的 SQL 编辑器:行号、语法着色、当前行高亮。
//
// 做法是一个透明文字的 <textarea> 叠在着色的 <pre> 上 —— 输入、光标、选区、撤销、输入法都是
// 浏览器原生的,着色只是背后那一层画出来的同一段文字。不引编辑器库:这一页只写单条 SELECT,
// 用不上它们的大部分能力,却要多背几百 KB。着色复用 lib/sqlHighlight(与库树里看 DDL 同一个
// 分词器,两处对"什么是关键字"不会有分歧)。
//
// 两层必须逐像素对齐:同一套字体、字号、行高、内边距,都不折行(wrap="off"),滚动时着色层
// 跟着 textarea 走。
import {
  useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState,
  type HTMLAttributes, type KeyboardEvent, type Ref,
} from 'react'
import clsx from 'clsx'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { useMountEffect } from '@/hooks/effects'
import { useEvent } from '@/hooks/useEvent'
import { useStateRef } from '@/hooks/useStateRef'
import './SqlEditor.scoped.css'

// completer:自动补全的候选来源(2026-10-26)。不传就没有补全,编辑器与之前完全一样。
// 返回光标前要替换掉的字符数和候选(候选就是要插入的文本,已按需加好引号)。
export type SqlCompleter = (text: string, cursor: number) => Promise<{ replace: number; items: string[] } | null>

export interface SqlEditorHandle {
  focus: () => void
  readonly el: HTMLTextAreaElement | null
}

// 其余属性(data-testid 等)落到 textarea 上
interface Props extends Omit<HTMLAttributes<HTMLTextAreaElement>, 'onChange'> {
  ref?: Ref<SqlEditorHandle>
  value: string
  placeholder?: string
  completer?: SqlCompleter
  onChange: (v: string) => void
  onRun?: () => void
  onFormat?: () => void
  onCursor?: (c: { line: number; col: number }) => void
}

interface AcState { items: string[]; idx: number; replace: number; left: number; top: number; up: boolean }

const LH = 21
const PLAIN_LINES = 3000
const PLAIN_CHARS = 200_000
const AC_MAX = 100
const AC_ROW = 26

export default function SqlEditor({ ref, value, placeholder, completer, onChange, onRun, onFormat, onCursor, ...rest }: Props) {
  const ta = useRef<HTMLTextAreaElement>(null)
  const hl = useRef<HTMLPreElement>(null)
  const gutter = useRef<HTMLDivElement>(null)
  const [line, setLine] = useState(1)
  const [scrollTop, setScrollTop] = useState(0)

  const lineCount = useMemo(() => Math.max(1, value.split('\n').length), [value])
  // 行号只画看得见的那一段(上下用占位撑出总高度):每行一个元素时,几千行的 SQL 每敲一个键
  // 都要比对几千个节点(2026-10-26 回归排查)。LH 与样式里的 --se-lh 一致。
  const [viewH, setViewH] = useState(1200)
  const start = Math.max(0, Math.floor(scrollTop / LH) - 5)
  const end = Math.min(lineCount, start + Math.ceil(viewH / LH) + 10)
  const lines = Array.from({ length: end - start }, (_, k) => start + k + 1)
  // 末尾补一个换行:最后一行是空行时 <pre> 不给它高度,两层就差一行
  // 超大文本退回素文本:着色层几十万个节点光排版就要几百毫秒,每敲一个键都卡。这时 textarea 的
  // 文字直接显示(与改版前的普通输入框一样快),着色层不画。
  const plain = value.length > PLAIN_CHARS || lineCount > PLAIN_LINES
  const html = useMemo(() => ({ __html: plain ? '' : highlightSqlHtml(value) + '\n' }), [plain, value])

  // textarea 不做成 React 受控的:受控会在输入法组合、外部改值时与浏览器抢光标。只在文字真的与
  // value 不一样(父组件改了它:切页签、格式化、插入表名)时写回去,用户自己敲的那次两边本来就相等。
  useLayoutEffect(() => {
    const el = ta.current
    if (el && el.value !== value) el.value = value
  })

  function syncScroll() {
    const el = ta.current
    if (!el) return
    if (acRef.current) closeAc() // 弹框跟着光标位置算,滚动后就不准了
    setScrollTop(el.scrollTop)
    if (el.clientHeight) setViewH(el.clientHeight)
    if (hl.current) {
      hl.current.scrollTop = el.scrollTop
      hl.current.scrollLeft = el.scrollLeft
    }
    if (gutter.current) gutter.current.scrollTop = el.scrollTop
  }

  // 拖大分割条 / 放大窗口时编辑器变高:行号可见段跟着变,不必等滚动或按键
  useMountEffect(() => {
    const el = ta.current
    if (el?.clientHeight) setViewH(el.clientHeight)
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => { if (ta.current?.clientHeight) setViewH(ta.current.clientHeight) })
    ro.observe(el)
    return () => ro.disconnect()
  })

  const updateCursor = useEvent(() => {
    const el = ta.current
    if (!el) return
    if (el.clientHeight) setViewH(el.clientHeight)
    const before = el.value.slice(0, el.selectionStart)
    const l = before.split('\n').length
    const col = before.length - before.lastIndexOf('\n')
    setLine(l)
    onCursor?.({ line: l, col })
  })

  const onInput = useEvent((e: Event) => {
    onChange((e.target as HTMLTextAreaElement).value)
    updateCursor()
    // 敲的是标识符字符或点:稍等一下再取候选(连续打字时只取最后一次);删字时弹框开着就跟着更新
    const ie = e as InputEvent
    if (!completer) return
    if (ie.inputType === 'insertText' && ie.data && /[\p{L}\p{N}_$.]$/u.test(ie.data)) scheduleComplete()
    else if (acRef.current && ie.inputType?.startsWith('delete')) scheduleComplete()
    else closeAc()
  })
  // 用原生的 input / select 事件:React 的 onChange 会按自己记的值去重,onSelect 是它合成的另一套时机
  useEffect(() => {
    const el = ta.current!
    el.addEventListener('input', onInput)
    el.addEventListener('select', updateCursor)
    return () => {
      el.removeEventListener('input', onInput)
      el.removeEventListener('select', updateCursor)
    }
  }, [onInput, updateCursor])

  // ---- 自动补全弹框 ----
  //
  // 只在弹框开着的时候接管 ↑ ↓ Tab Enter Esc;关着时按键与之前完全一样(Tab 缩进、Esc→Tab 移走焦点、
  // Ctrl+Enter 执行、Shift+Alt+F 格式化)。敲完的词已经和唯一候选一模一样时不弹 —— 否则敲完 SELECT
  // 按回车想换行,会被补全吃掉。Ctrl+Space 手动唤出。
  const [ac, setAc, acRef] = useStateRef<AcState | null>(null)
  const acList = useRef<HTMLDivElement>(null)
  const acTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const acSeq = useRef(0)
  const charW = useRef(0)
  function closeAc() { setAc(null); clearTimeout(acTimer.current); acSeq.current++ }
  function scheduleComplete() {
    clearTimeout(acTimer.current)
    acTimer.current = setTimeout(() => requestComplete(false), 120)
  }
  function measureCharW(el: HTMLTextAreaElement): number {
    if (charW.current) return charW.current
    const c = document.createElement('canvas').getContext('2d')
    if (!c) return 7.8
    const cs = getComputedStyle(el)
    c.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`
    charW.current = c.measureText('MMMMMMMMMM').width / 10 || 7.8
    return charW.current
  }
  const requestComplete = useEvent(async (manual: boolean) => {
    const el = ta.current
    const fn = completer
    if (!el || !fn || el.selectionStart !== el.selectionEnd) { closeAc(); return }
    const seq = ++acSeq.current
    const text = el.value
    const pos = el.selectionStart
    let res: Awaited<ReturnType<SqlCompleter>> = null
    try { res = await fn(text, pos) } catch { res = null }
    // 等候选的时候人又动了:作废
    if (seq !== acSeq.current || el.value !== text || el.selectionStart !== pos) return
    const items = (res?.items || []).slice(0, AC_MAX)
    const prefix = text.slice(pos - (res?.replace || 0), pos)
    if (!res || !items.length || (!manual && items.length === 1 && items[0].toLowerCase() === prefix.toLowerCase())) {
      setAc(null)
      return
    }
    // 弹框放在光标下一行;下面放不下就放到上面
    const before = text.slice(0, pos)
    const lineIdx = before.split('\n').length - 1
    const col = before.length - before.lastIndexOf('\n') - 1
    const cs = getComputedStyle(el)
    const padL = parseFloat(cs.paddingLeft) || 14
    const padT = parseFloat(cs.paddingTop) || 12
    const left = Math.max(4, padL + col * measureCharW(el) - el.scrollLeft)
    const lineTop = padT + lineIdx * LH - el.scrollTop
    const h = Math.min(items.length, 8) * AC_ROW + 8
    const up = lineTop + LH + h > el.clientHeight && lineTop - h > 0
    setAc({ items, idx: 0, replace: res.replace, left, top: up ? lineTop - h : lineTop + LH, up })
  })
  function acAccept(i = acRef.current?.idx ?? 0) {
    const el = ta.current
    const st = acRef.current
    if (!el || !st) return
    const item = st.items[i]
    const pos = el.selectionStart
    const from = Math.max(0, pos - st.replace)
    const v = el.value.slice(0, from) + item + el.value.slice(pos)
    closeAc()
    onChange(v)
    requestAnimationFrame(() => { el.focus(); el.setSelectionRange(from + item.length, from + item.length); updateCursor() })
  }
  function acMove(d: number) {
    const st = acRef.current
    if (!st) return
    const idx = (st.idx + d + st.items.length) % st.items.length
    setAc({ ...st, idx })
    requestAnimationFrame(() => acList.current?.children[idx]?.scrollIntoView({ block: 'nearest' }))
  }
  // 弹框开着时的按键;接管了返回 true
  function acKeydown(e: KeyboardEvent): boolean {
    if (!acRef.current) return false
    if (e.ctrlKey || e.metaKey || e.altKey) { closeAc(); return false } // Ctrl+Enter 等:关掉弹框,照常处理
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); acMove(1); return true
      case 'ArrowUp': e.preventDefault(); acMove(-1); return true
      case 'Enter':
      case 'Tab':
        if (e.shiftKey) { closeAc(); return false }
        e.preventDefault(); acAccept(); return true
      case 'Escape': e.preventDefault(); e.stopPropagation(); closeAc(); return true
      case 'ArrowLeft': case 'ArrowRight': case 'Home': case 'End': case 'PageUp': case 'PageDown':
        closeAc(); return false
    }
    return false
  }
  useMountEffect(() => () => clearTimeout(acTimer.current))

  // 按过 Esc 之后的那一下 Tab 照常移走焦点(与 VS Code / CodeMirror 同一个约定):Tab 被拿来缩进后,
  // 纯键盘用户原本没法从编辑器 Tab 到「执行」按钮(2026-10-26 回归排查)。
  const tabEscapes = useRef(false)

  function onKeydown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (acKeydown(e)) return
    if (completer && e.ctrlKey && (e.key === ' ' || e.code === 'Space')) {
      e.preventDefault()
      void requestComplete(true)
      return
    }
    if (e.key === 'Escape') { tabEscapes.current = true; return }
    const escaped = tabEscapes.current
    tabEscapes.current = false
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault()
      onRun?.()
      return
    }
    // Shift + Alt + F:格式化(与 VS Code 同一个键)
    if (e.shiftKey && e.altKey && (e.key === 'F' || e.key === 'f')) {
      e.preventDefault()
      onFormat?.()
      return
    }
    // Tab 插两个空格,而不是把焦点移走 —— 写 SQL 时缩进比跳到下一个按钮常用得多
    if (e.key === 'Tab' && !escaped && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
      const el = ta.current
      if (!el) return
      e.preventDefault()
      const s = el.selectionStart
      const selEnd = el.selectionEnd
      const v = el.value.slice(0, s) + '  ' + el.value.slice(selEnd)
      onChange(v)
      requestAnimationFrame(() => { el.setSelectionRange(s + 2, s + 2); updateCursor() })
    }
  }

  useImperativeHandle(ref, () => ({
    focus: () => ta.current?.focus(),
    get el() { return ta.current },
  }), [])

  return (
    <div className={clsx('se', plain && 'plain')}>
      <div ref={gutter} className="se-gutter" aria-hidden="true">
        <div style={{ height: `${start * LH}px` }} />
        {lines.map((n) => <div key={n} className={clsx('se-ln', n === line && 'on')}>{n}</div>)}
        <div style={{ height: `${(lineCount - end) * LH}px` }} />
      </div>
      <div className="se-main">
        {/* 当前行高亮:跟着光标所在行,随滚动平移 */}
        <div className="se-curline" style={{ transform: `translateY(${(line - 1) * LH - scrollTop}px)` }} />
        <pre ref={hl} className="se-hl" aria-hidden="true" dangerouslySetInnerHTML={html} />
        <textarea
          {...rest}
          ref={ta}
          className="se-input"
          placeholder={placeholder}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          wrap="off"
          onKeyDown={onKeydown}
          onKeyUp={updateCursor}
          onClick={() => { updateCursor(); closeAc() }}
          onFocus={updateCursor}
          onBlur={closeAc}
          onScroll={syncScroll}
        />
        {/* 自动补全弹框:mousedown 阻止默认让焦点留在 textarea 上,点选不会先触发 blur 关掉它 */}
        {ac && (
          <div
            ref={acList} className="se-ac" role="listbox" data-testid="sql-ac"
            style={{ left: `${ac.left}px`, top: `${ac.top}px`, maxHeight: `${8 * AC_ROW + 8}px` }}
          >
            {ac.items.map((it, i) => (
              <div
                key={it + i} className={clsx('se-ac-item', i === ac.idx && 'on')} role="option" aria-selected={i === ac.idx}
                onMouseDown={(e) => e.preventDefault()} onClick={() => acAccept(i)}
                onMouseEnter={() => { if (acRef.current) setAc({ ...acRef.current, idx: i }) }}
              >{it}</div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
