<script setup lang="ts">
// 数据查询(业务方)。
//
// 能查哪些库只由查询授权决定(外部审批系统下发,见 .scratch/biz-data-query/PRD.md)。这一页
// 只做三件事:选一个我能查的库、写一条 SELECT、看结果。只读、行数上限、执行时限、脱敏都在
// 服务端做 —— 这里只负责把服务端说的话说清楚:截断了多少、哪些列脱敏了、为什么被拒。
//
// 多个查询页签:每个页签有自己的库、SQL 和结果,互不影响(对照着看两个库、或者同一个库的
// 两条查询)。页签的标题、库和 SQL 草稿按人存在浏览器里,刷新不丢;结果不存 —— 那是生产数据。
//
// 布局是上下分屏的工作台:上面是页签、工具条和编辑器,下面是结果 / 消息 / 历史。中间的分割条
// 可以拖,比例存在本机(界面偏好)。整页不滚动,滚动只发生在编辑器和结果表格里面。
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Database, Table2, Play, TriangleAlert, EyeOff, Info, Download, Plus, X, Wand2, Eraser, Star,
  Copy, Check, FileCode2, History, MessageSquareText, Rows3, CircleCheck, CircleX, Trash2, Loader2, ShieldAlert, ExternalLink, RefreshCw,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import SqlEditor, { type SqlCompleter } from '@/components/query/SqlEditor.vue'
import QueryGrid from '@/components/query/QueryGrid.vue'
import api from '@/api'
import type { QueryGrant, BizQueryResult, RuleRef } from '@/types'
import { saveBlob } from '@/lib/download'
import { renderBizReason, BIZ_GRANT_GONE } from '@/lib/ruleText'
import { formatSQL } from '@/lib/sqlFormat'
import { pickRunSql } from '@/lib/sqlPick'
import { splitVertical, ddlOf, showCreateSql } from '@/lib/bizResultView'
import { complete, quoteIfNeeded } from '@/lib/sqlComplete'
import { SQL_KEYWORDS } from '@/lib/sqlHighlight'
import { copyText } from '@/lib/clipboard'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'

const { t, te } = useI18n()
const bizI18n = { t: t as never, te: te as never }
const ui = useUIStore()
const auth = useAuthStore()

// ---------------------------------------------------------------- 授权(能查哪些库)

const grants = ref<QueryGrant[]>([])
const exportOn = ref(false)
// 没有任何授权时拦截卡片上「去申请权限」的去处(设置项 bizQuery.applyUrl);空 = 不给链接
const applyUrl = ref('')
const loading = ref(true)
const loadErr = ref('')
const live = (g: QueryGrant) => g.status === 'active'
const liveGrants = computed(() => grants.value.filter(live))
const pendingGrants = computed(() => grants.value.filter((g) => g.status === 'pending'))
// 下拉里显示的是"实例 / 库";选项是字符串,按这个标签反查授权。
const grantLabel = (g: QueryGrant) => `${g.source} / ${g.database}`
const grantByLabel = (l: string) => liveGrants.value.find((g) => grantLabel(g) === l)

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    const r = await api.myQueryGrants()
    grants.value = r.grants
    exportOn.value = !!r.limits?.exportEnabled
    applyUrl.value = (r.limits?.applyUrl || '').trim()
    // 页签指着的授权失效了(撤销、到期)就清掉,让人重新选;没选过的给第一个能查的库。
    const first = liveGrants.value[0]
    for (const tab of tabs.value) {
      if (!liveGrants.value.some((g) => g.id === tab.grantId)) tab.grantId = first?.id ?? 0
    }
  } catch (e: any) {
    loadErr.value = e?.message || t('loadFailed')
  } finally {
    loading.value = false
  }
}

// ---------------------------------------------------------------- 页签

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
const storeKey = computed(() => `aegisdb_query_tabs_${auth.me?.id ?? 0}`)
// 常用查询单独一个键:它是人主动收藏的,主动登出不该跟着草稿一起清掉(stores/auth 只清 aegisdb_query_tabs_*)。
const savedKey = computed(() => `aegisdb_query_saved_${auth.me?.id ?? 0}`)
// 历史里每条 SQL 存进浏览器时的上限(字符):历史与草稿同一个键,几条超长 SQL 就能把配额占满,
// 之后 setItem 一直失败,刷新后草稿丢失(2026-10-26 回归排查)。超出的只截存储,不截当次显示。
const HIST_SQL_CAP = 20_000
// 单条常用查询的长度上限(字符):收藏不会自动淘汰,超长的几条就能把本机配额占满。超出就拒收并提示,
// 不偷偷截断 —— 截掉一半的收藏载回来就是一条坏 SQL(2026-10-26 回归排查 07)。
const SAVED_SQL_CAP = 20_000
const tabs = ref<QTab[]>([])
const activeId = ref(0)
const history = ref<HistEntry[]>([])
const saved = ref<HistEntry[]>([])
let nextId = 1
const tab = computed(() => tabs.value.find((x) => x.id === activeId.value) || tabs.value[0])
const sel = computed(() => grants.value.find((g) => g.id === tab.value?.grantId && live(g)) || null)

const blank = (over: Partial<QTab> = {}): QTab => {
  const id = nextId++
  return { id, title: t('qTabN', { n: id }), grantId: liveGrants.value[0]?.id ?? 0, sql: '',
    running: false, exporting: false, result: null, denied: '', log: [], vertical: false, table: '', ...over }
}

// 存:只存标题、库和 SQL 草稿 —— 结果是生产数据,不落浏览器。读写都包 try:隐私模式、
// 存储被清理时照样能用,只是刷新后回到一个空页签。
//
// 配额不够时先保草稿:历史按一半一半地丢,直到写得进去(历史最后可以一条不剩,草稿不能丢)。
function persist() {
  try { localStorage.setItem(savedKey.value, JSON.stringify(saved.value)) } catch { /* 存不下就算了 */ }
  const tabsOut = tabs.value.map(({ id, title, grantId, sql }) => ({ id, title, grantId, sql }))
  let hist = history.value.map((h) => (h.sql.length > HIST_SQL_CAP ? { ...h, sql: h.sql.slice(0, HIST_SQL_CAP) } : h))
  for (;;) {
    try {
      localStorage.setItem(storeKey.value, JSON.stringify({ active: activeId.value, tabs: tabsOut, history: hist }))
      return
    } catch {
      if (!hist.length) return // 只剩草稿也存不下:就算了
      hist = hist.slice(0, Math.floor(hist.length / 2))
    }
  }
}
const histOf = (xs: unknown): HistEntry[] => (Array.isArray(xs) ? xs : []).filter((h: any) => h && typeof h.sql === 'string')
  .map((h: any) => ({ sql: String(h.sql), grantId: Number(h.grantId) || 0, db: String(h.db || ''), at: Number(h.at) || 0,
    ok: !!h.ok, rows: h.rows == null ? undefined : Number(h.rows), ms: h.ms == null ? undefined : Number(h.ms) }))
function restore() {
  try {
    const raw = JSON.parse(localStorage.getItem(storeKey.value) || 'null')
    if (raw) {
      history.value = histOf(raw.history).slice(0, MAX_HISTORY)
      saved.value = histOf(raw.saved).slice(0, MAX_SAVED) // 老格式:常用查询还在草稿键里
    }
    const sv = JSON.parse(localStorage.getItem(savedKey.value) || 'null')
    if (Array.isArray(sv)) saved.value = histOf(sv).slice(0, MAX_SAVED)
    if (raw && Array.isArray(raw.tabs) && raw.tabs.length) {
      tabs.value = raw.tabs.slice(0, MAX_TABS).map((x: any) => ({
        id: Number(x.id) || 0, title: String(x.title || ''), grantId: Number(x.grantId) || 0, sql: String(x.sql || ''),
        running: false, exporting: false, result: null, denied: '', log: [], vertical: false, table: '',
      }))
      nextId = Math.max(...tabs.value.map((x) => x.id)) + 1
      activeId.value = tabs.value.some((x) => x.id === raw.active) ? raw.active : tabs.value[0].id
      return
    }
  } catch { /* 读不到就从一个空页签开始 */ }
  const first = blank()
  tabs.value = [first]
  activeId.value = first.id
}
let saveTimer: ReturnType<typeof setTimeout> | undefined
watch(() => [activeId.value, tabs.value.map((x) => [x.title, x.grantId, x.sql]), history.value.length, saved.value], () => {
  clearTimeout(saveTimer)
  saveTimer = setTimeout(persist, 400)
}, { deep: true })

function addTab(over: Partial<QTab> = {}) {
  if (tabs.value.length >= MAX_TABS) return null
  // 新页签沿用当前页签的库:多开一个页签,多半是想在同一个库上再写一条。
  const x = blank({ grantId: tab.value?.grantId || liveGrants.value[0]?.id || 0, ...over })
  tabs.value.push(x)
  activeId.value = x.id
  nextTick(() => editor.value?.focus())
  return x
}
function closeTab(id: number) {
  const i = tabs.value.findIndex((x) => x.id === id)
  if (i < 0) return
  if (tabs.value.length === 1) {
    // 最后一个不关掉,清空成新的 —— 一页什么都没有的查询页没有意义。
    const x = blank({ grantId: tabs.value[0].grantId })
    tabs.value = [x]
    activeId.value = x.id
    return
  }
  tabs.value.splice(i, 1)
  if (activeId.value === id) activeId.value = tabs.value[Math.min(i, tabs.value.length - 1)].id
}
const renaming = ref(0)
const renameText = ref('')
function startRename(x: QTab) {
  renaming.value = x.id
  renameText.value = x.title
  nextTick(() => (document.querySelector('.qtab input') as HTMLInputElement | null)?.select())
}
function endRename(save: boolean) {
  const x = tabs.value.find((y) => y.id === renaming.value)
  if (x && save && renameText.value.trim()) x.title = renameText.value.trim().slice(0, 40)
  renaming.value = 0
}

// ---------------------------------------------------------------- 库 / 表 下拉

function pickDb(label: string) {
  const g = grantByLabel(label)
  if (!g || !tab.value || g.id === tab.value.grantId) return
  tab.value.grantId = g.id
  tab.value.table = ''
  tab.value.result = null
  tab.value.denied = ''
}

// 表清单按"实例 + 库"缓存:几个页签开在同一个库上只取一次。
interface TableList { loading: boolean; tables: string[]; truncated: boolean; error: string; quote?: string }
const tableCache = ref<Record<string, TableList>>({})
const cacheKey = (g: QueryGrant) => `${g.sourceId}|${g.database}`
const tableList = computed<TableList | null>(() => (sel.value ? tableCache.value[cacheKey(sel.value)] || null : null))
// ---- 编辑器自动补全(2026-10-26):关键字 + 当前库的表 + 表 / 别名后面的列 ----
// 与 Web 命令行同一套分析(lib/sqlComplete):光标在字符串 / 注释里不补,FROM / JOIN 后补表,`t.` 后补列。
// 列按 (库, 表) 缓存,只取授权库里的(后端 /bizquery/columns 同样只认授权库)。
const colCache = new Map<string, Promise<string[]>>()
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
const completer: SqlCompleter = async (text, cursor) => {
  const g = sel.value
  const tl = tableList.value
  const quote = tl?.quote || '"'
  const res = await complete(text, {
    keywords: SQL_KEYWORDS,
    metas: [],
    databases: g ? [g.database] : [],
    quote,
    // 只认授权的这个库:不限定或限定名就是它
    tables: async (q) => (!q || (g && q.toLowerCase() === g.database.toLowerCase()) ? tl?.tables || [] : []),
    columns: (t) => (g ? columnsOf(g, t) : Promise.resolve([])),
    contains: true, // 敲名字中间 / 后面一段也能补(2026-10-27)
  }, cursor)
  if (!res || !res.candidates.length) return null
  const kw = new Set(SQL_KEYWORDS)
  return { replace: res.replace, items: res.candidates.map((c) => (kw.has(c.toUpperCase()) ? c : quoteIfNeeded(c, quote))) }
}

async function loadTables(g: QueryGrant, force = false) {
  const k = cacheKey(g)
  if (!force && tableCache.value[k]) return
  tableCache.value[k] = { loading: true, tables: [], truncated: false, error: '' }
  try {
    const env = await api.bizTables(g.sourceId, g.database)
    if (env.code !== 0) {
      const ref = (env.data as { reasonRef?: RuleRef } | undefined)?.reasonRef
      tableCache.value[k] = { loading: false, tables: [], truncated: false, error: renderBizReason(ref, env.msg, bizI18n) }
      if (ref && BIZ_GRANT_GONE.has(ref.code)) load()
      return
    }
    const d = env.data
    tableCache.value[k] = { loading: false, tables: d.tables || [], truncated: !!d.truncated, error: d.error || '', quote: d.quote || '' }
  } catch (e: any) {
    tableCache.value[k] = { loading: false, tables: [], truncated: false, error: e?.message || t('loadFailed') }
  }
}
watch(sel, (g) => { if (g) loadTables(g) }, { immediate: true })

const tablePlaceholder = computed(() => {
  const l = tableList.value
  if (!l || l.loading) return t('qTablesLoading')
  if (l.error) return t('qTablesFailed')
  return l.tables.length ? t('qPickTable') : t('qNoTables')
})
const editor = ref<InstanceType<typeof SqlEditor> | null>(null)
const textarea = () => (editor.value?.el as HTMLTextAreaElement | null | undefined) ?? null
// 选了表:编辑器是空的就写一条现成的查询;已经有内容就把表名插到光标处,不动人写好的东西。
function pickTable(name: string) {
  const x = tab.value
  if (!x || !name) return
  x.table = name
  if (!x.sql.trim()) {
    x.sql = `SELECT * FROM ${name}`
    nextTick(() => editor.value?.focus())
    return
  }
  const el = textarea()
  const at = el ? el.selectionStart : x.sql.length
  const end = el ? el.selectionEnd : x.sql.length
  x.sql = x.sql.slice(0, at) + name + x.sql.slice(end)
  nextTick(() => {
    if (!el) return
    el.focus()
    el.setSelectionRange(at + name.length, at + name.length)
  })
}

// ---------------------------------------------------------------- 编辑器动作

const cursor = ref({ line: 1, col: 1 })
function formatTab() {
  const x = tab.value
  if (!x || !x.sql.trim()) return
  x.sql = formatSQL(x.sql)
  nextTick(() => editor.value?.focus())
}
function clearTab() {
  const x = tab.value
  if (!x) return
  x.sql = ''
  x.result = null
  x.denied = ''
  nextTick(() => editor.value?.focus())
}
// 保存:把当前这条 SQL 收进「常用查询」(历史页里),同一个库上同样的 SQL 只留一条
function saveQuery() {
  const x = tab.value
  const q = x?.sql.trim()
  if (!x || !q) return
  if (q.length > SAVED_SQL_CAP) { ui.notify(t('qSaveTooLong', { n: SAVED_SQL_CAP }), 'error'); return }
  const g = sel.value
  saved.value = [
    { sql: q, grantId: x.grantId, db: g ? grantLabel(g) : '', at: Date.now(), ok: true },
    ...saved.value.filter((h) => !(h.sql === q && h.grantId === x.grantId)),
  ].slice(0, MAX_SAVED)
  ui.notify(t('qSaved'), 'success')
}

// ---------------------------------------------------------------- 执行 / 导出

const subTab = ref<'result' | 'log' | 'history'>('result')

function logTo(x: QTab, kind: LogEntry['kind'], text: string, sql: string) {
  x.log.unshift({ at: Date.now(), kind, db: sel.value ? grantLabel(sel.value) : '', sql, text })
  if (x.log.length > 100) x.log.length = 100
}
function remember(x: QTab, sql: string, ok: boolean, rows?: number, ms?: number) {
  const g = sel.value
  // 同一库上连着执行同一条 SQL 只留最新一条:反复点执行不该把历史(和存储配额)占满
  const rest = history.value[0]?.sql === sql && history.value[0]?.grantId === x.grantId ? history.value.slice(1) : history.value
  history.value = [{ sql, grantId: x.grantId, db: g ? grantLabel(g) : '', at: Date.now(), ok, rows, ms }, ...rest].slice(0, MAX_HISTORY)
}

// 被拒:按读者语言讲原因(认不出的 code 回落到服务端的中文原话);授权 / 实例已经没了,
// 就刷新授权列表 —— 按 code 判断,不去匹配原话(见 lib/ruleText.ts)。
function showDenial(x: QTab, ref: RuleRef | undefined, msg: string) {
  x.denied = renderBizReason(ref, msg, bizI18n)
  if (ref && BIZ_GRANT_GONE.has(ref.code)) load()
}

// 执行 / 导出交出去的 SQL:有选区只用选区,否则光标所在的那条(分号或空行为界,见 lib/sqlPick)。
// 编辑器失焦(点了工具栏按钮)时 textarea 仍记着选区。取不到编辑器才退回整段。
function runSql(x: QTab): string {
  const el = textarea()
  if (!el || el.value !== x.sql) return x.sql.trim()
  return pickRunSql(x.sql, el.selectionStart, el.selectionEnd)
}

function run() {
  const x = tab.value
  if (x) execQuery(runSql(x))
}

// 「DDL」:看表下拉里选中的那张表的建表语句。不动编辑器里写的东西,照样过只读判定、记审计。
function showDdl() {
  const x = tab.value
  const q = x ? showCreateSql(x.table) : ''
  if (q) execQuery(q)
}
const ddlView = computed(() => ddlOf(tab.value?.result))

async function execQuery(q: string) {
  const x = tab.value
  const g = sel.value
  if (!x || !g || !q || x.running) return
  // 结尾的 \G:剥掉再发,结果竖排画(lib/bizResultView)
  const { sql: send, vertical } = splitVertical(q)
  x.running = true
  x.denied = ''
  x.result = null
  x.vertical = vertical
  subTab.value = 'result'
  try {
    const env = await api.bizQueryExec({ sourceId: g.sourceId, database: g.database, sql: send }, g.effectiveTimeoutSec)
    if (env.code !== 0) {
      showDenial(x, (env.data as { reasonRef?: RuleRef } | undefined)?.reasonRef, env.msg)
      logTo(x, 'err', x.denied, q)
      remember(x, q, false)
      return
    }
    x.result = env.data
    const r = env.data
    if (r.error) {
      logTo(x, r.timedOut ? 'warn' : 'err', renderBizReason(r.errorRef, r.error, bizI18n), q)
      remember(x, q, false)
    } else {
      logTo(x, 'ok', t('qLogOk', { n: r.rows.length, ms: r.ms }), q)
      remember(x, q, true, r.rows.length, r.ms)
    }
  } catch (e: any) {
    x.denied = e?.message || t('actionFailed')
    logTo(x, 'err', x.denied, q)
  } finally {
    x.running = false
  }
}

// 导出:服务端算好的 canExport(全局开关、授权「允许导出」、实例分层的数据查询导出限制合起来;
// dev / uat 默认不受限)。老服务端没有这个字段时退回原来的判断。服务端还会再判一次、记审计。
// 不在前端拼 CSV / Excel —— 那会绕开导出授权与审计。
const canExport = computed(() => sel.value?.canExport ?? (exportOn.value && !!sel.value?.allowExport))
async function doExport() {
  const x = tab.value
  const g = sel.value
  const q = x && runSql(x)
  if (!x || !g || !q || x.exporting) return
  x.exporting = true
  x.denied = ''
  try {
    const f = await api.bizQueryExport({ sourceId: g.sourceId, database: g.database, sql: splitVertical(q).sql })
    saveBlob(f.blob, f.filename)
    ui.notify(t('qExported', { name: f.filename }), 'success')
    logTo(x, 'ok', t('qExported', { name: f.filename }), q)
  } catch (e: any) {
    showDenial(x, e?.data?.reasonRef, e?.message || t('actionFailed'))
    logTo(x, 'err', x.denied, q)
  } finally {
    x.exporting = false
  }
}

// 复制当前结果(TSV,贴进表格软件即成列)。与终端结果表的「复制 TSV」同一个能力。
const copied = ref(false)
async function copyResult() {
  const r = tab.value?.result
  if (!r || r.error) return
  const d = ddlOf(r)
  const body = d ? d.ddl : [r.columns, ...r.rows].map((row) => row.map((v) => (v ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n')
  if (await copyText(body)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  }
}

// ---------------------------------------------------------------- 历史

function loadHistory(h: HistEntry) {
  const g = liveGrants.value.find((x) => x.id === h.grantId)
  const x = tab.value
  // 当前页签空着就放进来,否则另开一个页签 —— 不覆盖人正在写的东西
  if (x && !x.sql.trim()) {
    x.sql = h.sql
    if (g) x.grantId = g.id
    x.result = null
    x.denied = ''
    nextTick(() => editor.value?.focus())
  } else if (!addTab({ sql: h.sql, ...(g ? { grantId: g.id } : {}) })) {
    ui.notify(t('qTabsFull', { n: MAX_TABS }), 'info')
    return
  }
  subTab.value = 'result'
}
const removeSaved = (i: number) => { saved.value = saved.value.filter((_, j) => j !== i) }
const clearHistory = () => { history.value = [] }

// ---------------------------------------------------------------- 上下分屏

const SPLIT_KEY = 'aegisdb_query_split'
const SPLIT_DEFAULT = 44
const split = ref(SPLIT_DEFAULT) // 上半部分占的百分比
try { const v = Number(localStorage.getItem(SPLIT_KEY)); if (v >= 20 && v <= 80) split.value = v } catch { /* 隐私模式 */ }
const ws = ref<HTMLElement | null>(null)
const dragging = ref(false)
function startDrag(e: PointerEvent) {
  const box = ws.value?.getBoundingClientRect()
  if (!box) return
  dragging.value = true
  ;(e.target as HTMLElement).setPointerCapture(e.pointerId)
  const move = (ev: PointerEvent) => {
    split.value = Math.min(80, Math.max(20, ((ev.clientY - box.top) / box.height) * 100))
  }
  const up = () => {
    dragging.value = false
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    try { localStorage.setItem(SPLIT_KEY, String(Math.round(split.value))) } catch { /* 隐私模式 */ }
  }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
function resetSplit() {
  split.value = SPLIT_DEFAULT
  try { localStorage.removeItem(SPLIT_KEY) } catch { /* 隐私模式 */ }
}

// ---------------------------------------------------------------- 显示

const fmtTime = (s: string) => {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const fmtClock = (ms: number) => new Date(ms).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const fmtWhen = (ms: number) => new Date(ms).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
const oneLine = (s: string) => s.replace(/\s+/g, ' ').trim()
const pendingText = computed(() => t('qPending', { list: pendingGrants.value.map((g) => `${g.database}(${t('qStartsAt', { t: fmtTime(g.startsAt) })})`).join('、') }))
// 表清单出了状况 / 有尚未生效的授权:才出那一行提示,平时不占地方
const hasNotice = computed(() => !!(tableList.value?.error || tableList.value?.truncated || pendingGrants.value.length))

onMounted(async () => {
  restore()
  await load()
})
</script>

<template>
  <div class="qpage">
    <div v-if="loading && !grants.length" class="qpick">{{ $t('treeLoading') }}</div>
    <div v-else-if="loadErr" class="qpick err">{{ loadErr }}</div>
    <!-- 没有任何生效的授权:拦截卡片,指去申请页(2026-10-22)。盖住的只是这一页 ——
         左侧导航仍可用(「记录」页照常能进)。 -->
    <div v-else-if="!liveGrants.length" class="qblock" data-testid="q-no-grants">
      <div class="qblock-card" role="alertdialog" aria-modal="true" :aria-label="$t('qNoAccessTitle')">
        <div class="qblock-ic"><ShieldAlert :size="26" /></div>
        <div class="qblock-t">{{ $t('qNoAccessTitle') }}</div>
        <div class="qblock-b">{{ applyUrl ? $t('qNoAccessBody') : $t('qNoGrants') }}</div>
        <div v-if="pendingGrants.length" class="qblock-pend">{{ pendingText }}</div>
        <div class="qblock-acts">
          <a v-if="applyUrl" class="qblock-go" :href="applyUrl" target="_blank" rel="noopener noreferrer" data-testid="q-apply">
            <ExternalLink :size="14" />{{ $t('qApplyAccess') }}
          </a>
          <button class="qblock-re" :disabled="loading" data-testid="q-recheck" @click="load">
            <RefreshCw :size="13" :class="{ qspin: loading }" />{{ $t('qRecheck') }}
          </button>
        </div>
      </div>
    </div>

    <div v-else-if="tab" ref="ws" class="qws" :class="{ dragging }">
      <!-- ============================ 上:页签 / 工具条 / 编辑器 ============================ -->
      <section class="qcard qtop" :style="{ flexBasis: split + '%' }">
        <div class="qtabs" role="tablist">
          <div v-for="x in tabs" :key="x.id" class="qtab" :class="{ on: x.id === tab.id }" role="tab" :aria-selected="x.id === tab.id"
               :title="$t('qTabRenameTip')" data-testid="q-tab" @click="activeId = x.id" @dblclick="startRename(x)">
            <Loader2 v-if="x.running" :size="12" class="qspin" />
            <Database v-else :size="12" class="qtabic" />
            <input v-if="renaming === x.id" v-model="renameText" class="qren" maxlength="40"
                   @click.stop @keydown.enter="endRename(true)" @keydown.esc="endRename(false)" @blur="endRename(true)" />
            <span v-else class="qtt">{{ x.title }}</span>
            <button class="qx" :title="$t('qCloseTab')" data-testid="q-tab-close" @click.stop="closeTab(x.id)"><X :size="12" /></button>
          </div>
          <button class="qadd" :disabled="tabs.length >= MAX_TABS" :title="$t('qNewTab')" data-testid="q-tab-add" @click="addTab()"><Plus :size="14" /></button>
        </div>

        <div class="qbar">
          <div class="qfield" data-testid="q-db">
            <span class="qfl"><Database :size="13" />{{ $t('qDb') }}</span>
            <VSelect class="qsel" :model-value="sel ? grantLabel(sel) : $t('qPickDb')" :options="liveGrants.map(grantLabel)"
                     :searchable="liveGrants.length > 8" height="30px" floating @update:model-value="pickDb" />
          </div>
          <div class="qfield" data-testid="q-table">
            <span class="qfl"><Table2 :size="13" />{{ $t('qTable') }}</span>
            <VSelect class="qsel qsel-tb" :model-value="tab.table || tablePlaceholder" :options="tableList?.tables || []" searchable
                     height="30px" floating :search-placeholder="$t('qTableSearch')" @update:model-value="pickTable" />
            <button class="qtool qddl" :disabled="!showCreateSql(tab.table) || tab.running || !sel"
                    :title="tab.table ? $t('qDdlTip', { t: tab.table }) : $t('qDdlPick')" data-testid="q-ddl" @click="showDdl">
              <FileCode2 :size="14" /><span>DDL</span>
            </button>
          </div>
          <!-- 限额、有效期、只读说明:收进一个信息图标,悬停 / 聚焦时展开 -->
          <div v-if="sel" class="qinfo" tabindex="0" :aria-label="$t('qInfoTitle')" data-testid="q-info">
            <Info :size="15" />
            <div class="qinfo-pop" role="tooltip">
              <div class="qinfo-t">{{ $t('qInfoTitle') }}</div>
              <div>{{ $t('qLimits', { rows: sel.effectiveMaxRows, sec: sel.effectiveTimeoutSec }) }}</div>
              <div>{{ $t('qUntil', { t: fmtTime(sel.endsAt) }) }}</div>
              <div>{{ $t('qReadOnlyHint') }}</div>
              <div class="qinfo-k">{{ $t('qKeyHints') }}</div>
            </div>
          </div>
          <span class="qspacer" />
          <div class="qacts">
            <button class="qtool" :disabled="!tab.sql.trim()" :title="$t('qFormatTip')" data-testid="q-format" @click="formatTab"><Wand2 :size="14" /><span>{{ $t('qFormat') }}</span></button>
            <button class="qtool" :disabled="!tab.sql" :title="$t('qClear')" data-testid="q-clear" @click="clearTab"><Eraser :size="14" /><span>{{ $t('qClear') }}</span></button>
            <button class="qtool" :disabled="!tab.sql.trim()" :title="$t('qSaveTip')" data-testid="q-save" @click="saveQuery"><Star :size="14" /><span>{{ $t('qSave') }}</span></button>
          </div>
          <VButton variant="primary" height="30px" class="qrun" :disabled="tab.running || !tab.sql.trim() || !sel" data-testid="q-run" @click="run">
            <Loader2 v-if="tab.running" :size="13" class="qspin" /><Play v-else :size="13" fill="currentColor" />
            {{ tab.running ? $t('qRunning') : $t('qRun') }}
            <kbd class="qkbd">Ctrl ↵</kbd>
          </VButton>
        </div>

        <div v-if="hasNotice" class="qmeta">
          <span v-if="tableList?.error" class="warn" data-testid="q-tables-error"><TriangleAlert :size="12" />{{ $t('qTablesErr', { err: tableList.error }) }}</span>
          <span v-else-if="tableList?.truncated" class="warn"><TriangleAlert :size="12" />{{ $t('qTablesTruncated', { n: tableList.tables.length }) }}</span>
          <span v-if="pendingGrants.length" class="dim">{{ pendingText }}</span>
        </div>

        <div class="qeditor">
          <SqlEditor ref="editor" v-model="tab.sql" :placeholder="$t('qPlaceholder')" data-testid="q-sql" :completer="completer"
                     @run="run" @format="formatTab" @cursor="cursor = $event" />
        </div>
        <div class="qedfoot">
          <span v-if="sel" class="qtarget" :title="grantLabel(sel)"><Database :size="11" />{{ sel.source }} / <b>{{ sel.database }}</b></span>
          <span class="qro">{{ $t('qReadOnlyBadge') }}</span>
          <span class="qspacer" />
          <span class="qcur">{{ $t('qCursor', { l: cursor.line, c: cursor.col }) }}</span>
          <span class="qsep">SQL</span>
        </div>
      </section>

      <div class="qsplit" role="separator" aria-orientation="horizontal" :title="$t('qSplitTip')"
           @pointerdown.prevent="startDrag" @dblclick="resetSplit"><span /></div>

      <!-- ============================ 下:结果 / 消息 / 历史 ============================ -->
      <section class="qcard qbottom">
        <div class="qsub" role="tablist">
          <button class="qsubt" :class="{ on: subTab === 'result' }" role="tab" data-testid="q-sub-result" @click="subTab = 'result'">
            <Rows3 :size="13" />{{ $t('qSubResult') }}
            <span v-if="tab.result && !tab.result.error" class="qbadge">{{ tab.result.rows.length }}</span>
          </button>
          <button class="qsubt" :class="{ on: subTab === 'log' }" role="tab" data-testid="q-sub-log" @click="subTab = 'log'">
            <MessageSquareText :size="13" />{{ $t('qSubLog') }}
            <span v-if="tab.log.length" class="qbadge" :class="{ bad: tab.log[0]?.kind === 'err' }">{{ tab.log.length }}</span>
          </button>
          <button class="qsubt" :class="{ on: subTab === 'history' }" role="tab" data-testid="q-sub-history" @click="subTab = 'history'">
            <History :size="13" />{{ $t('qSubHistory') }}
          </button>
          <span class="qspacer" />
          <template v-if="subTab === 'result'">
            <button v-if="tab.result && !tab.result.error" class="qtool" data-testid="q-copy" @click="copyResult">
              <component :is="copied ? Check : Copy" :size="13" /><span>{{ copied ? $t('copied') : $t('qCopyResult') }}</span>
            </button>
            <button v-if="canExport" class="qtool" :disabled="tab.exporting || !tab.sql.trim() || !sel" data-testid="q-export" @click="doExport">
              <Download :size="13" /><span>{{ tab.exporting ? $t('qExporting') : $t('qExport') }}</span>
            </button>
          </template>
          <button v-else-if="subTab === 'history' && history.length" class="qtool" @click="clearHistory"><Trash2 :size="13" /><span>{{ $t('qHistClear') }}</span></button>
        </div>

        <!-- 结果 -->
        <div v-show="subTab === 'result'" class="qbody">
          <div v-if="tab.denied" class="qmsg err" data-testid="q-denied"><TriangleAlert :size="14" />{{ tab.denied }}</div>
          <template v-if="tab.result">
            <div v-if="tab.result.error" class="qmsg" :class="tab.result.timedOut ? 'warn' : 'err'" data-testid="q-error">
              <TriangleAlert :size="14" />{{ renderBizReason(tab.result.errorRef, tab.result.error, bizI18n) }}
            </div>
            <QueryGrid v-else :columns="tab.result.columns" :rows="tab.result.rows" :vertical="tab.vertical" :ddl="ddlView">
              <template #status>
                <span class="qstat" data-testid="q-stat">
                  <span class="ok"><CircleCheck :size="13" />{{ $t('qOk') }}</span>
                  <span class="qdiv" />
                  <span>{{ $t('qStatRows', { n: tab.result.rows.length }) }}</span>
                  <span class="qdiv" />
                  <span>{{ $t('qStatMs', { ms: tab.result.ms }) }}</span>
                  <span v-if="tab.result.truncated" class="warn" data-testid="q-truncated"><TriangleAlert :size="12" />{{ $t('qTruncated', { n: tab.result.maxRows }) }}</span>
                  <span v-if="tab.result.maskedColumns?.length" class="mask" data-testid="q-masked"><EyeOff :size="12" />{{ $t('qMasked', { cols: tab.result.maskedColumns.join(', ') }) }}</span>
                  <span v-if="tab.result.simulated" class="warn">{{ $t('qSimulated') }}</span>
                  <span v-if="tab.result.served === 'replica'" class="rep" data-testid="q-served">{{ $t('qServedReplica') }}</span>
                  <span v-else-if="tab.result.served === 'primaryFallback'" class="warn" :title="tab.result.replicaError" data-testid="q-served">{{ $t('qServedFallback') }}</span>
                  <span v-else-if="tab.result.served === 'readonlyAccount'" class="rep" data-testid="q-served">{{ $t('qServedReadonly') }}</span>
                </span>
              </template>
            </QueryGrid>
          </template>
          <div v-else-if="tab.running" class="qempty"><Loader2 :size="22" class="qspin" /><div>{{ $t('qRunning') }}</div></div>
          <div v-else-if="!tab.denied" class="qempty">
            <Play :size="22" />
            <div>{{ $t('qResultEmpty') }}</div>
          </div>
        </div>

        <!-- 消息 -->
        <div v-show="subTab === 'log'" class="qbody qlist scy" data-testid="q-log">
          <div v-if="!tab.log.length" class="qempty"><MessageSquareText :size="22" /><div>{{ $t('qLogEmpty') }}</div></div>
          <div v-for="(l, i) in tab.log" :key="i" class="qlog" :class="l.kind">
            <component :is="l.kind === 'ok' ? CircleCheck : l.kind === 'warn' ? TriangleAlert : CircleX" :size="14" class="qlog-ic" />
            <span class="qlog-at">{{ fmtClock(l.at) }}</span>
            <div class="qlog-main">
              <div class="qlog-text">{{ l.text }}</div>
              <div class="qlog-sql" :title="l.sql">{{ l.db }} · {{ oneLine(l.sql) }}</div>
            </div>
          </div>
        </div>

        <!-- 历史 -->
        <div v-show="subTab === 'history'" class="qbody qlist scy" data-testid="q-history">
          <div v-if="!history.length && !saved.length" class="qempty"><History :size="22" /><div>{{ $t('qHistEmpty') }}</div></div>
          <template v-if="saved.length">
            <div class="qhh"><Star :size="12" />{{ $t('qHistSaved') }}</div>
            <div v-for="(h, i) in saved" :key="'s' + i" class="qhist" data-testid="q-saved-item">
              <div class="qhist-main" @click="loadHistory(h)">
                <div class="qhist-sql" :title="h.sql">{{ oneLine(h.sql) }}</div>
                <div class="qhist-meta">{{ h.db }} · {{ fmtWhen(h.at) }}</div>
              </div>
              <button class="qtool" @click="loadHistory(h)"><span>{{ $t('qHistLoad') }}</span></button>
              <button class="qx" :title="$t('qHistRemove')" @click="removeSaved(i)"><X :size="12" /></button>
            </div>
          </template>
          <template v-if="history.length">
            <div class="qhh"><History :size="12" />{{ $t('qHistRecent') }}</div>
            <div v-for="(h, i) in history" :key="'h' + i" class="qhist" data-testid="q-history-item">
              <component :is="h.ok ? CircleCheck : CircleX" :size="13" class="qhist-ic" :class="{ bad: !h.ok }" />
              <div class="qhist-main" @click="loadHistory(h)">
                <div class="qhist-sql" :title="h.sql">{{ oneLine(h.sql) }}</div>
                <div class="qhist-meta">
                  {{ h.db }} · {{ fmtWhen(h.at) }}<template v-if="h.ok && h.rows != null"> · {{ $t('qStatRows', { n: h.rows }) }} · {{ h.ms }} ms</template>
                </div>
              </div>
              <button class="qtool" @click="loadHistory(h)"><span>{{ $t('qHistLoad') }}</span></button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
/* 整页固定在视口里:外层不滚,滚动只发生在编辑器与结果表格内部(避免双重滚动条)。
   不套 --page-max:结果表常有十几列,宽屏下两侧留白只会逼人横向滚动。左右边距与顶栏(--space-6)对齐。 */
.qpage { flex: 1; min-height: 0; display: flex; flex-direction: column; width: 100%; min-width: 0;
  padding: var(--space-3) var(--space-6) var(--space-4); box-sizing: border-box; overflow: hidden; background: var(--surface-page); }
.qpick { display: flex; align-items: flex-start; gap: 8px; padding: 18px; border: 1px dashed var(--border-default); border-radius: 12px; background: var(--surface-card);
  font: 500 13px/1.6 var(--font-body); color: var(--text-muted); }
.qpick.err { color: var(--danger-text); }
.qpick svg { flex-shrink: 0; margin-top: 3px; }
.qpend { margin-top: 4px; font-size: 12px; color: var(--text-faint); }

/* 没有授权时的拦截卡片 */
.qblock { flex: 1; display: flex; align-items: center; justify-content: center; padding: 24px;
  background: color-mix(in oklch, var(--surface-page) 70%, transparent); }
.qblock-card { width: min(440px, 100%); padding: 28px 28px 22px; text-align: center; box-sizing: border-box;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; box-shadow: var(--shadow-lg); }
.qblock-ic { width: 52px; height: 52px; margin: 0 auto 14px; display: flex; align-items: center; justify-content: center;
  border-radius: 14px; background: var(--warning-subtle); color: var(--warning-text); }
.qblock-t { font: 700 17px var(--font-display); color: var(--text-strong); }
.qblock-b { margin-top: 8px; font: 500 13px/1.7 var(--font-body); color: var(--text-muted); }
.qblock-pend { margin-top: 10px; padding: 8px 10px; border-radius: 8px; background: var(--surface-sunken); font: 500 12px/1.6 var(--font-body); color: var(--text-faint); }
.qblock-acts { margin-top: 20px; display: flex; flex-direction: column; gap: 8px; }
.qblock-go { display: inline-flex; align-items: center; justify-content: center; gap: 7px; height: 40px; border-radius: 9px;
  background: var(--accent); color: #fff; font: 600 13.5px var(--font-body); text-decoration: none; }
.qblock-go:hover { filter: brightness(1.08); }
.qblock-re { display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: 34px; border: none; border-radius: 8px;
  background: transparent; color: var(--text-muted); font: 500 12.5px var(--font-body); cursor: pointer; }
.qblock-re:hover:not(:disabled) { background: var(--surface-sunken); color: var(--text-strong); }
.qblock-re:disabled { opacity: .5; cursor: default; }

.qws { flex: 1; min-height: 0; display: flex; flex-direction: column; font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
.qws.dragging { cursor: row-resize; user-select: none; }
.qcard { display: flex; flex-direction: column; min-height: 0; overflow: hidden; background: var(--surface-card);
  border: 1px solid var(--border-subtle); border-radius: 8px; box-shadow: var(--shadow-xs); }
.qtop { flex: 0 0 44%; min-height: 200px; }
.qbottom { flex: 1 1 0; min-height: 160px; }

/* 分割条 */
.qsplit { flex-shrink: 0; height: 10px; display: flex; align-items: center; justify-content: center; cursor: row-resize; touch-action: none; }
.qsplit span { width: 44px; height: 4px; border-radius: 999px; background: var(--border-default); transition: background .15s, width .15s; }
.qsplit:hover span, .qws.dragging .qsplit span { width: 72px; background: var(--accent); }

/* 页签条:Chrome / VS Code 风格,横向滚动不折行 */
.qtabs { display: flex; align-items: flex-end; gap: 2px; padding: 6px 8px 0; overflow-x: auto; flex-shrink: 0;
  background: color-mix(in oklch, var(--surface-sunken) 65%, var(--surface-card)); border-bottom: 1px solid var(--border-subtle); scrollbar-width: none; }
.qtab { position: relative; display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 6px 0 11px; max-width: 200px; min-width: 96px; flex-shrink: 0;
  border: 1px solid transparent; border-bottom: none; border-radius: 8px 8px 0 0; margin-bottom: -1px;
  font: 500 12.5px var(--font-body); color: var(--text-muted); cursor: pointer; user-select: none; }
.qtab:hover:not(.on) { color: var(--text-body); background: color-mix(in oklch, var(--surface-card) 60%, transparent); }
.qtab.on { color: var(--text-strong); font-weight: 600; background: var(--surface-card); border-color: var(--border-subtle); }
.qtab.on::before { content: ''; position: absolute; left: 8px; right: 8px; top: 0; height: 2px; border-radius: 0 0 2px 2px; background: var(--accent); }
.qtabic { flex-shrink: 0; color: var(--text-faint); }
.qtab.on .qtabic { color: var(--accent-text); }
.qtt { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.qren { width: 110px; height: 22px; padding: 0 6px; border: 1px solid var(--accent-text); border-radius: 5px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12px var(--font-body); outline: none; }
.qx { display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; border: none; border-radius: 5px; background: transparent; color: var(--text-faint); cursor: pointer; flex-shrink: 0; }
.qx:hover { background: var(--surface-sunken); color: var(--text-body); }
.qadd { display: flex; align-items: center; justify-content: center; width: 28px; height: 28px; margin: 0 0 2px 2px; border: none; border-radius: 7px; background: transparent; color: var(--text-muted); cursor: pointer; flex-shrink: 0; }
.qadd:hover:not(:disabled) { background: var(--surface-card); color: var(--accent-text); }
.qadd:disabled { opacity: .4; cursor: default; }

/* 工具条 */
.qbar { display: flex; align-items: center; gap: 10px; padding: 8px 12px; flex-wrap: wrap; flex-shrink: 0; border-bottom: 1px solid var(--border-subtle); }
.qfield { display: flex; align-items: center; gap: 6px; min-width: 0; }
.qfl { display: inline-flex; align-items: center; gap: 4px; font: 600 11.5px var(--font-body); color: var(--text-faint); white-space: nowrap; }
.qsel { width: 250px; max-width: 46vw; }
.qsel-tb { width: 200px; }
.qspacer { flex: 1; }
.qinfo { position: relative; display: flex; align-items: center; justify-content: center; width: 26px; height: 26px; border-radius: 7px; color: var(--text-faint); cursor: help; outline: none; }
.qinfo:hover, .qinfo:focus-visible { color: var(--accent-text); background: var(--surface-sunken); }
.qinfo-pop { position: absolute; left: 0; top: calc(100% + 6px); z-index: 30; width: 300px; padding: 10px 12px; display: none; flex-direction: column; gap: 4px;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 8px; box-shadow: var(--shadow-md);
  font: 500 12px/1.55 var(--font-body); color: var(--text-muted); cursor: default; }
.qinfo:hover .qinfo-pop, .qinfo:focus-within .qinfo-pop { display: flex; }
.qinfo-t { font-weight: 700; color: var(--text-strong); margin-bottom: 2px; }
.qinfo-k { margin-top: 4px; padding-top: 6px; border-top: 1px solid var(--border-subtle); font-family: var(--font-mono); font-size: 11px; color: var(--text-faint); }
.qacts { display: flex; align-items: center; gap: 2px; padding-right: 6px; margin-right: 2px; border-right: 1px solid var(--border-subtle); }
.qtool { display: inline-flex; align-items: center; gap: 5px; height: 28px; padding: 0 9px; border: 1px solid transparent; border-radius: 6px;
  background: transparent; color: var(--text-muted); font: 500 12px var(--font-body); cursor: pointer; white-space: nowrap; }
.qtool:hover:not(:disabled) { background: var(--surface-sunken); color: var(--text-strong); }
.qtool:disabled { opacity: .4; cursor: default; }
.qrun { gap: 6px; padding: 0 10px 0 12px; }
.qkbd { margin-left: 2px; padding: 1px 5px; border-radius: 4px; background: rgba(255, 255, 255, .18); font: 600 10px var(--font-mono); }
.qmeta { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; padding: 5px 12px; flex-shrink: 0; border-bottom: 1px solid var(--border-subtle);
  font: 500 11.5px var(--font-body); color: var(--text-faint); background: color-mix(in oklch, var(--warning-subtle) 35%, var(--surface-card)); }
.qmeta > span { display: inline-flex; align-items: center; gap: 5px; }
.qmeta .warn { color: var(--warning-text); }

/* 编辑器与其底栏 */
.qeditor { flex: 1; min-height: 0; display: flex; }
.qedfoot { display: flex; align-items: center; gap: 12px; height: 26px; padding: 0 12px; flex-shrink: 0; white-space: nowrap; overflow: hidden; border-top: 1px solid var(--border-subtle);
  background: color-mix(in oklch, var(--surface-sunken) 40%, var(--surface-card)); font: 500 11px var(--font-body); color: var(--text-faint); }
.qtarget { display: inline-flex; align-items: center; gap: 4px; font-family: var(--font-mono); color: var(--text-muted); min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.qtarget b { color: var(--text-strong); }
.qro { padding: 0 6px; border-radius: 4px; background: var(--success-subtle, var(--accent-subtle)); color: var(--success-text, var(--accent-text)); font-weight: 600; }
.qsep { font-family: var(--font-mono); }

/* 结果区子页签 */
.qsub { display: flex; align-items: center; gap: 2px; height: 38px; padding: 0 8px 0 4px; flex-shrink: 0; border-bottom: 1px solid var(--border-subtle); overflow-x: auto; scrollbar-width: none; }
.qsubt { position: relative; display: inline-flex; align-items: center; gap: 6px; height: 38px; padding: 0 12px; flex-shrink: 0; white-space: nowrap; border: none; background: transparent;
  font: 500 12.5px var(--font-body); color: var(--text-muted); cursor: pointer; }
.qsubt:hover { color: var(--text-strong); }
.qsubt.on { color: var(--text-strong); font-weight: 600; }
.qsubt.on::after { content: ''; position: absolute; left: 10px; right: 10px; bottom: -1px; height: 2px; border-radius: 2px 2px 0 0; background: var(--accent); }
.qbadge { min-width: 18px; height: 17px; padding: 0 5px; border-radius: 999px; background: var(--surface-sunken); color: var(--text-muted);
  font: 600 10.5px/17px var(--font-body); text-align: center; font-variant-numeric: tabular-nums; }
.qbadge.bad { background: var(--danger-subtle); color: var(--danger-text); }

.qbody { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.qmsg { display: flex; align-items: flex-start; gap: 8px; margin: 10px 12px 0; padding: 10px 12px; border-radius: 8px; font: 500 12.5px/1.6 var(--font-body); }
.qmsg svg { flex-shrink: 0; margin-top: 3px; }
.qmsg.err { background: var(--danger-subtle); color: var(--danger-text); }
.qmsg.warn { background: var(--warning-subtle); color: var(--warning-text); }
.qempty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; padding: 24px;
  font: 500 12.5px var(--font-body); color: var(--text-faint); text-align: center; }
.qempty svg { opacity: .55; }

.qstat { display: inline-flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.qstat > span { display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
.qstat .ok { color: var(--success-text, var(--success)); font-weight: 600; }
.qstat .warn { color: var(--warning-text); }
.qstat .mask { color: var(--accent-text); }
.qstat .rep { padding: 1px 7px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font-size: 11px; }
.qdiv { width: 1px; height: 12px; background: var(--border-default); }

/* 消息 / 历史 列表 */
.qlist { overflow-y: auto; }
.qlog { display: flex; align-items: flex-start; gap: 10px; padding: 9px 14px; border-bottom: 1px solid var(--border-subtle); font: 500 12.5px/1.55 var(--font-body); }
.qlog-ic { flex-shrink: 0; margin-top: 3px; }
.qlog.ok .qlog-ic { color: var(--success-text, var(--success)); }
.qlog.warn .qlog-ic { color: var(--warning-text); }
.qlog.err .qlog-ic { color: var(--danger-text); }
.qlog-at { flex-shrink: 0; font: 500 11.5px var(--font-mono); color: var(--text-faint); margin-top: 1px; }
.qlog-main { min-width: 0; flex: 1; }
.qlog-text { color: var(--text-body); word-break: break-word; }
.qlog.err .qlog-text { color: var(--danger-text); }
.qlog-sql { margin-top: 2px; font: 400 11.5px var(--font-mono); color: var(--text-faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.qhh { display: flex; align-items: center; gap: 6px; padding: 10px 14px 6px; font: 700 11px var(--font-body); color: var(--text-faint); letter-spacing: .04em; text-transform: uppercase; }
.qhist { display: flex; align-items: center; gap: 8px; padding: 7px 10px 7px 14px; border-bottom: 1px solid var(--border-subtle); }
.qhist:hover { background: color-mix(in oklch, var(--surface-sunken) 60%, var(--surface-card)); }
.qhist-ic { flex-shrink: 0; color: var(--success-text, var(--success)); }
.qhist-ic.bad { color: var(--danger-text); }
.qhist-main { flex: 1; min-width: 0; cursor: pointer; }
.qhist-sql { font: 400 12.5px var(--font-mono); color: var(--text-body); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.qhist-meta { margin-top: 2px; font: 500 11px var(--font-body); color: var(--text-faint); }

.qspin { animation: qspin 1s linear infinite; }
@keyframes qspin { to { transform: rotate(360deg); } }

@media (max-width: 1080px) {
  .qpage { padding: var(--space-3) var(--space-4) var(--space-4); }
}
@media (max-width: 760px) {
  .qpage { padding: 10px; }
  .qsel, .qsel-tb { width: 100%; max-width: none; }
  .qfield { flex: 1 1 100%; }
  .qtool span { display: none; }
  .qkbd { display: none; }
  .qtop { min-height: 260px; }
  .qsubt { padding: 0 9px; }
  .qedfoot .qcur, .qedfoot .qsep { display: none; }
}
</style>
