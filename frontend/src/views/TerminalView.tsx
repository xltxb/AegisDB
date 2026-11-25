// AppLayout 把这个视图放在 <KeepAlive> 里常驻:它的标签、xterm 会话和连接,不因为点了别的菜单就没了。
import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent as RPointerEvent } from 'react'
import clsx from 'clsx'
import {
  X, PanelLeftOpen, PanelRightOpen, Table2, Maximize2, Minimize2, Activity, ShieldAlert, LogOut,
} from 'lucide-react'
import { useUIStore } from '@/stores/ui'
import { useEnvTierStore } from '@/stores/envtier'
import { useActivated } from '@/components/KeepAlive'
import DbTree from '@/components/terminal/DbTree'
import RiskInspector from '@/components/terminal/RiskInspector'
import TerminalSession from '@/components/terminal/TerminalSession'
import ResultGrid from '@/components/terminal/ResultGrid'
import api from '@/api'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection, Member, RiskCommandView } from '@/types'
import type { WsStatus } from '@/lib/wsTerminal'
import './TerminalView.scoped.css'

// One tab per open connection; each renders an isolated <TerminalSession>.
// more / loadMore:结果分批显示时,表格底部的「加载更多」(由 TerminalSession 取下一批)
interface GridResult { columns: string[]; rows: string[][]; more?: boolean; loadMore?: () => Promise<void> }
interface Tab { id: number; conn: Connection; db: string; risk: 'idle' | 'safe' | 'high'; wsStatus: WsStatus; result?: GridResult }

// ---- 两侧边栏的宽度:可拖 ----
//
// 默认宽度由样式里的媒体查询给,那套逐级让位的规则(先收窄执行上下文、再整块收起、
// 最后连树也收成导轨)是为了"窗口一窄,被挤没的不能是终端本身"。拖动不推翻它,
// 只是把某一侧的宽度换成人自己定的那个值:CSS 里每一处都写成 var(--tree-w, 268px),
// 没拖过就走默认,拖过了就在每个断点上都听人的。
//
// 存像素而不是百分比 —— 这跟终端/结果表那根横向分隔条相反,是故意的:树和执行上下文
// 装的是**定宽的东西**(实例名、字段标签),它们需要的宽度不随窗口变;而结果表要占
// 的是"剩下的一半",所以那边存比例。
const TREE_W_KEY = 'aegisdb_tree_w'
const INSP_W_KEY = 'aegisdb_insp_w'
const TREE_MIN = 180
const TREE_MAX = 560
const INSP_MIN = 240
const INSP_MAX = 620
// 中间那栏的下限。拖动可以把两侧拉宽,但不能把终端挤到开始逐字断行 —— 这正是
// 媒体查询那一串在防的事,手动拖动没有理由成为它的后门。
const TERM_MIN = 420

const readW = (k: string) => {
  const v = Number(localStorage.getItem(k))
  return Number.isFinite(v) && v > 0 ? v : null
}

type Side = 'tree' | 'insp'

// ---- 终端 / 结果表格的上下分割 ----
//
// 表格视图打开后,屏幕要同时放下两样东西:上面是命令和它的回显,下面是结果表。
// 该给谁多少,取决于当下在做什么 —— 核对一列数据时想把表拉大,顺着报错往回翻
// 时想把终端拉大 —— 这不是能替用户定死的比例,所以给一根可拖的分隔条。
//
// 存的是百分比而不是像素:换一块屏幕、或把浏览器窗口拖成另一个高度时,按比例
// 还原仍然合理,按像素还原会在小屏上把终端压没。
const GRID_H_KEY = 'aegisdb_termgrid_h'
const GRID_H_DEFAULT = 42
const GRID_H_MIN = 15
const GRID_H_MAX = 80
const clampH = (v: number) => Math.min(GRID_H_MAX, Math.max(GRID_H_MIN, v))

export default function TerminalView() {
  const { t } = useI18n()
  const envtier = useEnvTierStore()

  // Shared, fetched once and passed down to every session.
  const [conns, setConns, connsRef] = useStateRef<Connection[]>([])
  const [riskCommands, setRiskCommands] = useState<RiskCommandView[]>([])
  const [chain, setChain] = useState<Member[]>([])
  const [scriptEnabled, setScriptEnabled] = useState(false)
  const [scriptSavePath, setScriptSavePath] = useState('')

  const [tabs, setTabs, tabsRef] = useStateRef<Tab[]>([])
  const [activeId, setActiveId, activeIdRef] = useStateRef(0)
  const seq = useRef(0)

  // 窄屏(≤767px,手机宽度;与样式里"树收成导轨"那一档同一个断点):树默认收起,展开时以抽屉
  // 浮在终端上面,而不是挤在 30px 的导轨列里把终端顶出屏幕(黑盒 2026-10 工单 05:手机宽度下
  // 实例树占满整屏)。选中实例后抽屉自动收起,把位置还给终端;选库不收 —— 选了库多半还要接着看表。
  // 收起时树不卸载(只是不显示),展开状态还在。
  //
  // 断点原先是 1080px:半屏(960px 左右)的窗口里树从一直可见变成了每次都要点开的抽屉,点库名就收起,
  // 收起一次展开状态就丢(2026-10-26 回归排查)。768–1080 恢复成树嵌在页面里、可收成导轨。
  const [narrowMq] = useState(() => (typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(max-width: 767px)') : null))
  const [narrow, setNarrow] = useState(!!narrowMq?.matches)
  const [treeCollapsed, setTreeCollapsed] = useState(narrow) // collapse the left database-tree panel
  useEffect(() => {
    function onNarrowChange(e: MediaQueryListEvent) {
      setNarrow(e.matches)
      if (e.matches) setTreeCollapsed(true)
    }
    narrowMq?.addEventListener('change', onNarrowChange)
    return () => narrowMq?.removeEventListener('change', onNarrowChange)
  }, [narrowMq])
  function closeDrawer() { if (narrow) setTreeCollapsed(true) }
  const onTreeSelect = useEvent((id: number) => { openConn(id); closeDrawer() })
  const onTreeSelectDb = useEvent((id: number, db: string) => { openDb(id, db) })
  const onTreeCollapse = useEvent(() => setTreeCollapsed(true))
  // 右侧执行上下文**默认收起**。它是辅助信息(风险判定的结论终端里照样会打印),而
  // 终端本身不可替代 —— 要对着一屏宽结果核数据时,这 340px 让出来最值。
  //
  // 判的是 !== '0' 而不是 === '1':没存过 = 收起(新的默认),而**明确展开过**的人
  // 存的是 '0',那份选择继续算数。把默认写成"没存过就展开"才会把人的选择吃掉。
  const [inspCollapsed, setInspCollapsed] = useState(() => localStorage.getItem('aegisdb_insp_collapsed') !== '0')
  function toggleInsp() {
    const v = !inspCollapsed
    setInspCollapsed(v)
    localStorage.setItem('aegisdb_insp_collapsed', v ? '1' : '0')
  }

  const [treeW, setTreeW, treeWRef] = useStateRef<number | null>(() => readW(TREE_W_KEY))
  const [inspW, setInspW, inspWRef] = useStateRef<number | null>(() => readW(INSP_W_KEY))
  const gridEl = useRef<HTMLDivElement>(null)

  // 只有拖过的那一侧才写变量;没写的那侧留给 CSS 默认值(含媒体查询)。
  const gridVars: Record<string, string> = {}
  if (treeW) gridVars['--tree-w'] = `${treeW}px`
  if (inspW) gridVars['--insp-w'] = `${inspW}px`

  const [resizing, setResizing, resizingRef] = useStateRef<Side | null>(null)

  /** 拖动时那一侧的上限:先受自身上限约束,再受"中间必须留够"约束。 */
  function maxFor(side: Side): number {
    const el = gridEl.current
    const hard = side === 'tree' ? TREE_MAX : INSP_MAX
    if (!el) return hard
    // 另一侧此刻**实际渲染**的宽度,直接从解析后的列宽读,不去猜它落在哪个断点上。
    const cols = getComputedStyle(el).gridTemplateColumns.split(' ').map(parseFloat)
    const other = side === 'tree' ? (cols[2] ?? 0) : (cols[0] ?? 0)
    return Math.min(hard, el.getBoundingClientRect().width - other - TERM_MIN)
  }

  function setW(side: Side, px: number) {
    const min = side === 'tree' ? TREE_MIN : INSP_MIN
    const v = Math.round(Math.min(maxFor(side), Math.max(min, px)))
    if (side === 'tree') { setTreeW(v); localStorage.setItem(TREE_W_KEY, String(v)) }
    else { setInspW(v); localStorage.setItem(INSP_W_KEY, String(v)) }
  }

  /** 双击 / 回车:还原成默认宽度,也就是把这一侧交还给媒体查询。 */
  function resetW(side: Side) {
    if (side === 'tree') { setTreeW(null); localStorage.removeItem(TREE_W_KEY) }
    else { setInspW(null); localStorage.removeItem(INSP_W_KEY) }
  }

  // 指针事件 + setPointerCapture,跟结果表那根分隔条同一套:一套代码覆盖鼠标、
  // 触控板和触摸屏,并且指针拖出把手之外仍然跟手 —— 拖动时它几乎一定会跑出去。
  function onEdgeDown(side: Side, e: RPointerEvent<HTMLElement>) {
    setResizing(side)
    e.currentTarget.setPointerCapture(e.pointerId)
    e.preventDefault()
  }
  function onEdgeMove(side: Side, e: RPointerEvent<HTMLElement>) {
    if (resizingRef.current !== side || !gridEl.current) return
    const box = gridEl.current.getBoundingClientRect()
    setW(side, side === 'tree' ? e.clientX - box.left : box.right - e.clientX)
  }
  function onEdgeUp(e: RPointerEvent<HTMLElement>) {
    if (!resizingRef.current) return
    setResizing(null)
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }

  // 键盘也能调:把手是可聚焦的 separator。没拖过时先从当前实际宽度起步,
  // 否则第一次按键会把面板跳到某个凭空的数字上。
  function onEdgeKey(side: Side, e: KeyboardEvent) {
    const step = e.shiftKey ? 32 : 8
    const cur = side === 'tree' ? treeWRef.current : inspWRef.current
    let base = cur
    if (base == null && gridEl.current) {
      const cols = getComputedStyle(gridEl.current).gridTemplateColumns.split(' ').map(parseFloat)
      base = side === 'tree' ? cols[0] : cols[2]
    }
    if (base == null || !Number.isFinite(base)) return
    const grow = side === 'tree' ? 'ArrowRight' : 'ArrowLeft'
    const shrink = side === 'tree' ? 'ArrowLeft' : 'ArrowRight'
    if (e.key === grow) setW(side, base + step)
    else if (e.key === shrink) setW(side, base - step)
    else if (e.key === 'Enter' || e.key === ' ') resetW(side)
    else return
    e.preventDefault()
  }

  // 窗口变窄后,拖出来的宽度可能已经把终端挤过了下限 —— 重新夹一次。存着的值不动:
  // 窗口再拉回来时,人自己设的那个宽度应该回来。
  const reclamp = useEvent(() => {
    if (treeWRef.current) setTreeW(Math.round(Math.min(treeWRef.current, Math.max(TREE_MIN, maxFor('tree')))))
    if (inspWRef.current) setInspW(Math.round(Math.min(inspWRef.current, Math.max(INSP_MIN, maxFor('insp')))))
  })
  useEffect(() => {
    window.addEventListener('resize', reclamp)
    return () => window.removeEventListener('resize', reclamp)
  }, [reclamp])

  // 沉浸模式:把左右两栏一起收掉,只留终端。不是浏览器全屏 —— 那会把顶栏和导航
  // 一起吞掉,而人还需要知道自己在哪个系统里。
  const [zen, setZen] = useState(false)
  // HTML result-grid panel: results render in a real table (horizontal scroll,
  // select/copy) instead of an ASCII table in the terminal. Persisted per browser.
  const [gridView, setGridView] = useState(() => localStorage.getItem('aegisdb_termgrid') === '1')
  function toggleGrid() {
    const v = !gridView
    setGridView(v)
    localStorage.setItem('aegisdb_termgrid', v ? '1' : '0')
  }

  const [gridH, setGridHState, gridHRef] = useStateRef(() => clampH(Number(localStorage.getItem(GRID_H_KEY)) || GRID_H_DEFAULT))
  const [dragging, setDragging, draggingRef] = useStateRef(false)
  const splitEl = useRef<HTMLDivElement>(null)

  function setGridH(v: number) {
    setGridHState(clampH(v))
    localStorage.setItem(GRID_H_KEY, String(Math.round(gridHRef.current)))
  }

  // 指针事件而不是 mousedown/mousemove:同一套代码覆盖鼠标、触控板和触摸屏,
  // 并且 setPointerCapture 让指针拖出分隔条之外也照样跟手 —— 拖动时指针几乎
  // 一定会跑到条外面去。
  function onSplitDown(e: RPointerEvent<HTMLElement>) {
    const host = splitEl.current?.parentElement
    if (!host) return
    setDragging(true)
    e.currentTarget.setPointerCapture(e.pointerId)
    e.preventDefault()
  }
  function onSplitMove(e: RPointerEvent<HTMLElement>) {
    if (!draggingRef.current) return
    const host = splitEl.current?.parentElement
    if (!host) return
    const box = host.getBoundingClientRect()
    if (box.height <= 0) return
    // 结果表在下面,所以它的高度是"容器底边减去指针位置"。
    setGridH(((box.bottom - e.clientY) / box.height) * 100)
  }
  function onSplitUp(e: RPointerEvent<HTMLElement>) {
    if (!draggingRef.current) return
    setDragging(false)
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }

  // 键盘也能调。分隔条是可聚焦的 separator,方向键一次 2%,Home/End 到两端,
  // 双击或回车恢复默认 —— 拖到一个别扭的比例后,不必再用鼠标一点点试回来。
  function onSplitKey(e: KeyboardEvent) {
    const step = e.shiftKey ? 8 : 2
    if (e.key === 'ArrowUp') setGridH(gridHRef.current + step)
    else if (e.key === 'ArrowDown') setGridH(gridHRef.current - step)
    else if (e.key === 'Home') setGridH(GRID_H_MAX)
    else if (e.key === 'End') setGridH(GRID_H_MIN)
    else if (e.key === 'Enter' || e.key === ' ') setGridH(GRID_H_DEFAULT)
    else return
    e.preventDefault()
  }

  const activeTab = tabs.find((x) => x.id === activeId) || null

  // 当前会话的安全等级。触发条件是**分层的 dangerBanner**,不是名字叫不叫 prod ——
  // 第二套生产环境和第一套一样危险,而按名字挑会让人最不熟悉的那些集群拿到最弱的
  // 警告。这和终端里那条红线、进入实例时那个弹窗用的是同一个判据。
  const safety = (() => {
    const cn = activeTab?.conn
    if (!cn) return null
    const tier = envtier.tierOf(cn.env)
    // 解析不出分层的按中档处理:没有分层意味着这台实例的管控级别**未知**,不是无害。
    const level = tier?.dangerBanner ? 'danger' : (!tier || tier.requireMfa) ? 'warn' : 'ok'
    return { level, env: cn.env.toUpperCase(), name: cn.name, role: cn.defaultRole }
  })()

  const patchTab = (id: number, p: Partial<Tab>) => setTabs((l) => l.map((x) => (x.id === id ? { ...x, ...p } : x)))
  const setResult = (id: number, r: GridResult) => patchTab(id, { result: r })

  const syncPageSub = useEvent(() => {
    const tb = tabsRef.current.find((x) => x.id === activeIdRef.current)
    useUIStore.getState().setPageSub(tb ? `${tb.conn.env}-${tb.conn.name} · ${tb.conn.defaultRole}` : '')
  })
  // 按 id 盯,不按标签对象:标签的连接状态、风险一变对象就换一个,而这个视图切走后还活着 ——
  // 那时改副标题会改到别的页面头上。
  useUpdateEffect(syncPageSub, [activeTab?.id])
  // Refresh the instance list whenever the (kept-alive) view is re-shown, so a
  // connection added on the Connections page appears without a full reload.
  async function loadConns() {
    try {
      const l = await api.connections()
      setConns(Array.isArray(l) ? l : [])
    } catch { /* ignore */ }
  }
  useActivated(() => { syncPageSub(); void loadConns() })

  function addTab(conn: Connection, db: string) {
    const tab: Tab = { id: ++seq.current, conn, db, risk: 'idle', wsStatus: 'connecting' }
    setTabs((l) => [...l, tab])
    setActiveId(tab.id)
  }

  // Open (or focus) a tab for a connection. One tab per connection.
  function openConn(id: number, db = '') {
    const existing = tabsRef.current.find((x) => x.conn.id === id)
    if (existing) { if (db) patchTab(existing.id, { db }); setActiveId(existing.id); return }
    const cn = connsRef.current.find((x) => x.id === id)
    if (!cn) return
    addTab(cn, db || cn.database || '')
  }
  // Selecting a database in the tree opens/focuses its instance tab on that db.
  function openDb(id: number, db: string) { openConn(id, db) }

  // 双击标签栏空白处:再开一个**当前实例、当前库**的会话。
  //
  // 这是 openConn 之外唯一会产生"同一实例第二个标签"的入口。openConn 按实例去重是对的
  // —— 树上点一下不该每次都多一个标签;而这里是人明确要第二个会话:一边跑着长查询,
  // 另一边看别的表。每个标签自带独立 WebSocket,两个会话互不牵连。
  // 没有活动标签时什么都不做:没有可复制的会话,空态提示已经在说"先从左侧选实例"。
  function newTab() {
    const cur = tabsRef.current.find((x) => x.id === activeIdRef.current)
    if (!cur) return
    addTab(cur.conn, cur.db)
  }
  // 同一实例开了不止一个标签时,标题后面跟一个序号 —— 否则两个标签长得一模一样,
  // 而这正是标签标题要"写清是哪一个"的理由。只有一个时不显示,别给唯一的那个也编号。
  function tabOrdinal(tab: Tab): number {
    const same = tabs.filter((x) => x.conn.id === tab.conn.id && x.db === tab.db)
    return same.length > 1 ? same.indexOf(tab) + 1 : 0
  }

  function closeTab(id: number) {
    const idx = tabsRef.current.findIndex((x) => x.id === id)
    if (idx === -1) return
    const left = tabsRef.current.filter((x) => x.id !== id)
    setTabs(left)
    if (activeIdRef.current === id) {
      const next = left[idx] || left[idx - 1] || null
      setActiveId(next ? next.id : 0)
    }
  }

  useMountEffect(() => {
    void (async () => {
      // Fetch independently: a failure of one (e.g. a non-admin lacking access to
      // the risk dictionary) must NOT prevent the connection list from loading.
      await loadConns()
      try {
        const l = await api.riskCommands()
        setRiskCommands(Array.isArray(l) ? l : [])
      } catch { /* optional reference data */ }
      try { setChain((await api.approvalChain()).chain ?? []) } catch { /* ignore */ }
      try { const sc = await api.scriptConfig(); setScriptEnabled(sc.enabled); setScriptSavePath(sc.savePath) } catch { /* ignore */ }
      // 进来只是进页面,不替人挑实例、不自动建连(2026-10 用户要求)。从前默认打开 prod(或第一个)
      // 实例的标签 —— 每个标签独占一条数据库连接,人只是路过终端页,也在生产库上占了一条连接;
      // 而且默认落在 prod 上,一不留神就在生产执行。空态提示会请人从左侧选实例。
    })()
  })

  const treeProps = {
    connections: conns, selectedId: activeTab?.conn.id || 0, selectedDb: activeTab?.db,
    onSelect: onTreeSelect, onSelectDb: onTreeSelectDb, onCollapse: onTreeCollapse,
  }
  const drawerStyle: CSSProperties | undefined = treeCollapsed || zen ? { display: 'none' } : undefined
  const ZenIcon = zen ? Minimize2 : Maximize2
  const result = activeTab?.result

  return (
    <div
      ref={gridEl}
      className={clsx('grid', treeCollapsed && 'tree-collapsed', inspCollapsed && 'insp-collapsed', zen && 'zen', resizing && 'resizing')}
      style={gridVars as CSSProperties}
    >
      {(treeCollapsed || zen || narrow) && (
        <div className="rail left" title={t('treeExpand')} data-testid="tree-rail" onClick={() => { setTreeCollapsed(false); setZen(false) }}><PanelLeftOpen size={16} /></div>
      )}
      {narrow ? (
        <>
          <div style={drawerStyle} className="drawer-mask" data-testid="tree-drawer-mask" onClick={() => setTreeCollapsed(true)} />
          <DbTree style={drawerStyle} className="drawer" {...treeProps} />
        </>
      ) : !treeCollapsed && !zen && <DbTree {...treeProps} />}

      <div className="term">
        {/* 两根竖把手。它们贴在终端栏的左右内边缘,而不是自己占一列:多一列就要在
            样式里每一条媒体查询里多写一个数,而那串规则本来就是这个视图里最容易写歪的
            地方。收起的那一侧不渲染把手 —— 30px 的导轨没有宽度可调。 */}
        {!treeCollapsed && !zen && (
          <div
            className="edge left" role="separator" aria-orientation="vertical"
            tabIndex={0} title={t('resizePanel')}
            onPointerDown={(e) => onEdgeDown('tree', e)} onPointerMove={(e) => onEdgeMove('tree', e)}
            onPointerUp={onEdgeUp} onPointerCancel={onEdgeUp}
            onDoubleClick={() => resetW('tree')} onKeyDown={(e) => onEdgeKey('tree', e)}
          ><span className="grip" /></div>
        )}
        {!inspCollapsed && !zen && (
          <div
            className="edge right" role="separator" aria-orientation="vertical"
            tabIndex={0} title={t('resizePanel')}
            onPointerDown={(e) => onEdgeDown('insp', e)} onPointerMove={(e) => onEdgeMove('insp', e)}
            onPointerUp={onEdgeUp} onPointerCancel={onEdgeUp}
            onDoubleClick={() => resetW('insp')} onKeyDown={(e) => onEdgeKey('insp', e)}
          ><span className="grip" /></div>
        )}
        {/* 第一行:标签页。一个标签 = 一个会话,标题写清"哪台实例的哪个库" —— 只写
            实例名时,同一台上开两个库的两个标签长得一模一样。 */}
        <div className="tabstrip">
          {tabs.map((tab) => {
            const ord = tabOrdinal(tab)
            return (
              <div
                key={tab.id}
                className={clsx('tab', tab.id === activeId && 'active')}
                onClick={() => setActiveId(tab.id)}
              >
                <span className={clsx('dot', tab.wsStatus !== 'open' && 'off')} />
                <span className="tname">{tab.conn.name}{tab.db && <span className="tdb">: {tab.db}</span>}{ord > 0 && <span className="tord">· {ord}</span>}</span>
                <span className="close" title={t('tabClose')} onClick={(e) => { e.stopPropagation(); closeTab(tab.id) }}><X size={12} /></span>
              </div>
            )
          })}
          {/* 空白处双击开新标签:和终端/浏览器一致的习惯。title 只在有会话可复制时给,
              否则悬停提示"双击新建"却什么都不发生,是在骗人。 */}
          <div className="tabspacer" title={activeTab ? t('tabNew') : ''} onDoubleClick={newTab} />
          {/* 右侧是**这个会话**的连接状态。网关整体的 p50 延迟顶栏上已经有了,
              在这里再放一份是同一个数字说两遍。 */}
          {activeTab && (
            <span className={clsx('wschip', activeTab.wsStatus)}>
              <Activity size={12} />{t('wsStatus_' + activeTab.wsStatus)}
            </span>
          )}
          <button className={clsx('tbtn', gridView && 'on')} title={t('gridToggle')} onClick={toggleGrid}><Table2 size={14} />{t('gridToggle')}</button>
          <button className="tbtn" title={zen ? t('zenExit') : t('zenEnter')} onClick={() => setZen(!zen)}>
            <ZenIcon size={14} />
          </button>
        </div>

        {/* 生产安全横幅。它取代了原先打在终端里的那一行红字 —— 那行字会被滚屏
            顶走,而"你正在生产库上"这件事不该只在会话开头说一次。 */}
        {safety && safety.level !== 'ok' && (
          <div className={clsx('safety', safety.level)}>
            <ShieldAlert size={15} className="sbi" />
            <span className="sbtxt">
              <b>{safety.level === 'danger' ? t('bannerProd') : t('bannerCaution')}</b>
              <span className="sbsep">·</span>{t('bannerInst')} <code>{safety.env} / {safety.name}</code>
              <span className="sbsep">·</span>{t('bannerRole')} <code>{safety.role}</code>
              <span className="sbsep">·</span>{t('bannerAudit')}
            </span>
            <button className="sbbtn" title={t('bannerEnd')} onClick={() => { if (activeTab) closeTab(activeTab.id) }}>
              <LogOut size={13} />{t('bannerEnd')}
            </button>
          </div>
        )}

        {!tabs.length && <div className="empty">{t('tabEmpty')}</div>}
        <div className="termsplit">
          {/* 所有标签的会话一直挂着,切标签只是不显示 —— 卸载就是断连、丢事务 */}
          {tabs.map((tab) => (
            <TerminalSession
              key={tab.id} style={tab.id === activeId ? undefined : { display: 'none' }}
              conn={tab.conn} active={tab.id === activeId} db={tab.db} gridView={gridView}
              conns={conns} chain={chain} scriptEnabled={scriptEnabled} scriptSavePath={scriptSavePath}
              onUpdateRisk={(v) => patchTab(tab.id, { risk: v })} onUpdateWsStatus={(v) => patchTab(tab.id, { wsStatus: v })}
              // A `use <db>` in the terminal switches its target database — mirror it onto the
              // tab so the tree highlights the new database.
              onUpdateDb={(v) => patchTab(tab.id, { db: v })} onResult={(r) => setResult(tab.id, r)}
              onClose={() => closeTab(tab.id)}
            />
          ))}
          {gridView && tabs.length > 0 && result && (
            <>
              <div
                ref={splitEl}
                className={clsx('splitter', dragging && 'dragging')}
                role="separator" aria-orientation="horizontal" tabIndex={0}
                aria-valuenow={Math.round(gridH)} aria-valuemin={GRID_H_MIN} aria-valuemax={GRID_H_MAX}
                title={t('splitHint')}
                onPointerDown={onSplitDown} onPointerMove={onSplitMove}
                onPointerUp={onSplitUp} onPointerCancel={onSplitUp}
                onDoubleClick={() => setGridH(GRID_H_DEFAULT)}
                onKeyDown={onSplitKey}
              ><span className="grip" /></div>
              <ResultGrid
                columns={result.columns} rows={result.rows} more={result.more}
                className="gridpanel" style={{ flexBasis: gridH + '%' }} onClose={toggleGrid} onMore={() => void result.loadMore?.()}
              />
            </>
          )}
        </div>
      </div>

      {inspCollapsed && !zen ? (
        <div className="rail right" title={t('ctxExpand')} onClick={toggleInsp}><PanelRightOpen size={16} /></div>
      ) : !zen && (
        <RiskInspector
          risk={activeTab?.risk || 'idle'} riskCommands={riskCommands}
          conn={activeTab?.conn || null} chain={chain} onCollapse={toggleInsp}
        />
      )}
    </div>
  )
}
