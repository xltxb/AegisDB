// 数据查询(业务方)。
//
// 能查哪些库只由查询授权决定(网关内申请审批、管理员开通或外部审批系统下发,见
// .scratch/biz-data-query/PRD.md 与 issue #173)。这一页
// 只做三件事:选一个我能查的库、写一条 SELECT、看结果。只读、行数上限、执行时限、脱敏都在
// 服务端做 —— 这里只负责把服务端说的话说清楚:截断了多少、哪些列脱敏了、为什么被拒。
//
// 多个查询页签:每个页签有自己的库、SQL 和结果,互不影响(对照着看两个库、或者同一个库的
// 两条查询)。页签的标题、库和 SQL 草稿按人存在浏览器里,刷新不丢;结果不存 —— 那是生产数据。
//
// 布局是上下分屏的工作台:上面是页签、工具条和编辑器,下面是结果 / 消息 / 历史。中间的分割条
// 可以拖,比例存在本机(界面偏好)。整页不滚动,滚动只发生在编辑器和结果表格里面。
import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import clsx from 'clsx'
import {
  Database, Table2, Play, TriangleAlert, EyeOff, Info, Download, Plus, X, Wand2, Eraser, Star,
  Copy, Check, FileCode2, History, MessageSquareText, Rows3, CircleCheck, CircleX, Trash2, Loader2, ShieldAlert, ExternalLink, RefreshCw, KeyRound,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import SqlEditor, { type SqlCompleter, type SqlEditorHandle } from '@/components/query/SqlEditor'
import QueryGrid from '@/components/query/QueryGrid'
import BizApplyModal, { BizApplyList } from '@/components/query/BizApplyModal'
import api from '@/api'
import type { QueryGrant, QueryGrantRequest, BizQueryResult, RuleRef } from '@/types'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import { saveBlob } from '@/lib/download'
import { renderBizReason, BIZ_GRANT_GONE } from '@/lib/ruleText'
import { formatSQL } from '@/lib/sqlFormat'
import { pickRunSql } from '@/lib/sqlPick'
import { splitVertical, ddlOf, showCreateSql } from '@/lib/bizResultView'
import { complete, quoteIfNeeded } from '@/lib/sqlComplete'
import { SQL_KEYWORDS } from '@/lib/sqlHighlight'
import { copyText } from '@/lib/clipboard'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import './QueryView.scoped.css'

const live = (g: QueryGrant) => g.status === 'active'
// 下拉里显示的是"实例 / 库";选项是字符串,按这个标签反查授权。
const grantLabel = (g: QueryGrant) => `${g.source} / ${g.database}`

// 消息页里的一条:这次执行发生了什么(成功几行、被拒的原因、库报错)。只在内存里。
interface LogEntry { at: number; kind: 'ok' | 'warn' | 'err'; db: string; sql: string; text: string }
interface QTab {
  id: number
  title: string
  grantId: number
  sql: string
  // 以下只活在内存里
  running: boolean
  exporting: boolean
  result: BizQueryResult | null
  denied: string
  log: LogEntry[]
  vertical: boolean // 这次结果按 \G 竖排画
  table: string     // 表下拉里最近选的表:「DDL」按钮看它
}
// 历史 / 常用:只存 SQL 文本、库和时间,不存结果。历史与草稿同一个存储键(aegisdb_query_tabs_*),
// 主动登出时一并清掉(见 stores/auth.ts);常用查询单独存(aegisdb_query_saved_*),登出保留。
interface HistEntry { sql: string; grantId: number; db: string; at: number; ok: boolean; rows?: number; ms?: number }
const MAX_TABS = 12
const MAX_HISTORY = 50
const MAX_SAVED = 30
const storeKey = () => `aegisdb_query_tabs_${useAuthStore.getState().me?.id ?? 0}`
// 常用查询单独一个键:它是人主动收藏的,主动登出不该跟着草稿一起清掉(stores/auth 只清 aegisdb_query_tabs_*)。
const savedKey = () => `aegisdb_query_saved_${useAuthStore.getState().me?.id ?? 0}`
// 历史里每条 SQL 存进浏览器时的上限(字符):历史与草稿同一个键,几条超长 SQL 就能把配额占满,
// 之后 setItem 一直失败,刷新后草稿丢失(2026-10-26 回归排查)。超出的只截存储,不截当次显示。
const HIST_SQL_CAP = 20_000
// 单条常用查询的长度上限(字符):收藏不会自动淘汰,超长的几条就能把本机配额占满。超出就拒收并提示,
// 不偷偷截断 —— 截掉一半的收藏载回来就是一条坏 SQL(2026-10-26 回归排查 07)。
const SAVED_SQL_CAP = 20_000
const histOf = (xs: unknown): HistEntry[] => (Array.isArray(xs) ? xs : []).filter((h: any) => h && typeof h.sql === 'string')
  .map((h: any) => ({ sql: String(h.sql), grantId: Number(h.grantId) || 0, db: String(h.db || ''), at: Number(h.at) || 0,
    ok: !!h.ok, rows: h.rows == null ? undefined : Number(h.rows), ms: h.ms == null ? undefined : Number(h.ms) }))

interface TableList { loading: boolean; tables: string[]; truncated: boolean; error: string; quote?: string }
const cacheKey = (g: QueryGrant) => `${g.sourceId}|${g.database}`

const SPLIT_KEY = 'aegisdb_query_split'
const SPLIT_DEFAULT = 44

const fmtTime = (s: string) => {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const fmtClock = (ms: number) => new Date(ms).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const fmtWhen = (ms: number) => new Date(ms).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
const oneLine = (s: string) => s.replace(/\s+/g, ' ').trim()

const NO_COLUMNS: string[] = []
const NO_ROWS: string[][] = []

/** 等这一批状态写进 DOM(子组件也更新完)之后再跑:聚焦、设选区用。 */
function useNextTick() {
  const queue = useRef<(() => void)[]>([])
  const [, bump] = useReducer((n: number) => n + 1, 0)
  useLayoutEffect(() => { queue.current.splice(0).forEach((f) => f()) })
  return useCallback((f: () => void) => { queue.current.push(f); bump() }, [])
}

export default function QueryView() {
  const { t, te } = useI18n()
  const bizI18n = { t, te }
  const nextTick = useNextTick()

  // ---------------------------------------------------------------- 授权(能查哪些库)

  const [grants, setGrants, grantsRef] = useStateRef<QueryGrant[]>([])
  const [exportOn, setExportOn] = useState(false)
  // 「去申请权限」的去处(设置项 bizQuery.applyMode):internal = 在本页弹出申请框,提交一张
  // grant 审批单;external = 跳 bizQuery.applyUrl,空 = 不给链接
  const [applyMode, setApplyMode] = useState<'internal' | 'external'>('internal')
  const [applyUrl, setApplyUrl] = useState('')
  const [maxGrantDays, setMaxGrantDays] = useState(90)
  const [applyOpen, setApplyOpen] = useState(false)
  const [mine, setMine] = useState<QueryGrantRequest[]>([])
  const [withdrawing, setWithdrawing] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')
  const liveGrants = useMemo(() => grants.filter(live), [grants])
  const pendingGrants = grants.filter((g) => g.status === 'pending')
  const liveNow = () => grantsRef.current.filter(live)
  const grantByLabel = (l: string) => liveNow().find((g) => grantLabel(g) === l)

  async function load() {
    setLoading(true)
    setLoadErr('')
    try {
      const r = await api.myQueryGrants()
      setGrants(r.grants ?? [])
      setExportOn(!!r.limits?.exportEnabled)
      setApplyUrl((r.limits?.applyUrl || '').trim())
      const mode = r.limits?.applyMode === 'external' ? 'external' : 'internal'
      setApplyMode(mode)
      setMaxGrantDays(r.limits?.maxGrantDays || 90)
      if (mode === 'internal') void loadMine()
      // 页签指着的授权失效了(撤销、到期)就清掉,让人重新选;没选过的给第一个能查的库。
      const ok = liveNow()
      setTabs((xs) => xs.map((x) => (ok.some((g) => g.id === x.grantId) ? x : { ...x, grantId: ok[0]?.id ?? 0 })))
    } catch (e: any) {
      setLoadErr(e?.message || t('loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  // 我的申请:读不到不挡查询页,只是不显示
  async function loadMine() {
    try { setMine((await api.myBizApplies()) ?? []) } catch { /* 不影响查询 */ }
  }

  async function withdrawApply(r: QueryGrantRequest) {
    if (!(await confirmAction(t('qaWithdrawConfirm', { ap: r.apNo, target: `${r.source}/${r.database}` })))) return
    setWithdrawing(r.id)
    const ui = useUIStore.getState()
    try {
      await api.cancelBizApply(r.id)
      ui.notify(t('qaWithdrawn'), 'success')
      await loadMine()
    } catch (e) {
      ui.notifyError(e, t('actionFailed'))
    } finally { setWithdrawing(0) }
  }

  const applyModal = (
    <BizApplyModal
      open={applyOpen} maxGrantDays={maxGrantDays} exportEnabled={exportOn} mine={mine} busyId={withdrawing}
      onWithdraw={withdrawApply} onSubmitted={() => void loadMine()} onClose={() => setApplyOpen(false)}
    />
  )

  // ---------------------------------------------------------------- 页签

  const [tabs, setTabs, tabsRef] = useStateRef<QTab[]>([])
  const [activeId, setActiveId, activeRef] = useStateRef(0)
  const [hist, setHist, histRef] = useStateRef<HistEntry[]>([])
  const [saved, setSaved, savedRef] = useStateRef<HistEntry[]>([])
  const nextId = useRef(1)
  const tab = tabs.find((x) => x.id === activeId) || tabs[0] as QTab | undefined
  const sel = grants.find((g) => g.id === tab?.grantId && live(g)) || null
  // 事件 / 异步流程里用:此刻的页签与授权(渲染里用上面两个)
  const curTab = (): QTab | undefined => tabsRef.current.find((x) => x.id === activeRef.current) || tabsRef.current[0]
  const curSel = () => { const x = curTab(); return grantsRef.current.find((g) => g.id === x?.grantId && live(g)) || null }
  const patch = (id: number, p: Partial<QTab>) => setTabs((xs) => xs.map((x) => (x.id === id ? { ...x, ...p } : x)))

  const blank = (over: Partial<QTab> = {}): QTab => {
    const id = nextId.current++
    return { id, title: t('qTabN', { n: id }), grantId: liveNow()[0]?.id ?? 0, sql: '',
      running: false, exporting: false, result: null, denied: '', log: [], vertical: false, table: '', ...over }
  }

  // 存:只存标题、库和 SQL 草稿 —— 结果是生产数据,不落浏览器。读写都包 try:隐私模式、
  // 存储被清理时照样能用,只是刷新后回到一个空页签。
  //
  // 配额不够时先保草稿:历史按一半一半地丢,直到写得进去(历史最后可以一条不剩,草稿不能丢)。
  function persist() {
    try { localStorage.setItem(savedKey(), JSON.stringify(savedRef.current)) } catch { /* 存不下就算了 */ }
    const tabsOut = tabsRef.current.map(({ id, title, grantId, sql }) => ({ id, title, grantId, sql }))
    let hs = histRef.current.map((h) => (h.sql.length > HIST_SQL_CAP ? { ...h, sql: h.sql.slice(0, HIST_SQL_CAP) } : h))
    for (;;) {
      try {
        localStorage.setItem(storeKey(), JSON.stringify({ active: activeRef.current, tabs: tabsOut, history: hs }))
        return
      } catch {
        if (!hs.length) return // 只剩草稿也存不下:就算了
        hs = hs.slice(0, Math.floor(hs.length / 2))
      }
    }
  }
  function restore() {
    try {
      const raw = JSON.parse(localStorage.getItem(storeKey()) || 'null')
      if (raw) {
        setHist(histOf(raw.history).slice(0, MAX_HISTORY))
        setSaved(histOf(raw.saved).slice(0, MAX_SAVED)) // 老格式:常用查询还在草稿键里
      }
      const sv = JSON.parse(localStorage.getItem(savedKey()) || 'null')
      if (Array.isArray(sv)) setSaved(histOf(sv).slice(0, MAX_SAVED))
      if (raw && Array.isArray(raw.tabs) && raw.tabs.length) {
        const xs: QTab[] = raw.tabs.slice(0, MAX_TABS).map((x: any) => ({
          id: Number(x.id) || 0, title: String(x.title || ''), grantId: Number(x.grantId) || 0, sql: String(x.sql || ''),
          running: false, exporting: false, result: null, denied: '', log: [], vertical: false, table: '',
        }))
        setTabs(xs)
        nextId.current = Math.max(...xs.map((x) => x.id)) + 1
        setActiveId(xs.some((x) => x.id === raw.active) ? raw.active : xs[0].id)
        return
      }
    } catch { /* 读不到就从一个空页签开始 */ }
    const first = blank()
    setTabs([first])
    setActiveId(first.id)
  }
  // 只在要存的东西变了的时候存(运行状态、结果变了不算)。离开页面时不清计时器:最后一次改动照样要落下。
  const saveTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const lastDraft = useRef<unknown[] | null>(null)
  useEffect(() => {
    const draft: unknown[] = [activeId, hist.length, saved, ...tabs.flatMap((x) => [x.title, x.grantId, x.sql])]
    const prev = lastDraft.current
    lastDraft.current = draft
    if (!prev || (prev.length === draft.length && prev.every((v, i) => Object.is(v, draft[i])))) return
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(persist, 400)
  })

  function addTab(over: Partial<QTab> = {}) {
    if (tabsRef.current.length >= MAX_TABS) return null
    // 新页签沿用当前页签的库:多开一个页签,多半是想在同一个库上再写一条。
    const x = blank({ grantId: curTab()?.grantId || liveNow()[0]?.id || 0, ...over })
    setTabs((xs) => [...xs, x])
    setActiveId(x.id)
    nextTick(() => editor.current?.focus())
    return x
  }
  function closeTab(id: number) {
    const xs = tabsRef.current
    const i = xs.findIndex((x) => x.id === id)
    if (i < 0) return
    if (xs.length === 1) {
      // 最后一个不关掉,清空成新的 —— 一页什么都没有的查询页没有意义。
      const x = blank({ grantId: xs[0].grantId })
      setTabs([x])
      setActiveId(x.id)
      return
    }
    const rest = xs.filter((_, j) => j !== i)
    setTabs(rest)
    if (activeRef.current === id) setActiveId(rest[Math.min(i, rest.length - 1)].id)
  }
  const [renaming, setRenaming, renamingRef] = useStateRef(0)
  const [renameText, setRenameText, renameTextRef] = useStateRef('')
  function startRename(x: QTab) {
    setRenaming(x.id)
    setRenameText(x.title)
    nextTick(() => (document.querySelector('.qtab input') as HTMLInputElement | null)?.select())
  }
  function endRename(save: boolean) {
    const x = tabsRef.current.find((y) => y.id === renamingRef.current)
    const title = renameTextRef.current.trim()
    if (x && save && title) patch(x.id, { title: title.slice(0, 40) })
    setRenaming(0)
  }

  // ---------------------------------------------------------------- 库 / 表 下拉

  function pickDb(label: string) {
    const g = grantByLabel(label)
    const x = curTab()
    if (!g || !x || g.id === x.grantId) return
    patch(x.id, { grantId: g.id, table: '', result: null, denied: '' })
  }

  // 表清单按"实例 + 库"缓存:几个页签开在同一个库上只取一次。
  const [tableCache, setTableCache, tableCacheRef] = useStateRef<Record<string, TableList>>({})
  const tableList: TableList | null = sel ? tableCache[cacheKey(sel)] || null : null
  // ---- 编辑器自动补全(2026-10-26):关键字 + 当前库的表 + 表 / 别名后面的列 ----
  // 与 Web 命令行同一套分析(lib/sqlComplete):光标在字符串 / 注释里不补,FROM / JOIN 后补表,`t.` 后补列。
  // 列按 (库, 表) 缓存,只取授权库里的(后端 /bizquery/columns 同样只认授权库)。
  const [colCache] = useState(() => new Map<string, Promise<string[]>>())
  function columnsOf(g: QueryGrant, table: string): Promise<string[]> {
    const k = `${cacheKey(g)}|${table.toLowerCase()}`
    let p = colCache.get(k)
    if (!p) {
      p = api.bizColumns(g.sourceId, g.database, table)
        .then((env) => (env.code === 0 ? env.data.columns || [] : []))
        .catch(() => [])
      colCache.set(k, p)
    }
    return p
  }
  const completer: SqlCompleter = async (text, cur) => {
    const g = curSel()
    const tl = g ? tableCacheRef.current[cacheKey(g)] || null : null
    const quote = tl?.quote || '"'
    const res = await complete(text, {
      keywords: SQL_KEYWORDS,
      metas: [],
      databases: g ? [g.database] : [],
      quote,
      // 只认授权的这个库:不限定或限定名就是它
      tables: async (q) => (!q || (g && q.toLowerCase() === g.database.toLowerCase()) ? tl?.tables || [] : []),
      columns: (tb) => (g ? columnsOf(g, tb) : Promise.resolve([])),
      contains: true, // 敲名字中间 / 后面一段也能补(2026-10-27)
    }, cur)
    if (!res || !res.candidates.length) return null
    const kw = new Set(SQL_KEYWORDS)
    return { replace: res.replace, items: res.candidates.map((c) => (kw.has(c.toUpperCase()) ? c : quoteIfNeeded(c, quote))) }
  }

  async function loadTables(g: QueryGrant, force = false) {
    const k = cacheKey(g)
    if (!force && tableCacheRef.current[k]) return
    const put = (v: TableList) => setTableCache((c) => ({ ...c, [k]: v }))
    put({ loading: true, tables: [], truncated: false, error: '' })
    try {
      const env = await api.bizTables(g.sourceId, g.database)
      if (env.code !== 0) {
        const ref = (env.data as { reasonRef?: RuleRef } | undefined)?.reasonRef
        put({ loading: false, tables: [], truncated: false, error: renderBizReason(ref, env.msg, bizI18n) })
        if (ref && BIZ_GRANT_GONE.has(ref.code)) void load()
        return
      }
      const d = env.data
      put({ loading: false, tables: d.tables || [], truncated: !!d.truncated, error: d.error || '', quote: d.quote || '' })
    } catch (e: any) {
      put({ loading: false, tables: [], truncated: false, error: e?.message || t('loadFailed') })
    }
  }
  // 选中的授权换了就取它的表清单(已取过的走缓存)
  useEffect(() => {
    if (sel) void loadTables(sel)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel])

  function tablePlaceholder(): string {
    const l = tableList
    if (!l || l.loading) return t('qTablesLoading')
    if (l.error) return t('qTablesFailed')
    return l.tables.length ? t('qPickTable') : t('qNoTables')
  }
  const editor = useRef<SqlEditorHandle>(null)
  const textarea = () => editor.current?.el ?? null
  // 选了表:编辑器是空的就写一条现成的查询;已经有内容就把表名插到光标处,不动人写好的东西。
  function pickTable(name: string) {
    const x = curTab()
    if (!x || !name) return
    if (!x.sql.trim()) {
      patch(x.id, { table: name, sql: `SELECT * FROM ${name}` })
      nextTick(() => editor.current?.focus())
      return
    }
    const el = textarea()
    const at = el ? el.selectionStart : x.sql.length
    const end = el ? el.selectionEnd : x.sql.length
    patch(x.id, { table: name, sql: x.sql.slice(0, at) + name + x.sql.slice(end) })
    nextTick(() => {
      if (!el) return
      el.focus()
      el.setSelectionRange(at + name.length, at + name.length)
    })
  }

  // ---------------------------------------------------------------- 编辑器动作

  const [cursor, setCursor] = useState({ line: 1, col: 1 })
  function formatTab() {
    const x = curTab()
    if (!x || !x.sql.trim()) return
    patch(x.id, { sql: formatSQL(x.sql) })
    nextTick(() => editor.current?.focus())
  }
  function clearTab() {
    const x = curTab()
    if (!x) return
    patch(x.id, { sql: '', result: null, denied: '' })
    nextTick(() => editor.current?.focus())
  }
  // 保存:把当前这条 SQL 收进「常用查询」(历史页里),同一个库上同样的 SQL 只留一条
  function saveQuery() {
    const x = curTab()
    const q = x?.sql.trim()
    if (!x || !q) return
    if (q.length > SAVED_SQL_CAP) { useUIStore.getState().notify(t('qSaveTooLong', { n: SAVED_SQL_CAP }), 'error'); return }
    const g = curSel()
    setSaved((xs) => [
      { sql: q, grantId: x.grantId, db: g ? grantLabel(g) : '', at: Date.now(), ok: true },
      ...xs.filter((h) => !(h.sql === q && h.grantId === x.grantId)),
    ].slice(0, MAX_SAVED))
    useUIStore.getState().notify(t('qSaved'), 'success')
  }

  // ---------------------------------------------------------------- 执行 / 导出

  const [subTab, setSubTab] = useState<'result' | 'log' | 'history'>('result')

  function logTo(id: number, kind: LogEntry['kind'], text: string, sql: string) {
    const g = curSel()
    const entry: LogEntry = { at: Date.now(), kind, db: g ? grantLabel(g) : '', sql, text }
    setTabs((xs) => xs.map((x) => (x.id === id ? { ...x, log: [entry, ...x.log].slice(0, 100) } : x)))
  }
  function remember(x: QTab, sql: string, ok: boolean, rows?: number, ms?: number) {
    const g = curSel()
    const grantId = tabsRef.current.find((y) => y.id === x.id)?.grantId ?? x.grantId
    setHist((hs) => {
      // 同一库上连着执行同一条 SQL 只留最新一条:反复点执行不该把历史(和存储配额)占满
      const rest = hs[0]?.sql === sql && hs[0]?.grantId === grantId ? hs.slice(1) : hs
      return [{ sql, grantId, db: g ? grantLabel(g) : '', at: Date.now(), ok, rows, ms }, ...rest].slice(0, MAX_HISTORY)
    })
  }

  // 被拒:按读者语言讲原因(认不出的 code 回落到服务端的中文原话);授权 / 实例已经没了,
  // 就刷新授权列表 —— 按 code 判断,不去匹配原话(见 lib/ruleText.ts)。返回要显示的那句话。
  function showDenial(id: number, ref: RuleRef | undefined, msg: string): string {
    const denied = renderBizReason(ref, msg, bizI18n)
    patch(id, { denied })
    if (ref && BIZ_GRANT_GONE.has(ref.code)) void load()
    return denied
  }

  // 执行 / 导出交出去的 SQL:有选区只用选区,否则光标所在的那条(分号或空行为界,见 lib/sqlPick)。
  // 编辑器失焦(点了工具栏按钮)时 textarea 仍记着选区。取不到编辑器才退回整段。
  function runSql(x: QTab): string {
    const el = textarea()
    if (!el || el.value !== x.sql) return x.sql.trim()
    return pickRunSql(x.sql, el.selectionStart, el.selectionEnd)
  }

  function run() {
    const x = curTab()
    if (x) void execQuery(runSql(x))
  }

  // 「DDL」:看表下拉里选中的那张表的建表语句。不动编辑器里写的东西,照样过只读判定、记审计。
  function showDdl() {
    const x = curTab()
    const q = x ? showCreateSql(x.table) : ''
    if (q) void execQuery(q)
  }
  const result = tab?.result ?? null
  const ddlView = useMemo(() => (result && !result.error && result.columns && result.rows ? ddlOf(result) : null), [result])

  async function execQuery(q: string) {
    const x = curTab()
    const g = curSel()
    if (!x || !g || !q || x.running) return
    // 结尾的 \G:剥掉再发,结果竖排画(lib/bizResultView)
    const { sql: send, vertical } = splitVertical(q)
    patch(x.id, { running: true, denied: '', result: null, vertical })
    setSubTab('result')
    try {
      const env = await api.bizQueryExec({ sourceId: g.sourceId, database: g.database, sql: send }, g.effectiveTimeoutSec)
      if (env.code !== 0) {
        const denied = showDenial(x.id, (env.data as { reasonRef?: RuleRef } | undefined)?.reasonRef, env.msg)
        logTo(x.id, 'err', denied, q)
        remember(x, q, false)
        return
      }
      const r = env.data
      patch(x.id, { result: r })
      if (r.error) {
        logTo(x.id, r.timedOut ? 'warn' : 'err', renderBizReason(r.errorRef, r.error, bizI18n), q)
        remember(x, q, false)
      } else {
        logTo(x.id, 'ok', t('qLogOk', { n: r.rows.length, ms: r.ms }), q)
        remember(x, q, true, r.rows.length, r.ms)
      }
    } catch (e: any) {
      const denied = e?.message || t('actionFailed')
      patch(x.id, { denied })
      logTo(x.id, 'err', denied, q)
    } finally {
      patch(x.id, { running: false })
    }
  }

  // 导出:服务端算好的 canExport(全局开关、授权「允许导出」、实例分层的数据查询导出限制合起来;
  // dev / uat 默认不受限)。老服务端没有这个字段时退回原来的判断。服务端还会再判一次、记审计。
  // 不在前端拼 CSV / Excel —— 那会绕开导出授权与审计。
  const canExport = sel?.canExport ?? (exportOn && !!sel?.allowExport)
  async function doExport() {
    const x = curTab()
    const g = curSel()
    const q = x && runSql(x)
    if (!x || !g || !q || x.exporting) return
    patch(x.id, { exporting: true, denied: '' })
    try {
      const f = await api.bizQueryExport({ sourceId: g.sourceId, database: g.database, sql: splitVertical(q).sql })
      saveBlob(f.blob, f.filename)
      useUIStore.getState().notify(t('qExported', { name: f.filename }), 'success')
      logTo(x.id, 'ok', t('qExported', { name: f.filename }), q)
    } catch (e: any) {
      const denied = showDenial(x.id, e?.data?.reasonRef, e?.message || t('actionFailed'))
      logTo(x.id, 'err', denied, q)
    } finally {
      patch(x.id, { exporting: false })
    }
  }

  // 复制当前结果(TSV,贴进表格软件即成列)。与终端结果表的「复制 TSV」同一个能力。
  const [copied, setCopied] = useState(false)
  async function copyResult() {
    const r = curTab()?.result
    if (!r || r.error) return
    const d = ddlOf(r)
    const body = d ? d.ddl : [r.columns, ...r.rows].map((row) => row.map((v) => (v ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n')
    if (await copyText(body)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    }
  }

  // ---------------------------------------------------------------- 历史

  function loadHistory(h: HistEntry) {
    const g = liveNow().find((x) => x.id === h.grantId)
    const x = curTab()
    // 当前页签空着就放进来,否则另开一个页签 —— 不覆盖人正在写的东西
    if (x && !x.sql.trim()) {
      patch(x.id, { sql: h.sql, ...(g ? { grantId: g.id } : {}), result: null, denied: '' })
      nextTick(() => editor.current?.focus())
    } else if (!addTab({ sql: h.sql, ...(g ? { grantId: g.id } : {}) })) {
      useUIStore.getState().notify(t('qTabsFull', { n: MAX_TABS }), 'info')
      return
    }
    setSubTab('result')
  }
  const removeSaved = (i: number) => setSaved((xs) => xs.filter((_, j) => j !== i))
  const clearHistory = () => setHist([])

  // ---------------------------------------------------------------- 上下分屏

  // 上半部分占的百分比
  const [split, setSplit, splitRef] = useStateRef(() => {
    try { const v = Number(localStorage.getItem(SPLIT_KEY)); if (v >= 20 && v <= 80) return v } catch { /* 隐私模式 */ }
    return SPLIT_DEFAULT
  })
  const ws = useRef<HTMLDivElement>(null)
  const [dragging, setDragging] = useState(false)
  function startDrag(e: ReactPointerEvent) {
    e.preventDefault()
    const box = ws.current?.getBoundingClientRect()
    if (!box) return
    setDragging(true)
    ;(e.target as HTMLElement).setPointerCapture(e.pointerId)
    const move = (ev: PointerEvent) => {
      setSplit(Math.min(80, Math.max(20, ((ev.clientY - box.top) / box.height) * 100)))
    }
    const up = () => {
      setDragging(false)
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      try { localStorage.setItem(SPLIT_KEY, String(Math.round(splitRef.current))) } catch { /* 隐私模式 */ }
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }
  function resetSplit() {
    setSplit(SPLIT_DEFAULT)
    try { localStorage.removeItem(SPLIT_KEY) } catch { /* 隐私模式 */ }
  }

  // ---------------------------------------------------------------- 显示

  const pendingText = t('qPending', { list: pendingGrants.map((g) => `${g.database}(${t('qStartsAt', { t: fmtTime(g.startsAt) })})`).join('、') })
  // 表清单出了状况 / 有尚未生效的授权:才出那一行提示,平时不占地方
  const hasNotice = !!(tableList?.error || tableList?.truncated || pendingGrants.length)

  useMountEffect(() => {
    restore()
    void load()
  })

  // 结果表格底栏左边的状态;memo 住,敲字时表格不跟着重画
  const gridStatus = useMemo(() => result && !result.error && (
    <span className="qstat" data-testid="q-stat">
      <span className="ok"><CircleCheck size={13} />{t('qOk')}</span>
      <span className="qdiv" />
      <span>{t('qStatRows', { n: (result.rows ?? NO_ROWS).length })}</span>
      <span className="qdiv" />
      <span>{t('qStatMs', { ms: result.ms })}</span>
      {result.truncated && <span className="warn" data-testid="q-truncated"><TriangleAlert size={12} />{t('qTruncated', { n: result.maxRows })}</span>}
      {!!result.maskedColumns?.length && <span className="mask" data-testid="q-masked"><EyeOff size={12} />{t('qMasked', { cols: result.maskedColumns.join(', ') })}</span>}
      {result.simulated && <span className="warn">{t('qSimulated')}</span>}
      {result.served === 'replica' ? <span className="rep" data-testid="q-served">{t('qServedReplica')}</span>
        : result.served === 'primaryFallback' ? <span className="warn" title={result.replicaError} data-testid="q-served">{t('qServedFallback')}</span>
          : result.served === 'readonlyAccount' ? <span className="rep" data-testid="q-served">{t('qServedReadonly')}</span> : null}
    </span>
  ), [result, t])

  function body() {
    if (loading && !grants.length) return <div className="qpick">{t('treeLoading')}</div>
    if (loadErr) return <div className="qpick err">{loadErr}</div>
    // 没有任何生效的授权:拦截卡片,指去申请页(2026-10-22)。盖住的只是这一页 ——
    // 左侧导航仍可用(「记录」页照常能进)。
    if (!liveGrants.length) {
      return (
        <div className="qblock" data-testid="q-no-grants">
          <div className="qblock-card" role="alertdialog" aria-modal="true" aria-label={t('qNoAccessTitle')}>
            <div className="qblock-ic"><ShieldAlert size={26} /></div>
            <div className="qblock-t">{t('qNoAccessTitle')}</div>
            <div className="qblock-b">{applyMode === 'internal' ? t('qNoAccessBodyInternal') : applyUrl ? t('qNoAccessBody') : t('qNoGrants')}</div>
            {pendingGrants.length > 0 && <div className="qblock-pend">{pendingText}</div>}
            <div className="qblock-acts">
              {applyMode === 'internal' ? (
                <button className="qblock-go" data-testid="q-apply" onClick={() => setApplyOpen(true)}>
                  <KeyRound size={14} />{t('qApplyAccess')}
                </button>
              ) : applyUrl && (
                <a className="qblock-go" href={applyUrl} target="_blank" rel="noopener noreferrer" data-testid="q-apply">
                  <ExternalLink size={14} />{t('qApplyAccess')}
                </a>
              )}
              <button className="qblock-re" disabled={loading} data-testid="q-recheck" onClick={load}>
                <RefreshCw size={13} className={clsx(loading && 'qspin')} />{t('qRecheck')}
              </button>
            </div>
            {applyMode === 'internal' && <BizApplyList items={mine} busyId={withdrawing} onWithdraw={withdrawApply} />}
          </div>
        </div>
      )
    }
    return tab ? workbench(tab) : null
  }

  const workbench = (tab: QTab) => (
    <div ref={ws} className={clsx('qws', dragging && 'dragging')}>
      {/* ============================ 上:页签 / 工具条 / 编辑器 ============================ */}
      <section className="qcard qtop" style={{ flexBasis: split + '%' }}>
        <div className="qtabs" role="tablist">
          {tabs.map((x) => (
            <div
              key={x.id} className={clsx('qtab', x.id === tab.id && 'on')} role="tab" aria-selected={x.id === tab.id}
              title={t('qTabRenameTip')} data-testid="q-tab" onClick={() => setActiveId(x.id)} onDoubleClick={() => startRename(x)}
            >
              {x.running ? <Loader2 size={12} className="qspin" /> : <Database size={12} className="qtabic" />}
              {renaming === x.id ? (
                <input
                  value={renameText} onChange={(e) => setRenameText(e.target.value)} className="qren" maxLength={40}
                  onClick={(e) => e.stopPropagation()}
                  onKeyDown={(e) => { if (e.key === 'Enter') endRename(true); else if (e.key === 'Escape') endRename(false) }}
                  onBlur={() => endRename(true)}
                />
              ) : <span className="qtt">{x.title}</span>}
              <button className="qx" title={t('qCloseTab')} data-testid="q-tab-close" onClick={(e) => { e.stopPropagation(); closeTab(x.id) }}><X size={12} /></button>
            </div>
          ))}
          <button className="qadd" disabled={tabs.length >= MAX_TABS} title={t('qNewTab')} data-testid="q-tab-add" onClick={() => addTab()}><Plus size={14} /></button>
        </div>

        <div className="qbar">
          <div className="qfield" data-testid="q-db">
            <span className="qfl"><Database size={13} />{t('qDb')}</span>
            <VSelect
              className="qsel" value={sel ? grantLabel(sel) : t('qPickDb')} options={liveGrants.map(grantLabel)}
              searchable={liveGrants.length > 8} height="30px" floating onChange={pickDb}
            />
          </div>
          <div className="qfield" data-testid="q-table">
            <span className="qfl"><Table2 size={13} />{t('qTable')}</span>
            <VSelect
              className="qsel qsel-tb" value={tab.table || tablePlaceholder()} options={tableList?.tables || NO_COLUMNS} searchable
              height="30px" floating searchPlaceholder={t('qTableSearch')} onChange={pickTable}
            />
            <button
              className="qtool qddl" disabled={!showCreateSql(tab.table) || tab.running || !sel}
              title={tab.table ? t('qDdlTip', { t: tab.table }) : t('qDdlPick')} data-testid="q-ddl" onClick={showDdl}
            >
              <FileCode2 size={14} /><span>DDL</span>
            </button>
          </div>
          {/* 限额、有效期、只读说明:收进一个信息图标,悬停 / 聚焦时展开 */}
          {sel && (
            <div className="qinfo" tabIndex={0} aria-label={t('qInfoTitle')} data-testid="q-info">
              <Info size={15} />
              <div className="qinfo-pop" role="tooltip">
                <div className="qinfo-t">{t('qInfoTitle')}</div>
                <div>{t('qLimits', { rows: sel.effectiveMaxRows, sec: sel.effectiveTimeoutSec })}</div>
                <div>{t('qUntil', { t: fmtTime(sel.endsAt) })}</div>
                <div>{t('qReadOnlyHint')}</div>
                <div className="qinfo-k">{t('qKeyHints')}</div>
              </div>
            </div>
          )}
          <span className="qspacer" />
          <div className="qacts">
            <button className="qtool" disabled={!tab.sql.trim()} title={t('qFormatTip')} data-testid="q-format" onClick={formatTab}><Wand2 size={14} /><span>{t('qFormat')}</span></button>
            <button className="qtool" disabled={!tab.sql} title={t('qClear')} data-testid="q-clear" onClick={clearTab}><Eraser size={14} /><span>{t('qClear')}</span></button>
            <button className="qtool" disabled={!tab.sql.trim()} title={t('qSaveTip')} data-testid="q-save" onClick={saveQuery}><Star size={14} /><span>{t('qSave')}</span></button>
            {/* 已有授权的人申请更多库:internal 弹申请框;external 有地址时给链接 */}
            {applyMode === 'internal' ? (
              <button className="qtool" title={t('qApplyMoreTip')} data-testid="q-apply-more" onClick={() => setApplyOpen(true)}><KeyRound size={14} /><span>{t('qApplyMore')}</span></button>
            ) : applyUrl && (
              <a className="qtool" href={applyUrl} target="_blank" rel="noopener noreferrer" title={t('qApplyMoreTip')} data-testid="q-apply-more"><ExternalLink size={14} /><span>{t('qApplyMore')}</span></a>
            )}
          </div>
          <VButton variant="primary" height="30px" className="qrun" disabled={tab.running || !tab.sql.trim() || !sel} data-testid="q-run" onClick={run}>
            {tab.running ? <Loader2 size={13} className="qspin" /> : <Play size={13} fill="currentColor" />}
            {' '}{tab.running ? t('qRunning') : t('qRun')}{' '}
            <kbd className="qkbd">Ctrl ↵</kbd>
          </VButton>
        </div>

        {hasNotice && (
          <div className="qmeta">
            {tableList?.error
              ? <span className="warn" data-testid="q-tables-error"><TriangleAlert size={12} />{t('qTablesErr', { err: tableList.error })}</span>
              : tableList?.truncated && <span className="warn"><TriangleAlert size={12} />{t('qTablesTruncated', { n: tableList.tables.length })}</span>}
            {pendingGrants.length > 0 && <span className="dim">{pendingText}</span>}
          </div>
        )}

        <div className="qeditor">
          <SqlEditor
            ref={editor} value={tab.sql} onChange={(v) => patch(tab.id, { sql: v })} placeholder={t('qPlaceholder')} data-testid="q-sql"
            completer={completer} onRun={run} onFormat={formatTab} onCursor={setCursor}
          />
        </div>
        <div className="qedfoot">
          {sel && <span className="qtarget" title={grantLabel(sel)}><Database size={11} />{sel.source} / <b>{sel.database}</b></span>}
          <span className="qro">{t('qReadOnlyBadge')}</span>
          <span className="qspacer" />
          <span className="qcur">{t('qCursor', { l: cursor.line, c: cursor.col })}</span>
          <span className="qsep">SQL</span>
        </div>
      </section>

      <div
        className="qsplit" role="separator" aria-orientation="horizontal" title={t('qSplitTip')}
        onPointerDown={startDrag} onDoubleClick={resetSplit}
      ><span /></div>

      {/* ============================ 下:结果 / 消息 / 历史 ============================ */}
      <section className="qcard qbottom">
        <div className="qsub" role="tablist">
          <button className={clsx('qsubt', subTab === 'result' && 'on')} role="tab" data-testid="q-sub-result" onClick={() => setSubTab('result')}>
            <Rows3 size={13} />{t('qSubResult')}{' '}
            {tab.result && !tab.result.error && <span className="qbadge">{(tab.result.rows ?? NO_ROWS).length}</span>}
          </button>
          <button className={clsx('qsubt', subTab === 'log' && 'on')} role="tab" data-testid="q-sub-log" onClick={() => setSubTab('log')}>
            <MessageSquareText size={13} />{t('qSubLog')}{' '}
            {tab.log.length > 0 && <span className={clsx('qbadge', tab.log[0]?.kind === 'err' && 'bad')}>{tab.log.length}</span>}
          </button>
          <button className={clsx('qsubt', subTab === 'history' && 'on')} role="tab" data-testid="q-sub-history" onClick={() => setSubTab('history')}>
            <History size={13} />{t('qSubHistory')}
          </button>
          <span className="qspacer" />
          {subTab === 'result' ? (
            <>
              {tab.result && !tab.result.error && (
                <button className="qtool" data-testid="q-copy" onClick={copyResult}>
                  {copied ? <Check size={13} /> : <Copy size={13} />}<span>{copied ? t('copied') : t('qCopyResult')}</span>
                </button>
              )}
              {canExport && (
                <button className="qtool" disabled={tab.exporting || !tab.sql.trim() || !sel} data-testid="q-export" onClick={doExport}>
                  <Download size={13} /><span>{tab.exporting ? t('qExporting') : t('qExport')}</span>
                </button>
              )}
            </>
          ) : subTab === 'history' && hist.length > 0 ? (
            <button className="qtool" onClick={clearHistory}><Trash2 size={13} /><span>{t('qHistClear')}</span></button>
          ) : null}
        </div>

        {/* 结果 */}
        <div className="qbody" style={{ display: subTab === 'result' ? undefined : 'none' }}>
          {tab.denied && <div className="qmsg err" data-testid="q-denied"><TriangleAlert size={14} />{tab.denied}</div>}
          {tab.result ? (
            tab.result.error ? (
              <div className={clsx('qmsg', tab.result.timedOut ? 'warn' : 'err')} data-testid="q-error">
                <TriangleAlert size={14} />{renderBizReason(tab.result.errorRef, tab.result.error, bizI18n)}
              </div>
            ) : (
              <QueryGrid
                columns={tab.result.columns ?? NO_COLUMNS} rows={tab.result.rows ?? NO_ROWS} vertical={tab.vertical} ddl={ddlView}
                status={gridStatus}
              />
            )
          ) : tab.running ? (
            <div className="qempty"><Loader2 size={22} className="qspin" /><div>{t('qRunning')}</div></div>
          ) : !tab.denied ? (
            <div className="qempty">
              <Play size={22} />
              <div>{t('qResultEmpty')}</div>
            </div>
          ) : null}
        </div>

        {/* 消息 */}
        <div className="qbody qlist scy" style={{ display: subTab === 'log' ? undefined : 'none' }} data-testid="q-log">
          {!tab.log.length && <div className="qempty"><MessageSquareText size={22} /><div>{t('qLogEmpty')}</div></div>}
          {tab.log.map((l, i) => {
            const Icon = l.kind === 'ok' ? CircleCheck : l.kind === 'warn' ? TriangleAlert : CircleX
            return (
              <div key={i} className={clsx('qlog', l.kind)}>
                <Icon size={14} className="qlog-ic" />
                <span className="qlog-at">{fmtClock(l.at)}</span>
                <div className="qlog-main">
                  <div className="qlog-text">{l.text}</div>
                  <div className="qlog-sql" title={l.sql}>{l.db} · {oneLine(l.sql)}</div>
                </div>
              </div>
            )
          })}
        </div>

        {/* 历史 */}
        <div className="qbody qlist scy" style={{ display: subTab === 'history' ? undefined : 'none' }} data-testid="q-history">
          {!hist.length && !saved.length && <div className="qempty"><History size={22} /><div>{t('qHistEmpty')}</div></div>}
          {saved.length > 0 && (
            <>
              <div className="qhh"><Star size={12} />{t('qHistSaved')}</div>
              {saved.map((h, i) => (
                <div key={'s' + i} className="qhist" data-testid="q-saved-item">
                  <div className="qhist-main" onClick={() => loadHistory(h)}>
                    <div className="qhist-sql" title={h.sql}>{oneLine(h.sql)}</div>
                    <div className="qhist-meta">{h.db} · {fmtWhen(h.at)}</div>
                  </div>
                  <button className="qtool" onClick={() => loadHistory(h)}><span>{t('qHistLoad')}</span></button>
                  <button className="qx" title={t('qHistRemove')} onClick={() => removeSaved(i)}><X size={12} /></button>
                </div>
              ))}
            </>
          )}
          {hist.length > 0 && (
            <>
              <div className="qhh"><History size={12} />{t('qHistRecent')}</div>
              {hist.map((h, i) => {
                const Icon = h.ok ? CircleCheck : CircleX
                return (
                  <div key={'h' + i} className="qhist" data-testid="q-history-item">
                    <Icon size={13} className={clsx('qhist-ic', !h.ok && 'bad')} />
                    <div className="qhist-main" onClick={() => loadHistory(h)}>
                      <div className="qhist-sql" title={h.sql}>{oneLine(h.sql)}</div>
                      <div className="qhist-meta">
                        {h.db} · {fmtWhen(h.at)}{h.ok && h.rows != null && <> · {t('qStatRows', { n: h.rows })} · {h.ms} ms</>}
                      </div>
                    </div>
                    <button className="qtool" onClick={() => loadHistory(h)}><span>{t('qHistLoad')}</span></button>
                  </div>
                )
              })}
            </>
          )}
        </div>
      </section>
    </div>
  )

  return <div className="qpage">{body()}{applyModal}</div>
}
