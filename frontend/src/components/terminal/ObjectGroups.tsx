import { Fragment, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as RMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import { ChevronDown, ChevronRight, SquareFunction, Cog, Package, Zap, FileCode2, Download, ListChecks } from 'lucide-react'
import type { DbObjects } from '@/types'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import './ObjectGroups.scoped.css'

// Programmable-object groups (functions / procedures / packages / triggers)
// under one database or schema node of the terminal tree. Purely presentational:
// the parent owns loading, the source viewer and the download; this renders
// whatever it got and reports which object was clicked.
//
// objects: undefined → the parent never requested this scope (render
// nothing); null → request in flight; loaded → groups (empty kinds are hidden).
interface Props {
  objects: DbObjects | null | undefined
  onOpen: (type: string, name: string) => void
  onDownload: (names: string[]) => void
}

const CATS = [
  { key: 'functions', label: 'treeFunctions', type: 'function', icon: SquareFunction },
  { key: 'procedures', label: 'treeProcedures', type: 'procedure', icon: Cog },
  { key: 'packages', label: 'treePackages', type: 'package', icon: Package },
  { key: 'triggers', label: 'treeTriggers', type: 'trigger', icon: Zap },
] as const

export default function ObjectGroups({ objects, onOpen, onDownload }: Props) {
  const { t } = useI18n()

  function list(key: string): string[] {
    return objects ? ((objects as unknown as Record<string, string[]>)[key] ?? []) : []
  }

  // Per-node collapse state — one component instance per tree node, so plain
  // per-category keys are already scoped correctly.
  const [open, setOpen] = useState<Record<string, boolean>>({})
  function toggle(k: string) { setOpen((o) => ({ ...o, [k]: !o[k] })) }

  // ---- 程序包:多选 + 右键下载 ----
  //
  // 只有程序包有这套交互:下载 .pck 是按"包头 + 包体"成对拼的,别的对象没有这个形状。
  // 点击的习惯与文件管理器一致 —— 单击看源码(原有行为不变),Ctrl/⌘ 单击加减选,
  // Shift 单击连选;右键在已选中的行上作用于整组,在未选中的行上只作用于这一行。
  const [sel, setSel] = useState<Set<string>>(new Set())
  const anchor = useRef('')
  const pkgs = () => list('packages')

  function clickPkg(e: RMouseEvent, name: string) {
    if (e.ctrlKey || e.metaKey) {
      const s = new Set(sel)
      if (s.has(name)) s.delete(name)
      else s.add(name)
      setSel(s)
      anchor.current = name
      return
    }
    if (e.shiftKey && anchor.current) {
      const all = pkgs()
      const a = all.indexOf(anchor.current)
      const b = all.indexOf(name)
      if (a >= 0 && b >= 0) {
        setSel(new Set(all.slice(Math.min(a, b), Math.max(a, b) + 1)))
        return
      }
    }
    setSel(new Set([name]))
    anchor.current = name
    onOpen('package', name)
  }

  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const menuEl = useRef<HTMLDivElement>(null)

  function ctxPkg(e: RMouseEvent, name: string) {
    if (!sel.has(name)) {
      setSel(new Set([name]))
      anchor.current = name
    }
    setMenu({ x: e.clientX, y: e.clientY })
  }
  // 贴着鼠标弹出,但不许越出视口 —— 树在屏幕底部时菜单要往上翻。
  useLayoutEffect(() => {
    const el = menuEl.current
    if (!el || !menu) return
    const r = el.getBoundingClientRect()
    const x = Math.max(4, Math.min(menu.x, window.innerWidth - r.width - 4))
    const y = Math.max(4, Math.min(menu.y, window.innerHeight - r.height - 4))
    if (x !== menu.x || y !== menu.y) setMenu({ x, y })
  }, [menu])

  const closeMenu = useEvent(() => setMenu(null))
  const menuOpen = !!menu
  useEffect(() => {
    if (!menuOpen) return
    const onOutside = (e: MouseEvent) => {
      if (menuEl.current && !menuEl.current.contains(e.target as Node)) closeMenu()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); closeMenu() }
    }
    const onMove = () => closeMenu()
    window.addEventListener('mousedown', onOutside, true)
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('resize', onMove)
    window.addEventListener('scroll', onMove, true)
    return () => {
      window.removeEventListener('mousedown', onOutside, true)
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('resize', onMove)
      window.removeEventListener('scroll', onMove, true)
    }
  }, [menuOpen, closeMenu])

  // 按树上的顺序交出去,文件在 zip 里的顺序与人看到的一致。
  const selected = () => pkgs().filter((n) => sel.has(n))
  function doDownload() {
    const names = selected()
    closeMenu()
    if (names.length) onDownload(names)
  }
  function doView() {
    const [name] = selected()
    closeMenu()
    if (name) onOpen('package', name)
  }
  function doSelectAll() {
    setSel(new Set(pkgs()))
    closeMenu()
  }

  return (
    <>
      {objects === null ? <div className="hint">{t('treeLoading')}</div> : objects ? (
        <>
          {CATS.map((cat) => {
            const items = list(cat.key)
            if (!items.length) return null
            const Chevron = open[cat.key] ? ChevronDown : ChevronRight
            return (
              <Fragment key={cat.key}>
                <div className="cat" onClick={(e) => { e.stopPropagation(); toggle(cat.key) }}>
                  <Chevron size={11} color="var(--text-faint)" />
                  <cat.icon size={12} color="var(--text-faint)" />
                  {' '}{t(cat.label)}<span className="cnt">{items.length}</span>
                </div>
                {open[cat.key] && (
                  <div className="objs">
                    {items.map((o) => (cat.key === 'packages' ? (
                      <div
                        key={o} className={clsx('obj', sel.has(o) && 'on')}
                        title={o + ' · ' + t('pkgRowHint')} data-testid="pkg-row"
                        onClick={(e) => { e.stopPropagation(); clickPkg(e, o) }}
                        onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); ctxPkg(e, o) }}
                      >
                        <FileCode2 size={12} color="var(--text-faint)" /><span className="nm">{o}</span>
                      </div>
                    ) : (
                      <div
                        key={o} className="obj" title={o + ' · ' + t('objViewSource')}
                        onClick={(e) => { e.stopPropagation(); onOpen(cat.type, o) }}
                      >
                        <FileCode2 size={12} color="var(--text-faint)" /><span className="nm">{o}</span>
                      </div>
                    )))}
                  </div>
                )}
              </Fragment>
            )
          })}
          {objects.error && <div className="hint err">{objects.error}</div>}
        </>
      ) : null}

      {menu && createPortal(
        // 事件不许顺着 React 树冒泡回树节点(portal 会)
        <div
          ref={menuEl} className="ctx" role="menu" data-testid="pkg-menu"
          style={{ left: menu.x + 'px', top: menu.y + 'px' }}
          onClick={(e) => e.stopPropagation()}
          onContextMenu={(e) => { e.preventDefault(); e.stopPropagation() }}
        >
          {sel.size === 1 && (
            <button className="ctx-i" role="menuitem" onClick={doView}>
              <FileCode2 size={13} />{t('objViewSource')}
            </button>
          )}
          <button className="ctx-i" role="menuitem" data-testid="pkg-download" onClick={doDownload}>
            <Download size={13} />{sel.size > 1 ? t('pkgDownloadMany', { n: sel.size }) : t('pkgDownloadOne')}
          </button>
          <div className="ctx-sep" />
          <button className="ctx-i" role="menuitem" data-testid="pkg-select-all" onClick={doSelectAll}>
            <ListChecks size={13} />{t('pkgSelectAll', { n: pkgs().length })}
          </button>
        </div>,
        document.body,
      )}
    </>
  )
}
