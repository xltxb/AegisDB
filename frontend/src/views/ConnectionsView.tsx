import { Fragment, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import { Database, Tag, Pencil, Search, ChevronDown, X, Plus, Upload, Copy, Check,
  ChevronsDownUp, ChevronsUpDown, Activity, Download, Rows3, Rows2, DatabaseZap } from 'lucide-react'
import InspectionBadge from '@/components/common/InspectionBadge'
import VButton from '@/components/common/VButton'
import TagEditModal from '@/components/modals/TagEditModal'
import api from '@/api'
import { envError } from '@/api/http'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useNow } from '@/hooks/useNow'
import { useStateRef } from '@/hooks/useStateRef'
import { confirmAction } from '@/lib/confirm'
import { UTF8_BOM } from '@/lib/transcript'
import { csvCell } from '@/lib/csv'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { copyText } from '@/lib/clipboard'
import { relTime } from '@/lib/relTime'
import { BUILTIN_CONN_LAYER, builtinLabel } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useInspectionStore } from '@/stores/inspection'
import type { Connection, Project } from '@/types'
import ConnectionImportModal, { type ImportModalHandle } from './ConnectionImportModal'
import ConnectionNewModal, { type NewModalHandle } from './ConnectionNewModal'
import ConnectionEditModal, { replicaAddr, type EditModalHandle } from './ConnectionEditModal'
import './ConnectionsView.scoped.css'

interface Db { name: string; projectId?: number }

// 一台实例「异常」是什么意思 —— 全页只有这一个定义。
//
// 它是两件事的并集,而在此之前只有前一件:
//
//   维护态    人标的"别碰"
//   连不上    上一次巡检真的没连上(后端自 0037 起记下来)
//
// 少了后一件的后果,是一个叫「仅看异常」的筛选器**永远筛不出连不上的实例** ——
// 两台新加的、根本连不上的库,在页面上和健康的库长得一模一样。
//
// 从没巡检过**不算**异常:那是"不知道",不是"坏了"。但它也不该被显示成正常 ——
// 见行上的 checkTip。
function isUnhealthy(c: Connection) {
  return c.status !== 'online' || c.lastCheckOk === false
}

/** Types ordered by the engine catalogue, with anything unrecognised last. */
function byCatalogueOrder(a: string, b: string): number {
  const labels = engineLabels()
  const ia = labels.indexOf(a)
  const ib = labels.indexOf(b)
  if (ia === ib) return a.localeCompare(b)
  if (ia < 0) return 1
  if (ib < 0) return -1
  return ia - ib
}

const tagArr = (s: string) => (s ? s.split(',').map((x) => x.trim()).filter(Boolean) : [])

// 与主库同一个地址、只换了账号:没有从库的架构(TiDB 等)配的只读账号,标签不叫「从库」
const replicaIsReadonly = (c: Connection) => !!c.replicaHost && c.replicaHost.toLowerCase() === (c.host || '').toLowerCase() && (!c.replicaPort || c.replicaPort === c.port)

function roleColor(r: string) { return r?.startsWith('dba') ? '#8facff' : 'var(--text-muted)' }

// 原生 select 的值不在选项里(例如归属的项目没列出来)时留空:React 会改选第一项,
// 那等于替这台实例声称了一个它没有的值。
const blankIfUnknown = (v: string | number) => (el: HTMLSelectElement | null) => {
  if (el && ![...el.options].some((o) => o.value === String(v))) el.selectedIndex = -1
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// 每组的渲染预算。展开一个 28 台的生产组时先出 25 行,其余由人点一下再出 ——
// 这一页每行都带着标签、下拉框和可展开的库面板,一次性铺开的不是 88 行文本,
// 而是上千个节点。
const PAGE = 25

// 同步在服务端后台跑(大实例十几秒),这里每隔一会儿问一次跑完没有。
// 离开页面就不再问 —— 服务端照样跑完,下次进来点一下就能看到结果。
const META_POLL_MS = 1500
const META_POLL_GIVE_UP_MS = 10 * 60 * 1000

/**
 * Instances grouped environment → database type — was four fixed computeds, one
 * per built-in environment, with no type dimension at all.
 *
 * Both levels are needed to answer "what is in this cluster": which engine an
 * instance speaks decides what its commands mean and which driver reaches it, so
 * a table that only sorted by environment left that in a column to be scanned for.
 *
 * The trailing group collects instances whose environment no longer resolves.
 * They must stay listed: this page is where an operator would go to move them
 * somewhere real, and an unlisted instance is one nobody can fix.
 */
interface ConnGroup {
  key: string
  label: string
  cls: string
  count: number
  /** 不展开也要看得见的健康概况 —— "这个环境现在有没有事"。 */
  ok: number
  bad: number
  /** 预算之内实际渲染的行;`hidden` 是被留在后面、由人点一下才出来的条数。 */
  types: { key: string; label: string; rows: Connection[]; total: number }[]
  hidden: number
}

export default function ConnectionsView() {
  const { t } = useI18n()
  const me = useAuthStore((s) => s.me)
  // Instance configuration is platform-admin only (backend enforces it too).
  const isAdmin = me?.roleCodes?.includes('admin') ?? (me?.roleCode === 'admin')

  // 出厂分层的连接层级(「L1 核心 · 写」)按读者语言显示;自填的原样。
  const layerLabel = (s: string) => builtinLabel(BUILTIN_CONN_LAYER, s, t)
  const notify = useUIStore((s) => s.notify)
  const notifyError = useUIStore((s) => s.notifyError)
  const envtier = useEnvTierStore()
  // 实例名旁的巡检标记(严重 / 警告才显示)
  const inspByConn = useInspectionStore((s) => s.byConn)

  const [conns, setConns] = useState<Connection[]>([])
  // 项目:库的组织归属。与 tags 是两回事 —— tags 决定谁能碰这个库(判定层会看),
  // 项目决定这个库归谁跟进(判定层不看)。两者显示上刻意分开,免得被当成一回事。
  const [projects, setProjects] = useState<Project[]>([])
  // 项目的增删改搬去了「项目」页;这里只留库归属,它要对着实例底下的库来点,
  // 离开这个上下文就没法用。列表只读一次即可 —— 新建的项目在那边建,回到这页
  // 会重新加载。
  const projectName = (id?: number) => projects.find((p) => p.id === id)?.name || ''
  // 展开一个实例才去列它的库:库是**实时发现**的,对真连接意味着一次网络往返,
  // 没人看的时候不该替他付这个钱。展开态、库列表、加载/报错各自按实例存。
  const [openDbs, setOpenDbs, openDbsRef] = useStateRef<Record<number, boolean>>({})
  const [dbsOf, setDbsOf, dbsOfRef] = useStateRef<Record<number, Db[]>>({})
  const [dbsBusy, setDbsBusy] = useState<Record<number, boolean>>({})
  const [dbsErr, setDbsErr] = useState<Record<number, string>>({})

  async function toggleDbs(c: Connection) {
    const next = !openDbsRef.current[c.id]
    setOpenDbs((m) => ({ ...m, [c.id]: next }))
    if (next && !dbsOfRef.current[c.id]) await loadDbs(c)
  }

  async function loadDbs(c: Connection) {
    setDbsBusy((m) => ({ ...m, [c.id]: true }))
    setDbsErr((m) => ({ ...m, [c.id]: '' }))
    try {
      const r = await api.connectionSchema(c.id)
      // 连不上目标库时服务端把原因放在 error 里而不是抛错 —— 原样说出来,
      // 空列表配一句“没有库”会把网络问题伪装成事实。
      const err = r.error
      if (err) setDbsErr((m) => ({ ...m, [c.id]: err }))
      setDbsOf((m) => ({ ...m, [c.id]: (r.databases || []).map((d) => ({ name: d.name, projectId: d.projectId || 0 })) }))
    } catch (e: any) {
      setDbsErr((m) => ({ ...m, [c.id]: e?.message || String(e) }))
      setDbsOf((m) => ({ ...m, [c.id]: [] }))
    } finally {
      setDbsBusy((m) => ({ ...m, [c.id]: false }))
    }
  }

  // 归属的单位是**库**:一个实例底下的几个库分属不同团队是常态。
  // 先按选的显示,没改成再放回原来的归属。
  async function setDbProject(c: Connection, db: Db, id: number) {
    const prev = db.projectId || 0
    const put = (v: number) =>
      setDbsOf((m) => ({ ...m, [c.id]: (m[c.id] ?? []).map((d) => (d.name === db.name ? { ...d, projectId: v } : d)) }))
    put(id)
    try {
      const env = await api.setDatabaseProject(c.id, db.name, id)
      if (env.code !== 0) { put(prev); notifyError(envError(env), t('actionFailed')) }
    } catch (e) { put(prev); notifyError(e, t('actionFailed')) }
  }

  // 实例行上显示它底下已归属的项目(去重) —— 一个实例可能横跨几个项目,
  // 所以这里是个列表而不是一个值。没展开过就没有,不猜。
  function projectsOn(c: Connection): string[] {
    const dbs = dbsOf[c.id]
    if (!dbs) return []
    const names = new Set<string>()
    for (const d of dbs) {
      const p = projects.find((x) => x.id === d.projectId)
      if (p) names.add(p.name)
    }
    return [...names]
  }
  // 数据源一多,双层分组的长表就成了"滚动扫全表"。三件套:全字段搜索、环境
  // 筛选、分组折叠 —— 搜索时强制展开,否则命中项藏在折叠组里等于没搜到。
  // 输入框绑 qInput,过滤读 q —— 中间隔 300ms 防抖。
  //
  // 几十台实例时每敲一个键就重算一遍双层分组、并把命中组全部展开,是看得见的卡顿;
  // 而"边打字边跳"本身也难用。防抖只挡计算,不挡回显:输入框里的字是即时的。
  const [qInput, setQInput] = useState('')
  const [q, setQ] = useState('')
  const qTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  function onSearch(v: string) {
    setQInput(v)
    clearTimeout(qTimer.current)
    qTimer.current = setTimeout(() => setQ(v), 300)
  }
  useEffect(() => () => clearTimeout(qTimer.current), [])
  function clearSearch() {
    setQInput('')
    clearTimeout(qTimer.current)
    setQ('')
  }

  const [envFilter, setEnvFilter] = useState('') // '' = 全部环境

  // 行上那句话:三种状态各有各的说法,沉默不是其中之一。
  function checkTip(c: Connection) {
    if (c.lastCheckOk === false) return t('connCheckFailedAt', { at: fmtAge(c.lastCheckAt), err: c.lastCheckErr || '' })
    if (c.lastCheckOk === true) return t('connCheckOkAt', { at: fmtAge(c.lastCheckAt) })
    return t('connNeverChecked')
  }

  // 巡检时间显示成"多久以前"。实现在 lib/relTime(与总览页共用);now 每分钟走一步,
  // 让"3 分钟前"不至于在页面上冻住。
  const now = useNow()
  const fmtAge = (at?: string) => relTime(t, at, now)

  // 只看异常:排障时的第一个动作。'' = 全部
  const [statusFilter, setStatusFilter] = useState<'' | 'bad'>('')
  const [engineFilter, setEngineFilter] = useState('') // '' = 全部引擎
  const anyFilter = !!(q.trim() || statusFilter || engineFilter)

  const [collapsed, setCollapsed] = useState(() => new Set<string>())
  function toggleGroup(key: string) {
    setCollapsed((s) => {
      const next = new Set(s)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }
  // 搜索或筛选时强制展开:命中项藏在折叠组里,等于没搜到。
  const isCollapsed = (key: string) => !anyFilter && collapsed.has(key)

  // 默认只展开**第一个分层**下的环境(通常是生产),其余收起。
  //
  // 88 套实例平铺出来没人读得完,而真正天天要看的是生产。这跟终端左侧那棵树用的是
  // 同一条规则(见 DbTree),不在这一页另立一套。只在还没被人动过的组上生效 ——
  // 用户手动展开/收起过的,不该被下一次数据刷新推翻。
  const defaultsDone = useRef(false)
  useLayoutEffect(() => {
    const envs = envtier.environments
    if (defaultsDone.current || !envs.length) return
    const first = envtier.tiers[0]?.code
    setCollapsed((s) => {
      const next = new Set(s)
      for (const e of envs) if (e.tierCode !== first) next.add(e.code)
      return next
    })
    defaultsDone.current = true
  }, [envtier.environments, envtier.tiers])

  /**
   * 点一个环境 = 只看它。
   *
   * 需求里给了两种行为(筛选 / 滚动定位),按"当前是不是全部展示模式"二选一。
   * 这里只做**筛选**这一种,并顺带把该组展开、把表格滚回视野 —— 一个按当前隐藏
   * 状态决定自己要干什么的控件,人按下去之前不知道会发生什么,而这一页的每个
   * 环境本来就有自己的分组条可以直接点开定位。
   *
   * 筛选也正是"减少单页渲染量"这条要求真正生效的那一半。
   */
  function pickEnv(code: string) {
    const picked = envFilter === code ? '' : code
    setEnvFilter(picked)
    if (picked) {
      setCollapsed((s) => {
        const next = new Set(s)
        next.delete(code)
        return next
      })
    }
    tableEl.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }
  const tableEl = useRef<HTMLDivElement>(null)

  // 密度。紧凑模式压到 40px 行高并收起副标题与标签行 —— 1080p 首屏能一眼看到 20 台
  // 以上;要看标签和归属时切回舒适模式。按浏览器记住。
  const [dense, setDenseState] = useState(() => localStorage.getItem('aegisdb_conn_dense') === '1')
  function setDense(v: boolean) { setDenseState(v); localStorage.setItem('aegisdb_conn_dense', v ? '1' : '0') }

  const [budget, setBudget] = useState<Record<string, number>>({})
  const budgetOf = (key: string) => budget[key] ?? PAGE
  function showMore(key: string) { setBudget((b) => ({ ...b, [key]: (b[key] ?? PAGE) + PAGE })) }
  // 搜索匹配:名称 / 地址 / 端口 / 库名 / 引擎 / 标签 / 角色,一个框全找。
  // 端口单独列一项:host:port 那一串里虽然带着它,但只敲 "3306" 也该命中。
  function matches(c: Connection, needle: string) {
    const hay = [c.name, c.host, String(c.port), `${c.host}:${c.port}`, c.database, c.engine, c.tags, c.defaultRole, c.layer, layerLabel(c.layer)]
      .join(' ').toLowerCase()
    return hay.includes(needle)
  }
  const needle = q.trim().toLowerCase()
  /** 除环境外的所有条件 —— 环境计数要按"其它条件都满足"来算。 */
  function passes(c: Connection) {
    if (statusFilter === 'bad' && !isUnhealthy(c)) return false
    if (engineFilter && engineDisplay(c.engine) !== engineFilter) return false
    return !needle || matches(c, needle)
  }
  const passing = conns.filter(passes)
  const filtered = envFilter ? passing.filter((c) => c.env === envFilter) : passing
  /** 引擎下拉的可选值:只列**现有实例真的在用**的引擎,不列一整本目录。 */
  const engineFilterOpts = [...new Set(conns.map((c) => engineDisplay(c.engine)))].sort(byCatalogueOrder)

  // ---- 命中高亮 ----
  //
  // 切成片段逐段渲染,不拼 HTML:实例名、host、标签都是别人填进库里的,
  // 拼进 innerHTML 就是把一个配置字段变成脚本注入口。
  function hi(text: string): { t: string; on: boolean }[] {
    if (!needle || !text) return [{ t: text, on: false }]
    const out: { t: string; on: boolean }[] = []
    const low = text.toLowerCase()
    let i = 0
    for (;;) {
      const j = low.indexOf(needle, i)
      if (j < 0) { if (i < text.length) out.push({ t: text.slice(i), on: false }); break }
      if (j > i) out.push({ t: text.slice(i, j), on: false })
      out.push({ t: text.slice(j, j + needle.length), on: true })
      i = j + needle.length
    }
    return out
  }
  // 环境 chips 的计数按"搜索后"算:筛选器要回答"命中的都在哪",不是全量分布
  const envCounts: Record<string, number> = {}
  for (const c of passing) envCounts[c.env] = (envCounts[c.env] || 0) + 1

  const [allTags, setAllTags] = useState<string[]>([])
  const [tagModal, setTagModal] = useState<{ open: boolean; conn: Connection | null }>({ open: false, conn: null })
  function openTagEdit(c: Connection) { if (!isAdmin) return; setTagModal({ open: true, conn: c }) }
  async function saveTags(tags: string[]) {
    // M14: 保存标签失败以 toast 呈现
    try {
      if (tagModal.conn) await api.setConnectionTags(tagModal.conn.id, tags.join(','))
      setTagModal((m) => ({ ...m, open: false }))
      await load()
      setAllTags((await api.tags()) ?? [])
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  // 业务查询源:只有打了这个标记的实例能被查询授权引用,外部审批系统也只看得到它们。
  // 约定指向从库、使用只读库账号 —— 标记前把这句话摆在确认框里。取消标记会让这台实例上
  // 已有的查询授权不再生效,同样先说清楚。
  async function toggleBiz(c: Connection) {
    if (!isAdmin) return
    const on = !c.bizQuery
    if (!(await confirmAction(t(on ? 'connBizOnConfirm' : 'connBizOffConfirm', { name: c.name })))) return
    try {
      await api.setConnectionBizQuery(c.id, on)
      await load()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  const newModal = useRef<NewModalHandle>(null)
  const impModal = useRef<ImportModalHandle>(null)
  const editModal = useRef<EditModalHandle>(null)
  function openNew() { if (isAdmin) newModal.current?.open() }
  function openImport() { if (isAdmin) impModal.current?.open() }
  function openEdit(c: Connection) { if (isAdmin) editModal.current?.open(c) }

  const byEnv: Record<string, Connection[]> = {}
  for (const c of filtered) (byEnv[c.env] ||= []).push(c)

  // 按引擎分桶,并在**组一级**的预算内裁剪:预算跨引擎桶连续消耗,所以
  // "这一组先出 25 行"说的就是 25 行,而不是每个引擎各出 25 行。
  function buildGroup(key: string, rows: Connection[]) {
    const m: Record<string, Connection[]> = {}
    for (const c of rows) (m[engineDisplay(c.engine)] ||= []).push(c)
    let left = budgetOf(key)
    const types: ConnGroup['types'] = []
    for (const k of Object.keys(m).sort(byCatalogueOrder)) {
      const all = m[k]
      // 预算用完了也要留下标题行(带总数),否则"这个引擎存在"这件事会凭空消失。
      const take = Math.max(0, Math.min(left, all.length))
      left -= take
      types.push({ key: k, label: k, rows: all.slice(0, take), total: all.length })
    }
    const shown = types.reduce((n, x) => n + x.rows.length, 0)
    const bad = rows.filter(isUnhealthy).length
    return {
      types,
      hidden: rows.length - shown,
      // ok 与 bad 是同一个定义的两半:ok = 总数 − bad。分开写两个谓词,
      // 一台在线但连不上的实例会同时进两边,分组头显示 3 台里有 3 好 1 坏。
      bad,
      ok: rows.length - bad,
    }
  }

  const allGroups: ConnGroup[] = envtier.environments.map((e) => {
    const rows = byEnv[e.code] ?? []
    return {
      key: e.code,
      label: envtier.envLabel(e.code),
      cls: envtier.dotForEnv(e.code),
      count: rows.length,
      ...buildGroup(e.code, rows),
    }
  })
  const known = new Set<string>(envtier.environments.map((e) => e.code))
  for (const k of Object.keys(byEnv).filter((x) => !known.has(x)).sort()) {
    allGroups.push({
      key: k, label: `${k} · ${t('envUnknown')}`, cls: 'muted',
      count: byEnv[k].length, ...buildGroup(k, byEnv[k]),
    })
  }
  // 过滤态下空组只是噪音;全量视图仍显示空环境(它回答"这个环境还没接入实例")
  const envGroups = anyFilter || envFilter ? allGroups.filter((g) => g.count > 0) : allGroups

  function expandAll() { setCollapsed(new Set()) }
  function collapseAll() { setCollapsed(new Set(envGroups.map((g) => g.key))) }
  const allCollapsed = envGroups.length > 0 && envGroups.every((g) => collapsed.has(g.key))

  async function load() {
    // M14: 加载失败以 toast 呈现，避免静默失败
    try {
      setConns((await api.connections()) ?? [])
      try { setProjects((await api.projects()) ?? []) } catch { /* 归属下拉降级为空 */ }
      // 顶栏说这一页是干什么的,数字交给页内那两枚徽标:同一个数在一屏上写两遍,
      // 早晚会有一处忘了改 —— 原来的 subDb 就把"3 环境分层"写死了,而实际是 7 个。
      useUIStore.getState().setPageSub({ key: 'connSubLine' })
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  useMountEffect(() => {
    void (async () => {
      // Environments group the table and fill the form's dropdown, so load them
      // before the rows render.
      await useEnvTierStore.getState().load().catch(() => {})
      await load()
      try { setAllTags((await api.tags()) ?? []) } catch { /* ignore */ }
    })()
    void useInspectionStore.getState().load()
  })

  // 地址整串复制。列里是截断显示的 —— 看得见的那一段不等于能粘贴的那一串,
  // 所以复制取的是完整值,而不是屏幕上的文本。
  const [copiedId, setCopiedId] = useState(0)
  const copyTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  async function copyHost(c: Connection) {
    const ok = await copyText(`${c.host}:${c.port}`)
    if (!ok) { notify(t('objCopyFail'), 'error'); return }
    setCopiedId(c.id)
    clearTimeout(copyTimer.current)
    copyTimer.current = setTimeout(() => setCopiedId(0), 1600)
    notify(t('copied'), 'success', 1600)
  }

  // ---- 批量选择 ----
  //
  // 选中集按 id 存,并且**只在当前可见的行**上做全选 —— 一个能把没在屏幕上的行
  // 一起选走的复选框,是批量操作里最容易出事的一种。
  const [sel, setSel] = useState<Set<number>>(() => new Set())
  const selCount = sel.size
  const visibleIds: number[] = []
  for (const g of envGroups) {
    if (isCollapsed(g.key)) continue
    for (const ty of g.types) for (const c of ty.rows) visibleIds.push(c.id)
  }
  const allVisibleSelected = visibleIds.length > 0 && visibleIds.every((id) => sel.has(id))
  function toggleSel(id: number) {
    setSel((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  function toggleSelAll() {
    const next = new Set(sel)
    if (allVisibleSelected) visibleIds.forEach((id) => next.delete(id))
    else visibleIds.forEach((id) => next.add(id))
    setSel(next)
  }
  function clearSel() { setSel(new Set()) }
  const selectedConns = conns.filter((c) => sel.has(c.id))

  const [batchBusy, setBatchBusy, batchBusyRef] = useStateRef(false)

  /**
   * 选中的实例里有多少台在**受管控的分层**上。
   *
   * 判据用 `dangerBanner || requireMfa` —— 和 lib/envTierLabels 里 dotFor 判"这一格
   * 该不该是红的"用的是同一条。不另立一个"什么算生产"的定义:那样会出现分层页上不红、
   * 这里却警告的情况,而两处只要说法不一致,人就会开始不信其中之一。
   *
   * 两个标志都关掉的部署里这个数会是 0 —— 那不是漏判,那是这套环境**自己声明**了
   * 它没有需要额外提醒的分层。
   */
  const selGuarded = selectedConns.filter((c) => {
    const tier = envtier.tierOf(c.env)
    return !!tier && (tier.dangerBanner || tier.requireMfa)
  }).length

  // 批量巡检:逐台真的连过去试一次。并发限 4 —— 这是往目标库上连,不是本地循环。
  //
  // 结论不在这里另存一份:后端自 0037 起把每次探测记在连接上,结尾的 load() 会把它
  // 刷回来。以前这里有一个只存于会话的 checkResult,和持久化的标记并排画在同一行 ——
  // 同一件事实两个标记,而且"不写库"的注释早已失实。
  async function runBatchCheck() {
    const rows = selectedConns.slice()
    if (!rows.length || batchBusyRef.current) return
    setBatchBusy(true)
    let ok = 0
    let bad = 0
    const queue = rows.slice()
    const worker = async () => {
      for (;;) {
        const c = queue.shift()
        if (!c) return
        try { await api.testConnection(c.id); ok++ }
        catch { bad++ }
      }
    }
    try { await Promise.all([worker(), worker(), worker(), worker()]) }
    finally { setBatchBusy(false) }
    notify(t('connCheckDone', { ok, bad }), bad ? 'error' : 'success', bad ? 6000 : 3500)
    await load()
  }

  // 立刻同步一台实例的元数据(表清单 + 列结构)。
  //
  // 它**真的会登录那台库**,所以是一个按钮而不是打开页面就跑。与定时同步的开关无关:
  // 开关关着时这个按钮照样可用 —— 手动本来就是一次明确的决定,也是验证凭据和网络
  // 通不通最快的办法。
  //
  // 结果只留在本次会话里,不写进 c 上:它是"刚才这一次同步的结果",和实例的维护态
  // 是两件事 —— 它随下一次 load() 消失,而实例的巡检结论是持久化的。
  const [syncBusy, setSyncBusy, syncBusyRef] = useStateRef<Record<number, boolean>>({})
  // 三态,不是两态。「同步了 40 张表,但有 2 个库没权限」既不是成功也不是失败 ——
  // 只有成/败两格时它只能被塞进"成功",而那正是这份数据最容易骗人的地方:
  // 少收一个库的表现是"那个库一张表都没有",和"那个库真的是空的"长得一模一样。
  const [syncResult, setSyncResult] = useState<Record<number, { state: 'ok' | 'warn' | 'bad' | 'run'; text: string }>>({})
  const pageAlive = useRef(true)
  useEffect(() => {
    pageAlive.current = true
    return () => { pageAlive.current = false }
  }, [])
  async function syncMeta(c: Connection) {
    if (syncBusyRef.current[c.id]) return
    setSyncBusy((m) => ({ ...m, [c.id]: true }))
    try {
      let st = await api.startConnectionMetadataSync(c.id)
      setSyncResult((m) => ({ ...m, [c.id]: { state: 'run', text: t('connMetaSyncing') } }))
      const giveUpAt = Date.now() + META_POLL_GIVE_UP_MS
      while (st.running) {
        if (!pageAlive.current) return
        if (Date.now() > giveUpAt) {
          notify(t('connMetaSyncStillRunning', { name: c.name }), 'info', 6000)
          return
        }
        await sleep(META_POLL_MS)
        st = await api.metadataSyncStatus(c.id)
      }
      // 后台跑完但什么都没收到(连不上 / 没权限 …):原因照原样转出来。
      if (st.failed) throw new Error(st.failed)
      const r = st
      // 服务端回的是"这台实例现在缓存了多少张表"。0 张是个真实的答案,不是失败 ——
      // 但它和"没同步过"读起来一样,所以要说成"0 张表"而不是留空。
      const n = r.sync?.tables ?? 0
      // sync.err 非空 = 收下了,但不完整(列数截断,或 PG 家族某几个库没读到)。
      // 服务端那句话照原样转出来:它说的是**哪几个库**没读到,换成一句"部分失败"
      // 就等于把唯一可据以行动的信息丢了。
      const warn = (r.sync?.err || '').trim()
      setSyncResult((m) => ({ ...m, [c.id]: { state: warn ? 'warn' : 'ok', text: t('connMetaTables', { n }) } }))
      if (warn) notify(t('connMetaSyncPartial', { name: c.name, n, reason: warn }), 'error', 8000)
      else notify(t('connMetaSyncOk', { name: c.name, n }), 'success')
    } catch (e) {
      // 失败原因来自服务端(连不上 / 没权限 / 未配凭据),照原样显示。
      // 换成一句"同步失败"会把这三种完全不同的处置方式压成同一句话。
      setSyncResult((m) => ({ ...m, [c.id]: { state: 'bad', text: t('connMetaSyncBad') } }))
      // notifyError 自己会从抛出的错误里取服务端那句话,兜底文案只在取不到时用。
      notifyError(e, t('connMetaSyncBad'))
    } finally {
      setSyncBusy((m) => ({ ...m, [c.id]: false }))
    }
  }

  // 导出所选配置。**不含凭据** —— 口令在库里是加密的,而一份能落到下载目录里的
  // CSV 不该是把它们带出网关的那条路。
  function exportSelected() {
    const rows = selectedConns
    if (!rows.length) return
    // 实例名、标签是人填的:以 = + - @ 开头的会被表格软件当公式执行(2026-10 安全审计 L-FE2)
    const esc = csvCell
    const head = ['name', 'engine', 'env', 'host', 'port', 'database', 'defaultRole', 'status', 'tags']
    const body = rows.map((c) => [c.name, c.engine, c.env, c.host, c.port, c.database, c.defaultRole, c.status, c.tags].map(esc).join(','))
    // BOM:这份表是要在 Excel 里打开的,不带的话中文实例名会读成乱码。
    const url = URL.createObjectURL(new Blob([UTF8_BOM + [head.join(','), ...body].join('\n')], { type: 'text/csv;charset=utf-8' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `connections-${rows.length}.csv`
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    notify(t('connExportDone', { n: rows.length }), 'success')
  }

  async function toggle(c: Connection) {
    if (!isAdmin) return
    // M14: 切换状态失败以 toast 呈现
    try {
      await api.toggleConnection(c.id)
      await load()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  const ToggleAllIcon = allCollapsed ? ChevronsUpDown : ChevronsDownUp
  const hits = (text: string) => hi(text).map((p, i) => <span key={i} className={clsx(p.on && 'hit')}>{p.t}</span>)

  function renderRow(c: Connection) {
    const CopyIcon = copiedId === c.id ? Check : Copy
    const dbs = dbsOf[c.id] || []
    const addr = `${c.host}:${c.port}`
    const sync = syncResult[c.id]
    return (
      <Fragment key={c.id}>
        <div className={clsx('trow', sel.has(c.id) && 'sel')}>
          {isAdmin && (
            <div className="cbcell">
              <input type="checkbox" className="cb" checked={sel.has(c.id)} onChange={() => toggleSel(c.id)} />
            </div>
          )}
          <div className="namecell">
            <div className="cn">{hits(c.name)}<InspectionBadge report={inspByConn[c.id]} /></div>
            {!dense && <div className="cl">{layerLabel(c.layer)}</div>}
            {!dense && (
              <div className="tags">
                <span className={clsx('rbadge', c.username ? 'real' : 'sim')}>{c.username ? t('connReal') : t('connSim')}</span>
                {/* 归属是**按库**定的,所以入口在这里展开,而不是行上一个下拉:
                    一个实例底下的几个库可以分属不同项目。与访问标签刻意分开显示。 */}
                <span className={clsx('dbtoggle', openDbs[c.id] && 'on')} onClick={() => toggleDbs(c)}>
                  <Database size={10} />{t('connDbProjects')}
                </span>
                {projectsOn(c).map((pn) => <span key={pn} className="prchip">{pn}</span>)}
                {tagArr(c.tags).map((tg) => <span key={tg} className="tchip">{tg}</span>)}
                {c.replicaHost && <span className="bizchip rep" title={t('connReplicaTip', { addr: replicaAddr(c) })} data-testid={'replica-' + c.id}>{replicaIsReadonly(c) ? t('qServedReadonly') : t('connReplicaTag')}</span>}
                {(isAdmin || c.bizQuery) && (
                  <span
                    className={clsx('bizchip', c.bizQuery && 'on', isAdmin && 'act')}
                    title={c.bizQuery ? t('connBizOnTip') : t('connBizOffTip')} data-testid={'biz-' + c.id} onClick={() => toggleBiz(c)}
                  >
                    <Search size={10} />{t('connBizQuery')}
                  </span>
                )}
                {isAdmin && <span className="tedit" onClick={() => openTagEdit(c)}><Tag size={10} />{tagArr(c.tags).length ? t('edit') : t('tagAdd')}</span>}
              </div>
            )}
          </div>
          <div className="engcell"><span className="eic"><Database size={12} /></span>{engineDisplay(c.engine)}</div>
          {/* 地址一行到底:截断 + 完整值进 title,右边一颗常驻的复制按钮。
              这一串是拿去粘到别处用的,能看见不等于能拿走。 */}
          <div className="hostcell">
            <span className="hosttxt" title={addr}>{hits(addr)}</span>
            <button className="ghost cp" title={copiedId === c.id ? t('copied') : t('copy')} onClick={() => copyHost(c)}>
              <CopyIcon size={12} />
            </button>
          </div>
          <div className="mono" style={{ color: roleColor(c.defaultRole) }}>{c.defaultRole}</div>
          {/* 状态回到"一颗点 + 两个字"。它此前是个填色胶囊,和同一行里的策略
              胶囊长得一样重,而两者一个是事实、一个是配置。 */}
          <div>
            {/* 说明挂在药丸本身上:挂在外层 div 上会被这个填满整格的子元素自己的 title
                盖住(非管理员那边甚至是 title="",空 title 会抑制祖先的提示),于是
                「从未巡检过 ≠ 正常」那句只有悬停在药丸旁边的空白处才出现。 */}
            <span
              className={clsx('st', c.status, isAdmin && 'click')}
              title={isAdmin ? checkTip(c) + ' · ' + t('tipToggleStatus') : checkTip(c)} onClick={() => toggle(c)}
            >
              <span className="sdot" />{c.status === 'online' ? t('online') : t('maint')}
            </span>
            {/* 上一次巡检真的没连上。它**不随页面刷新消失** —— 在此之前这个结论只活在
                一次 HTTP 响应里,于是两台连不上的库和健康的库长得一模一样。
                带上"多久以前":一个不说年龄的结论会被当成此刻的事实。 */}
            {c.lastCheckOk === false && (
              <span className="ck bad" title={c.lastCheckErr}>
                {t('connUnreachable')} · {fmtAge(c.lastCheckAt)}
              </span>
            )}
          </div>
          <div className="opscell">
            {/* 同步结果只在本次会话里存在,和巡检结论同一个理由 */}
            {sync && <span className={clsx('ck', sync.state)} title={sync.state === 'warn' ? t('connMetaIncomplete') : ''}>{sync.text}</span>}
            {isAdmin && (
              <button className="ghost" title={t('connMetaSync')} disabled={syncBusy[c.id]} onClick={() => syncMeta(c)}>
                <DatabaseZap size={14} className={clsx(syncBusy[c.id] && 'spin')} />
              </button>
            )}
            {isAdmin && <button className="ghost" title={t('connEdit')} onClick={() => openEdit(c)}><Pencil size={14} /></button>}
          </div>
        </div>
        {/* 库一级:归属定在这里 */}
        {openDbs[c.id] && (
          <div className="dbpanel">
            {dbsBusy[c.id] ? <div className="dbnote">{t('schemaLoading')}</div>
              : dbsErr[c.id] ? <div className="dbnote err">{dbsErr[c.id]}</div>
                : !dbs.length ? <div className="dbnote">{t('schemaEmpty')}</div>
                  : null}
            {dbs.map((db) => (
              <div key={db.name} className="dbrow">
                <span className="dbname">{db.name}</span>
                {isAdmin ? (
                  <select
                    ref={blankIfUnknown(db.projectId || 0)} className="prsel" value={db.projectId || 0} title={t('connProject')}
                    onChange={(e) => setDbProject(c, db, Number(e.target.value))}
                  >
                    <option value={0}>{t('connNoProject')}</option>
                    {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                  </select>
                ) : db.projectId ? <span className="prchip">{projectName(db.projectId)}</span>
                  : <span className="dbnone">{t('connNoProject')}</span>}
              </div>
            ))}
          </div>
        )}
      </Fragment>
    )
  }

  return (
    <div className={clsx('scy page', selCount > 0 && 'hasfab')}>
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">CONNECTIONS</div>
          <div className="v-h1">{t('t_db')}</div>
          {/* 规模先用两枚轻标签交代("多少台、几种环境"),再说这一页是干嘛的。
              数字混在一句话里时,要数一眼看不出来。 */}
          <div className="hbadges">
            <span className="hbadge">{t('connBadgeInst', { n: conns.length })}</span>
            <span className="hbadge">{t('connBadgeEnv', { n: envtier.environments.length })}</span>
          </div>
        </div>
        <div className="acts">
          {!isAdmin ? <span className="roflag">{t('readOnlyPerms')}</span> : (
            <>
              <VButton variant="secondary" onClick={openImport}><Upload size={14} />{t('importInst')}</VButton>
              <VButton variant="primary" onClick={openNew}><Plus size={15} />{t('newConn')}</VButton>
            </>
          )}
        </div>
      </div>

      <div className="toolbar">
        <div className="searchbox">
          <Search size={14} />
          <input value={qInput} onChange={(e) => onSearch(e.target.value)} className="sin" placeholder={t('connSearchPh')} />
          {qInput && <X size={13} className="sclear" onClick={clearSearch} />}
        </div>
        {/* 排障时的两个快捷条件。只列**现有实例真的在用**的引擎,不摆一整本目录。 */}
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as '' | 'bad')} className="fsel" title={t('colStatus')}>
          <option value="">{t('connFltStatusAll')}</option>
          <option value="bad">{t('connFltStatusBad')}</option>
        </select>
        <select ref={blankIfUnknown(engineFilter)} value={engineFilter} onChange={(e) => setEngineFilter(e.target.value)} className="fsel" title={t('colEngine')}>
          <option value="">{t('connFltEngineAll')}</option>
          {engineFilterOpts.map((e) => <option key={e} value={e}>{e}</option>)}
        </select>
        {/* 分段控制器:一整条凹槽,选中的那一段浮起来。散着的胶囊看不出"这几个
            是同一组、只能选一个";凹槽把它们收成一件东西。 */}
        <div className="seg">
          <button className={clsx('segi', envFilter === '' && 'on')} onClick={() => setEnvFilter('')}>
            {t('connAllEnv')}<i>{filtered.length}</i>
          </button>
          {envtier.environments.map((e) => (
            <button key={e.code} className={clsx('segi', envFilter === e.code && 'on')} onClick={() => pickEnv(e.code)}>
              {/* 圆点带的是**分层**的颜色,与树、审批列表同一个来源。它不随选中态变,
                  因为"这个环境有多危险"和"我现在筛的是不是它"是两件事。 */}
              <span className={clsx('ed', envtier.dotForEnv(e.code))} />{envtier.envLabel(e.code)}<i>{envCounts[e.code] || 0}</i>
            </button>
          ))}
        </div>

        <div className="tbright">
          <button
            className="tbtn" title={allCollapsed ? t('connExpandAll') : t('connCollapseAll')}
            onClick={() => (allCollapsed ? expandAll() : collapseAll())}
          >
            <ToggleAllIcon size={14} />
            {' '}{allCollapsed ? t('connExpandAll') : t('connCollapseAll')}
          </button>
          {/* 密度开关。紧凑模式收起副标题与标签行 —— 那些在排障时是噪音,
              在配置时才是内容,所以是个开关而不是一个决定。 */}
          <div className="seg dens">
            <button className={clsx('segi', !dense && 'on')} title={t('connCozy')} onClick={() => setDense(false)}><Rows2 size={13} /></button>
            <button className={clsx('segi', dense && 'on')} title={t('connDense')} onClick={() => setDense(true)}><Rows3 size={13} /></button>
          </div>
        </div>
      </div>

      {/* 卡片容器 + 内层最小宽度:窄屏时整张表横向滚动,而不是把地址挤成三行。
          滚动条挂在卡片上,表头和数据行在同一个滚动上下文里,不会各滚各的。 */}
      <div ref={tableEl} className={clsx('table', isAdmin && 'adm', dense && 'dense')}>
        <div className="tscroll">
          <div className="thead">
            {isAdmin && (
              <span className="cbcell">
                <input type="checkbox" className="cb" checked={allVisibleSelected} title={t('connSelAllVisible')} onChange={toggleSelAll} />
              </span>
            )}
            <span>{t('colInst')}</span><span>{t('colEngine')}</span><span>{t('colAddr')}</span>
            <span>{t('colRole')}</span><span>{t('colStatus')}</span>
            <span className="tar">{t('apActions')}</span>
          </div>

          {envGroups.map((group) => (
            <Fragment key={group.key}>
              {/* 分组条不再整条染色。原来一整条淡红压在生产环境上,红色在这一页是
                  "危险"的意思,而"这里是生产"不该长期占着那个信号。颜色收进左边那颗
                  圆点里,条子本身是中性的浅底。 */}
              <div className="grouprow click" onClick={() => toggleGroup(group.key)}>
                <ChevronDown size={13} className={clsx('chev', isCollapsed(group.key) && 'closed')} />
                <span className={clsx('d', group.cls)} />
                <span className="glabel">{group.label}</span>
                <span className="gcnt">{t('connGroupCount', { n: group.count })}</span>
                {/* 不展开也能看出这个环境有没有事。异常为 0 时只说"全部正常",
                    把一个恒定的红色 0 挂在那里,久了谁都不看了。 */}
                {group.count > 0 && (
                  <span className={clsx('ghealth', group.bad > 0 && 'bad')}>
                    <span className="hd ok" />{group.ok}{' '}
                    {group.bad > 0 && <><span className="hd bad" />{group.bad}</>}
                  </span>
                )}
              </div>
              {!isCollapsed(group.key) && (
                <>
                  {group.types.map((ty) => (
                    <Fragment key={ty.key}>
                      {/* Second level: database type. The engine decides which driver reaches
                          the instance and how its commands are read, so it groups rather than
                          sitting in a column to be scanned for. */}
                      {ty.rows.length > 0 && <div className="typerow">{ty.label}<span className="gcnt">{ty.total}</span></div>}
                      {ty.rows.map(renderRow)}
                    </Fragment>
                  ))}
                  {!group.types.length && <div className="typerow empty">{t('connGroupEmpty')}</div>}
                  {/* 预算之外的行不进 DOM。这一页每行都带着标签、下拉框和可展开的库面板,
                      一次铺开的不是 N 行文本,而是上千个节点。 */}
                  {group.hidden > 0 && (
                    <div className="morerow">
                      <button className="mbtn" onClick={() => showMore(group.key)}>{t('connShowMore', { n: group.hidden })}</button>
                    </div>
                  )}
                </>
              )}
            </Fragment>
          ))}
          {!envGroups.some((g) => g.count > 0) && <div className="noresult">{t('connNoMatch')}</div>}
        </div>
      </div>

      {/* 悬浮批量操作条。只在选中后出现,并且始终显示"选了几台" ——
          批量动作最怕的是不知道自己正在对多少东西下手。 */}
      {selCount > 0 && createPortal(
        <div className="fab">
          <span className="fcnt">{t('connSelected', { n: selCount })}</span>
          {selGuarded > 0 && <span className="fprod">{t('connSelGuarded', { n: selGuarded })}</span>}
          <button className="fbtn" disabled={batchBusy} onClick={runBatchCheck}><Activity size={13} />{t('connBatchCheck')}</button>
          <button className="fbtn" disabled={batchBusy} onClick={exportSelected}><Download size={13} />{t('connBatchExport')}</button>
          <button className="fbtn ghosty" disabled={batchBusy} onClick={clearSel}><X size={13} />{t('connSelClear')}</button>
        </div>,
        document.body,
      )}

      <TagEditModal
        open={tagModal.open} title={t('tagConnTitle')} subtitle={tagModal.conn ? tagModal.conn.name : ''}
        tags={tagArr(tagModal.conn?.tags || '')} suggestions={allTags}
        onClose={() => setTagModal((m) => ({ ...m, open: false }))} onSave={saveTags}
      />
      <ConnectionImportModal ref={impModal} reload={load} />
      <ConnectionNewModal ref={newModal} reload={load} />
      <ConnectionEditModal ref={editModal} reload={load} />
    </div>
  )
}
