<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { Database, Tag, Pencil, Search, ChevronDown, X, Plus, Upload, Copy, Check,
  ChevronsDownUp, ChevronsUpDown, Activity, Download, Rows3, Rows2, DatabaseZap } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import TagEditModal from '@/components/modals/TagEditModal.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { parseConnectionImport, IMPORT_TEMPLATE, type ImportRow } from '@/lib/connectionImport'
import { UTF8_BOM } from '@/lib/transcript'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { copyText } from '@/lib/clipboard'
import type { Connection, Project } from '@/types'

const auth = useAuthStore()
// Instance configuration is platform-admin only (backend enforces it too).
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))

const { t } = useI18n()
const ui = useUIStore()
const envtier = useEnvTierStore()

const conns = ref<Connection[]>([])
// 项目:库的组织归属。与 tags 是两回事 —— tags 决定谁能碰这个库(判定层会看),
// 项目决定这个库归谁跟进(判定层不看)。两者显示上刻意分开,免得被当成一回事。
const projects = ref<Project[]>([])
// 项目的增删改搬去了「项目」页;这里只留库归属,它要对着实例底下的库来点,
// 离开这个上下文就没法用。列表只读一次即可 —— 新建的项目在那边建,回到这页
// 会重新加载。
const projectName = (id?: number) => projects.value.find((p) => p.id === id)?.name || ''
// 展开一个实例才去列它的库:库是**实时发现**的,对真连接意味着一次网络往返,
// 没人看的时候不该替他付这个钱。展开态、库列表、加载/报错各自按实例存。
const openDbs = ref<Record<number, boolean>>({})
const dbsOf = ref<Record<number, { name: string; projectId?: number }[]>>({})
const dbsBusy = ref<Record<number, boolean>>({})
const dbsErr = ref<Record<number, string>>({})

async function toggleDbs(c: Connection) {
  openDbs.value[c.id] = !openDbs.value[c.id]
  if (openDbs.value[c.id] && !dbsOf.value[c.id]) await loadDbs(c)
}

async function loadDbs(c: Connection) {
  dbsBusy.value[c.id] = true
  dbsErr.value[c.id] = ''
  try {
    const r = await api.connectionSchema(c.id)
    // 连不上目标库时服务端把原因放在 error 里而不是抛错 —— 原样说出来,
    // 空列表配一句“没有库”会把网络问题伪装成事实。
    if (r.error) dbsErr.value[c.id] = r.error
    dbsOf.value[c.id] = (r.databases || []).map((d) => ({ name: d.name, projectId: d.projectId || 0 }))
  } catch (e: any) {
    dbsErr.value[c.id] = e?.message || String(e)
    dbsOf.value[c.id] = []
  } finally {
    dbsBusy.value[c.id] = false
  }
}

// 归属的单位是**库**:一个实例底下的几个库分属不同团队是常态。
async function setDbProject(c: Connection, db: { name: string; projectId?: number }, id: number) {
  const prev = db.projectId || 0
  try {
    const env = await api.setDatabaseProject(c.id, db.name, id)
    if (env.code !== 0) { db.projectId = prev; ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    db.projectId = id
  } catch (e) { db.projectId = prev; ui.notifyError(e, t('actionFailed')) }
}

// 实例行上显示它底下已归属的项目(去重) —— 一个实例可能横跨几个项目,
// 所以这里是个列表而不是一个值。没展开过就没有,不猜。
function projectsOn(c: Connection): string[] {
  const dbs = dbsOf.value[c.id]
  if (!dbs) return []
  const names = new Set<string>()
  for (const d of dbs) {
    const p = projects.value.find((x) => x.id === d.projectId)
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
const qInput = ref('')
const q = ref('')
let qTimer: ReturnType<typeof setTimeout> | null = null
watch(qInput, (v) => {
  if (qTimer) clearTimeout(qTimer)
  qTimer = setTimeout(() => (q.value = v), 300)
})
onBeforeUnmount(() => { if (qTimer) clearTimeout(qTimer) })
function clearSearch() {
  qInput.value = ''
  if (qTimer) clearTimeout(qTimer)
  q.value = ''
}

const envFilter = ref('') // '' = 全部环境
// 只看异常:排障时的第一个动作。'' = 全部
const statusFilter = ref<'' | 'bad'>('')
const engineFilter = ref('') // '' = 全部引擎
const anyFilter = computed(() => !!(q.value.trim() || statusFilter.value || engineFilter.value))

const collapsed = ref(new Set<string>())
function toggleGroup(key: string) {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsed.value = next
}
// 搜索或筛选时强制展开:命中项藏在折叠组里,等于没搜到。
const isCollapsed = (key: string) => !anyFilter.value && collapsed.value.has(key)

// 默认只展开**第一个分层**下的环境(通常是生产),其余收起。
//
// 88 套实例平铺出来没人读得完,而真正天天要看的是生产。这跟终端左侧那棵树用的是
// 同一条规则(见 DbTree),不在这一页另立一套。只在还没被人动过的组上生效 ——
// 用户手动展开/收起过的,不该被下一次数据刷新推翻。
const defaultsDone = ref(false)
watch(() => envtier.environments, (envs) => {
  if (defaultsDone.value || !envs.length) return
  const first = envtier.tiers[0]?.code
  const next = new Set(collapsed.value)
  for (const e of envs) if (e.tierCode !== first) next.add(e.code)
  collapsed.value = next
  defaultsDone.value = true
}, { immediate: true, deep: true })

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
  envFilter.value = envFilter.value === code ? '' : code
  if (envFilter.value) {
    const next = new Set(collapsed.value)
    next.delete(code)
    collapsed.value = next
  }
  tableEl.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
const tableEl = ref<HTMLElement>()

function expandAll() { collapsed.value = new Set() }
function collapseAll() { collapsed.value = new Set(envGroups.value.map((g) => g.key)) }
const allCollapsed = computed(() => envGroups.value.length > 0 && envGroups.value.every((g) => collapsed.value.has(g.key)))

// 密度。紧凑模式压到 40px 行高并收起副标题与标签行 —— 1080p 首屏能一眼看到 20 台
// 以上;要看标签和归属时切回舒适模式。按浏览器记住。
const dense = ref(localStorage.getItem('vela_conn_dense') === '1')
function setDense(v: boolean) { dense.value = v; localStorage.setItem('vela_conn_dense', v ? '1' : '0') }

// 每组的渲染预算。展开一个 28 台的生产组时先出 25 行,其余由人点一下再出 ——
// 这一页每行都带着标签、下拉框和可展开的库面板,一次性铺开的不是 88 行文本,
// 而是上千个节点。
const PAGE = 25
const budget = ref<Record<string, number>>({})
const budgetOf = (key: string) => budget.value[key] ?? PAGE
function showMore(key: string) { budget.value = { ...budget.value, [key]: budgetOf(key) + PAGE } }
// 搜索匹配:名称 / 地址 / 端口 / 库名 / 引擎 / 标签 / 角色,一个框全找。
// 端口单独列一项:host:port 那一串里虽然带着它,但只敲 "3306" 也该命中。
function matches(c: Connection, needle: string) {
  const hay = [c.name, c.host, String(c.port), `${c.host}:${c.port}`, c.database, c.engine, c.tags, c.defaultRole, c.layer]
    .join(' ').toLowerCase()
  return hay.includes(needle)
}
/** 除环境外的所有条件 —— 环境计数要按"其它条件都满足"来算。 */
function passes(c: Connection) {
  if (statusFilter.value === 'bad' && c.status === 'online') return false
  if (engineFilter.value && engineDisplay(c.engine) !== engineFilter.value) return false
  const needle = q.value.trim().toLowerCase()
  return !needle || matches(c, needle)
}
const filtered = computed(() => {
  let rows = conns.value.filter(passes)
  if (envFilter.value) rows = rows.filter((c) => c.env === envFilter.value)
  return rows
})
/** 引擎下拉的可选值:只列**现有实例真的在用**的引擎,不列一整本目录。 */
const engineFilterOpts = computed(() => {
  const set = new Set(conns.value.map((c) => engineDisplay(c.engine)))
  return [...set].sort(byCatalogueOrder)
})

// ---- 命中高亮 ----
//
// 切成片段用 <mark> 渲染,不用 v-html:实例名、host、标签都是别人填进库里的,
// 拼进 innerHTML 就是把一个配置字段变成脚本注入口。
function hi(text: string): { t: string; on: boolean }[] {
  const needle = q.value.trim().toLowerCase()
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
const envCounts = computed(() => {
  const m: Record<string, number> = {}
  for (const c of conns.value) if (passes(c)) m[c.env] = (m[c.env] || 0) + 1
  return m
})
const allTags = ref<string[]>([])
const tagModal = ref<{ open: boolean; conn: Connection | null }>({ open: false, conn: null })
const tagArr = (s: string) => (s ? s.split(',').map((x) => x.trim()).filter(Boolean) : [])
function openTagEdit(c: Connection) { if (!isAdmin.value) return; tagModal.value = { open: true, conn: c } }
async function saveTags(tags: string[]) {
  // M14: 保存标签失败以 toast 呈现
  try {
    if (tagModal.value.conn) await api.setConnectionTags(tagModal.value.conn.id, tags.join(','))
    tagModal.value.open = false
    await load()
    allTags.value = await api.tags()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

// Admins can change an existing instance's gateway policy inline.
async function setPolicy(c: Connection, policy: string) {
  if (!policy || policy === c.policy) return
  try {
    await api.setConnectionPolicy(c.id, policy)
    await load()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

// The environments an instance can be placed in. This was the four built-in
// labels; it comes from the environment list now, so a cluster created in the
// tiers page is selectable here immediately, with no reload and no code change.
//
// Labels stay unique by appending the code when two environments share a display
// name — the select binds on the LABEL, so a duplicate would make one of them
// unpickable.
const envOpts = computed(() => {
  const seen = new Map<string, number>()
  for (const e of envtier.environments) seen.set(e.displayName, (seen.get(e.displayName) ?? 0) + 1)
  return envtier.environments.map((e) => ({
    env: e.code,
    label: (seen.get(e.displayName) ?? 0) > 1 ? `${e.displayName} (${e.code})` : e.displayName,
  }))
})
/**
 * Options for editing ONE instance: the current list, plus the instance's own
 * environment when that no longer exists.
 *
 * Without the extra entry the select falls back to the first option, so opening
 * an instance whose environment was deleted and pressing save would quietly move
 * it into production. Surfacing the dangling code instead makes the operator pick
 * a destination deliberately.
 */
function envOptsFor(code: string) {
  const base = envOpts.value
  return base.some((o) => o.env === code) ? base : [...base, { env: code, label: code }]
}
// Straight from the shared catalogue so the console can only offer engines the
// gateway can actually drive (see lib/engines). ClickHouse and Redis used to be
// listed here with no driver behind them: such a connection saves fine and then
// behaves as a simulated one.
const engineOpts = engineLabels()
// Oracle identifies the target DB by a service name (or a SID via the "sid/" prefix),
// not a plain schema name — hint that in the 数据库名 field placeholder.
const dbHint = (engine: string) => (/oracle/i.test(engine) ? t('dbHintOracle') : 'orders_db')
const policyOpts = ['strict', 'approve-1', 'audit-only']

const blankDraft = () => ({ name: '', host: '', engine: engineOpts[0], envLabel: envOpts.value[0]?.label ?? '', policy: 'strict', username: '', password: '', database: '' })
const draft = ref(blankDraft())

// Creating an instance uses the same modal treatment as editing one: the form
// used to sit permanently at the bottom of the page, below the table, so the
// "new connection" button had nowhere to go and was wired to nothing.
const newModal = ref(false)
const newBusy = ref(false)
function openNew() {
  if (!isAdmin.value) return
  draft.value = blankDraft()
  newModal.value = true
}
// ---- bulk import ----
//
// Each row becomes a real instance the gateway proxies commands to, so the whole
// sheet is parsed and validated BEFORE anything is created (see lib/connectionImport):
// a half-applied sheet that then fails leaves the estate in a state nobody asked
// for. Rows are created through the ordinary connections API, so the server-side
// environment/policy validation and credential encryption all still apply.
const impModal = ref(false)
const impText = ref('')
const impFile = ref<HTMLInputElement>()
const impBusy = ref(false)
const impDone = ref(0)
const impFailures = ref<string[]>([])
// Validate against the environments that actually exist, so a sheet targeting a
// newly created cluster is accepted here instead of only failing at the server.
const impParsed = computed(() => parseConnectionImport(impText.value, envtier.environments.map((e) => e.code)))

function openImport() {
  if (!isAdmin.value) return
  impText.value = ''
  impFailures.value = []
  impDone.value = 0
  impModal.value = true
}

async function pickImportFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  impText.value = await f.text()
  if (impFile.value) impFile.value.value = '' // let the same file be chosen again
}

function downloadTemplate() {
  // The template itself is ASCII, but it exists to be filled in — with Chinese
  // instance names, in Excel. Opened without a byte order mark Excel treats it as
  // ANSI and saves it back that way, and the importer then reads GBK bytes as
  // UTF-8. Shipping the mark makes the whole round trip UTF-8.
  const url = URL.createObjectURL(new Blob([UTF8_BOM + IMPORT_TEMPLATE], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = 'connections-template.csv'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

async function runImport() {
  const rows: ImportRow[] = impParsed.value.rows
  if (!rows.length) { ui.notify(t('impNothing'), 'info'); return }
  impBusy.value = true
  impDone.value = 0
  impFailures.value = []
  let ok = 0
  // Sequential on purpose: each create is a privileged write, and a per-row
  // outcome is far more useful than one aggregate failure from a parallel burst.
  for (const r of rows) {
    try {
      const created = await api.createConnection({
        name: r.name, engine: r.engine, host: r.host, env: r.env as any,
        policy: r.policy, username: r.username, password: r.password, database: r.database,
      })
      try { await api.testConnection(created.id) } catch { /* best-effort attach */ }
      ok++
    } catch (e: any) {
      impFailures.value.push(t('impRowFailed', { line: r.line, name: r.name, msg: e?.message || '' }))
    }
    impDone.value++
  }
  impBusy.value = false
  await load()
  ui.notify(t('impDone', { ok, fail: rows.length - ok }), impFailures.value.length ? 'error' : 'success')
  if (!impFailures.value.length) impModal.value = false
}

// Edit an existing instance in a modal. A blank password keeps the stored one.
const editModal = ref<{ open: boolean; id: number }>({ open: false, id: 0 })
const editDraft = ref(blankDraft())
const editBusy = ref(false)
function openEdit(c: Connection) {
  if (!isAdmin.value) return
  editDraft.value = {
    name: c.name,
    host: `${c.host}:${c.port}`,
    engine: c.engine,
    // Never fall back to the first option — see envOptsFor.
    envLabel: envOptsFor(c.env).find((o) => o.env === c.env)!.label,
    policy: c.policy,
    username: c.username || '',
    password: '',
    database: c.database || '',
  }
  editModal.value = { open: true, id: c.id }
}
async function saveEdit() {
  if (!editDraft.value.name.trim() || !editDraft.value.host.trim()) return
  // A label with no match is the instance's own dangling environment, which
  // envOptsFor added verbatim — so the label IS the code. Keeping it means an
  // untouched save leaves the instance where it was; the server refuses it if
  // that environment is really gone, which is the correct, visible outcome.
  const env = envOpts.value.find((o) => o.label === editDraft.value.envLabel)?.env
    ?? editDraft.value.envLabel
  editBusy.value = true
  try {
    // Update, then test-attach to the gateway (best-effort).
    const updated = await api.updateConnection(editModal.value.id, {
      name: editDraft.value.name.trim(), engine: editDraft.value.engine,
      host: editDraft.value.host.trim(), env: env as any, policy: editDraft.value.policy,
      username: editDraft.value.username.trim(), password: editDraft.value.password, database: editDraft.value.database.trim(),
    })
    try { await api.testConnection(updated.id) } catch { /* best-effort */ }
    editModal.value = { open: false, id: 0 }
    await load()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally {
    editBusy.value = false
  }
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
const envGroups = computed<ConnGroup[]>(() => {
  const byEnv: Record<string, Connection[]> = {}
  for (const c of filtered.value) (byEnv[c.env] ||= []).push(c)

  // 按引擎分桶,并在**组一级**的预算内裁剪:预算跨引擎桶连续消耗,所以
  // "这一组先出 25 行"说的就是 25 行,而不是每个引擎各出 25 行。
  const build = (key: string, rows: Connection[]) => {
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
    return {
      types,
      hidden: rows.length - shown,
      ok: rows.filter((c) => c.status === 'online').length,
      bad: rows.filter((c) => c.status !== 'online').length,
    }
  }

  const groups: ConnGroup[] = envtier.environments.map((e) => {
    const rows = byEnv[e.code] ?? []
    return {
      key: e.code,
      label: envtier.envLabel(e.code),
      cls: envtier.dotForEnv(e.code),
      count: rows.length,
      ...build(e.code, rows),
    }
  })
  const known = new Set(envtier.environments.map((e) => e.code))
  for (const k of Object.keys(byEnv).filter((x) => !known.has(x)).sort()) {
    groups.push({
      key: k, label: `${k} · ${t('envUnknown')}`, cls: 'muted',
      count: byEnv[k].length, ...build(k, byEnv[k]),
    })
  }
  // 过滤态下空组只是噪音;全量视图仍显示空环境(它回答"这个环境还没接入实例")
  if (anyFilter.value || envFilter.value) return groups.filter((g) => g.count > 0)
  return groups
})

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    conns.value = await api.connections()
    try { projects.value = await api.projects() } catch { /* 归属下拉降级为空 */ }
    // 顶栏说这一页是干什么的,数字交给页内那两枚徽标:同一个数在一屏上写两遍,
    // 早晚会有一处忘了改 —— 原来的 subDb 就把"3 环境分层"写死了,而实际是 7 个。
    ui.pageSub = { key: 'connSubLine' }
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
onMounted(async () => {
  // Environments group the table and fill the form's dropdown, so load them
  // before the rows render.
  await envtier.load().catch(() => {})
  await load()
  try { allTags.value = await api.tags() } catch { /* ignore */ }
})

// 网关策略的色档。返回的是**类名**而不是内联色值:内联样式盖不过 :hover /
// :focus,于是那个下拉框在任何状态下都只有一个样子,看着像块死色。
function polCls(p: string) {
  if (p === 'strict') return 'strict'
  if (p === 'audit-only') return 'audit'
  return 'approve'
}

// 地址整串复制。列里是截断显示的 —— 看得见的那一段不等于能粘贴的那一串,
// 所以复制取的是完整值,而不是屏幕上的文本。
const copiedId = ref(0)
let copyTimer: ReturnType<typeof setTimeout> | null = null
async function copyHost(c: Connection) {
  const ok = await copyText(`${c.host}:${c.port}`)
  if (!ok) { ui.notify(t('objCopyFail'), 'error'); return }
  copiedId.value = c.id
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => (copiedId.value = 0), 1600)
  ui.notify(t('copied'), 'success', 1600)
}
function roleColor(r: string) { return r.startsWith('dba') ? '#8facff' : 'var(--text-muted)' }


// ---- 批量选择 ----
//
// 选中集按 id 存,并且**只在当前可见的行**上做全选 —— 一个能把没在屏幕上的行
// 一起选走的复选框,是批量操作里最容易出事的一种。
const sel = ref<Set<number>>(new Set())
const selCount = computed(() => sel.value.size)
const visibleIds = computed(() => {
  const out: number[] = []
  for (const g of envGroups.value) {
    if (isCollapsed(g.key)) continue
    for (const ty of g.types) for (const c of ty.rows) out.push(c.id)
  }
  return out
})
const allVisibleSelected = computed(() =>
  visibleIds.value.length > 0 && visibleIds.value.every((id) => sel.value.has(id)))
function toggleSel(id: number) {
  const next = new Set(sel.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  sel.value = next
}
function toggleSelAll() {
  const next = new Set(sel.value)
  if (allVisibleSelected.value) visibleIds.value.forEach((id) => next.delete(id))
  else visibleIds.value.forEach((id) => next.add(id))
  sel.value = next
}
function clearSel() { sel.value = new Set() }
const selectedConns = computed(() => conns.value.filter((c) => sel.value.has(c.id)))

const batchBusy = ref(false)
const batchPolicy = ref('')

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
const selGuarded = computed(() => selectedConns.value.filter((c) => {
  const tier = envtier.tierOf(c.env)
  return !!tier && (tier.dangerBanner || tier.requireMfa)
}).length)

// 批量改网关策略。改的是**这些实例此后怎么被判**,所以先说清"多少台、其中多少台
// 在生产分层",再让人按确认 —— 一次点错在这里等于把一批生产库的闸门一起挪了。
// 逐台走同一个接口,不另开批量端点:批量入口绕过单台的服务端校验是很常见的漏法。
async function runBatchPolicy() {
  const p = batchPolicy.value
  const rows = selectedConns.value
  if (!p || !rows.length || batchBusy.value) return
  // 没有受管控分层被选中时就不提那一句 —— "其中 0 台"是句废话,而废话读多了
  // 会让人把整段确认文案一起跳过。
  const msg = selGuarded.value
    ? t('connBatchConfirmGuarded', { n: rows.length, p, d: selGuarded.value })
    : t('connBatchConfirm', { n: rows.length, p })
  if (!window.confirm(msg)) return
  batchBusy.value = true
  let ok = 0
  const failed: string[] = []
  try {
    for (const c of rows) {
      if (c.policy === p) { ok++; continue }
      try { await api.setConnectionPolicy(c.id, p); ok++ } catch { failed.push(c.name) }
    }
  } finally {
    batchBusy.value = false
    batchPolicy.value = ''
    await load()
  }
  // 部分失败要点名,不是给个总数了事:没改成的那几台仍然按老策略在跑。
  if (failed.length) ui.notify(t('connBatchPartial', { ok, bad: failed.length, names: failed.slice(0, 3).join(', ') }), 'error', 6000)
  else ui.notify(t('connBatchDone', { ok }), 'success')
}

// 批量巡检:逐台真的连过去试一次。并发限 4 —— 这是往目标库上连,不是本地循环。
const checkResult = ref<Record<number, 'ok' | 'bad'>>({})
async function runBatchCheck() {
  const rows = selectedConns.value.slice()
  if (!rows.length || batchBusy.value) return
  batchBusy.value = true
  checkResult.value = {}
  let ok = 0
  let bad = 0
  const queue = rows.slice()
  const worker = async () => {
    for (;;) {
      const c = queue.shift()
      if (!c) return
      try { await api.testConnection(c.id); checkResult.value = { ...checkResult.value, [c.id]: 'ok' }; ok++ }
      catch { checkResult.value = { ...checkResult.value, [c.id]: 'bad' }; bad++ }
    }
  }
  try { await Promise.all([worker(), worker(), worker(), worker()]) }
  finally { batchBusy.value = false }
  ui.notify(t('connCheckDone', { ok, bad }), bad ? 'error' : 'success', bad ? 6000 : 3500)
  await load()
}

// 立刻同步一台实例的元数据(表清单 + 列结构)。
//
// 它**真的会登录那台库**,所以是一个按钮而不是打开页面就跑。与定时同步的开关无关:
// 开关关着时这个按钮照样可用 —— 手动本来就是一次明确的决定,也是验证凭据和网络
// 通不通最快的办法。
//
// 结果只留在本次会话里,不写进 c 上:它是"刚才这一次同步的结果",和实例的维护态
// 是两件事(与批量巡检的 checkResult 同一个理由)。
const syncBusy = ref<Record<number, boolean>>({})
const syncResult = ref<Record<number, { ok: boolean; text: string }>>({})
async function syncMeta(c: Connection) {
  if (syncBusy.value[c.id]) return
  syncBusy.value = { ...syncBusy.value, [c.id]: true }
  try {
    const r = await api.syncConnectionMetadata(c.id)
    // 服务端回的是"这台实例现在缓存了多少张表"。0 张是个真实的答案,不是失败 ——
    // 但它和"没同步过"读起来一样,所以要说成"0 张表"而不是留空。
    const n = r.sync?.tables ?? r.tables ?? 0
    syncResult.value = { ...syncResult.value, [c.id]: { ok: true, text: t('connMetaTables', { n }) } }
    ui.notify(t('connMetaSyncOk', { name: c.name, n }), 'success')
  } catch (e) {
    // 失败原因来自服务端(连不上 / 没权限 / 未配凭据),照原样显示。
    // 换成一句"同步失败"会把这三种完全不同的处置方式压成同一句话。
    syncResult.value = { ...syncResult.value, [c.id]: { ok: false, text: t('connMetaSyncBad') } }
    // notifyError 自己会从抛出的错误里取服务端那句话,兜底文案只在取不到时用。
    ui.notifyError(e, t('connMetaSyncBad'))
  } finally {
    syncBusy.value = { ...syncBusy.value, [c.id]: false }
  }
}

// 导出所选配置。**不含凭据** —— 口令在库里是加密的,而一份能落到下载目录里的
// CSV 不该是把它们带出网关的那条路。
function exportSelected() {
  const rows = selectedConns.value
  if (!rows.length) return
  const esc = (v: unknown) => {
    const x = String(v ?? '')
    return /[",\n]/.test(x) ? `"${x.replace(/"/g, '""')}"` : x
  }
  const head = ['name', 'engine', 'env', 'host', 'port', 'database', 'policy', 'defaultRole', 'status', 'tags']
  const body = rows.map((c) => [c.name, c.engine, c.env, c.host, c.port, c.database, c.policy, c.defaultRole, c.status, c.tags].map(esc).join(','))
  // BOM:这份表是要在 Excel 里打开的,不带的话中文实例名会读成乱码。
  const url = URL.createObjectURL(new Blob([UTF8_BOM + [head.join(','), ...body].join('\n')], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `connections-${rows.length}.csv`
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
  ui.notify(t('connExportDone', { n: rows.length }), 'success')
}

async function toggle(c: Connection) {
  if (!isAdmin.value) return
  // M14: 切换状态失败以 toast 呈现
  try {
    await api.toggleConnection(c.id)
    await load()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

async function add() {
  if (!draft.value.name.trim() || !draft.value.host.trim()) return
  const env = envOpts.value.find((o) => o.label === draft.value.envLabel)?.env || 'prod'
  const body = {
    name: draft.value.name.trim(), engine: draft.value.engine,
    host: draft.value.host.trim(), env: env as any, policy: draft.value.policy,
    username: draft.value.username.trim(), password: draft.value.password, database: draft.value.database.trim(),
  }
  // M14: 失败以 toast 呈现
  newBusy.value = true
  try {
    // "测试连接并保存" (FR-CONN-02): create then test-attach to the gateway.
    const created = await api.createConnection(body)
    try { await api.testConnection(created.id) } catch { /* best-effort */ }
    draft.value = blankDraft()
    newModal.value = false
    await load()
    ui.notify(t('connSaved'), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally {
    newBusy.value = false
  }
}
</script>

<template>
  <div class="scy page" :class="{ hasfab: selCount > 0 }">
    <div class="head">
      <div class="hleft">
        <div class="eyebrow">CONNECTIONS</div>
        <!-- 规模先用两枚轻标签交代("多少台、几种环境"),再说这一页是干嘛的。
             数字混在一句话里时,要数一眼看不出来。 -->
        <div class="hbadges">
          <span class="hbadge">{{ $t('connBadgeInst', { n: conns.length }) }}</span>
          <span class="hbadge">{{ $t('connBadgeEnv', { n: envtier.environments.length }) }}</span>
        </div>
      </div>
      <div class="acts">
        <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
        <template v-else>
          <VButton variant="secondary" @click="openImport"><Upload :size="14" />{{ $t('importInst') }}</VButton>
          <VButton variant="primary" @click="openNew"><Plus :size="15" />{{ $t('newConn') }}</VButton>
        </template>
      </div>
    </div>

    <div class="toolbar">
      <div class="searchbox">
        <Search :size="14" />
        <input v-model="qInput" class="sin" :placeholder="$t('connSearchPh')" />
        <X v-if="qInput" :size="13" class="sclear" @click="clearSearch" />
      </div>
      <!-- 排障时的两个快捷条件。只列**现有实例真的在用**的引擎,不摆一整本目录。 -->
      <select v-model="statusFilter" class="fsel" :title="$t('colStatus')">
        <option value="">{{ $t('connFltStatusAll') }}</option>
        <option value="bad">{{ $t('connFltStatusBad') }}</option>
      </select>
      <select v-model="engineFilter" class="fsel" :title="$t('colEngine')">
        <option value="">{{ $t('connFltEngineAll') }}</option>
        <option v-for="e in engineFilterOpts" :key="e" :value="e">{{ e }}</option>
      </select>
      <!-- 分段控制器:一整条凹槽,选中的那一段浮起来。散着的胶囊看不出"这几个
           是同一组、只能选一个";凹槽把它们收成一件东西。 -->
      <div class="seg">
        <button class="segi" :class="{ on: envFilter === '' }" @click="envFilter = ''">
          {{ $t('connAllEnv') }}<i>{{ filtered.length }}</i>
        </button>
        <button v-for="e in envtier.environments" :key="e.code" class="segi" :class="{ on: envFilter === e.code }"
          @click="pickEnv(e.code)">
          <!-- 圆点带的是**分层**的颜色,与树、审批列表同一个来源。它不随选中态变,
               因为"这个环境有多危险"和"我现在筛的是不是它"是两件事。 -->
          <span class="ed" :class="envtier.dotForEnv(e.code)" />{{ envtier.envLabel(e.code) }}<i>{{ envCounts[e.code] || 0 }}</i>
        </button>
      </div>

      <div class="tbright">
        <button class="tbtn" :title="allCollapsed ? $t('connExpandAll') : $t('connCollapseAll')"
                @click="allCollapsed ? expandAll() : collapseAll()">
          <component :is="allCollapsed ? ChevronsUpDown : ChevronsDownUp" :size="14" />
          {{ allCollapsed ? $t('connExpandAll') : $t('connCollapseAll') }}
        </button>
        <!-- 密度开关。紧凑模式收起副标题与标签行 —— 那些在排障时是噪音,
             在配置时才是内容,所以是个开关而不是一个决定。 -->
        <div class="seg dens">
          <button class="segi" :class="{ on: !dense }" :title="$t('connCozy')" @click="setDense(false)"><Rows2 :size="13" /></button>
          <button class="segi" :class="{ on: dense }" :title="$t('connDense')" @click="setDense(true)"><Rows3 :size="13" /></button>
        </div>
      </div>
    </div>

    <!-- 卡片容器 + 内层最小宽度:窄屏时整张表横向滚动,而不是把地址挤成三行。
         滚动条挂在卡片上,表头和数据行在同一个滚动上下文里,不会各滚各的。 -->
    <div ref="tableEl" class="table" :class="{ adm: isAdmin, dense }">
      <div class="tscroll">
      <div class="thead">
        <span v-if="isAdmin" class="cbcell">
          <input type="checkbox" class="cb" :checked="allVisibleSelected" :title="$t('connSelAllVisible')" @change="toggleSelAll" />
        </span>
        <span>{{ $t('colInst') }}</span><span>{{ $t('colEngine') }}</span><span>{{ $t('colAddr') }}</span>
        <span>{{ $t('colRole') }}</span><span>{{ $t('colPolicy') }}</span><span>{{ $t('colStatus') }}</span>
        <span class="tar">{{ $t('apActions') }}</span>
      </div>

      <template v-for="group in envGroups" :key="group.key">
        <!-- 分组条不再整条染色。原来一整条淡红压在生产环境上,红色在这一页是
             "危险"的意思,而"这里是生产"不该长期占着那个信号。颜色收进左边那颗
             圆点里,条子本身是中性的浅底。 -->
        <div class="grouprow click" @click="toggleGroup(group.key)">
          <ChevronDown :size="13" class="chev" :class="{ closed: isCollapsed(group.key) }" />
          <span class="d" :class="group.cls" />
          <span class="glabel">{{ group.label }}</span>
          <span class="gcnt">{{ $t('connGroupCount', { n: group.count }) }}</span>
          <!-- 不展开也能看出这个环境有没有事。异常为 0 时只说"全部正常",
               把一个恒定的红色 0 挂在那里,久了谁都不看了。 -->
          <span v-if="group.count" class="ghealth" :class="{ bad: group.bad > 0 }">
            <span class="hd ok" />{{ group.ok }}
            <template v-if="group.bad"><span class="hd bad" />{{ group.bad }}</template>
          </span>
        </div>
        <template v-if="!isCollapsed(group.key)">
        <template v-for="ty in group.types" :key="ty.key">
        <!-- Second level: database type. The engine decides which driver reaches
             the instance and how its commands are read, so it groups rather than
             sitting in a column to be scanned for. -->
        <div v-if="ty.rows.length" class="typerow">{{ ty.label }}<span class="gcnt">{{ ty.total }}</span></div>
        <template v-for="c in ty.rows" :key="c.id">
        <div class="trow" :class="{ sel: sel.has(c.id) }">
          <div v-if="isAdmin" class="cbcell">
            <input type="checkbox" class="cb" :checked="sel.has(c.id)" @change="toggleSel(c.id)" />
          </div>
          <div class="namecell">
            <div class="cn"><span v-for="(p, i) in hi(c.name)" :key="i" :class="{ hit: p.on }">{{ p.t }}</span></div>
            <div v-if="!dense" class="cl">{{ c.layer }}</div>
            <div v-if="!dense" class="tags">
              <span class="rbadge" :class="c.username ? 'real' : 'sim'">{{ c.username ? $t('connReal') : $t('connSim') }}</span>
              <!-- 归属是**按库**定的,所以入口在这里展开,而不是行上一个下拉:
                   一个实例底下的几个库可以分属不同项目。与访问标签刻意分开显示。 -->
              <span class="dbtoggle" :class="{ on: openDbs[c.id] }" @click="toggleDbs(c)">
                <Database :size="10" />{{ $t('connDbProjects') }}
              </span>
              <span v-for="pn in projectsOn(c)" :key="pn" class="prchip">{{ pn }}</span>
              <span v-for="tg in tagArr(c.tags)" :key="tg" class="tchip">{{ tg }}</span>
              <span v-if="isAdmin" class="tedit" @click="openTagEdit(c)"><Tag :size="10" />{{ tagArr(c.tags).length ? $t('edit') : $t('tagAdd') }}</span>
            </div>
          </div>
          <div class="engcell"><span class="eic"><Database :size="12" /></span>{{ engineDisplay(c.engine) }}</div>
          <!-- 地址一行到底:截断 + 完整值进 title,右边一颗常驻的复制按钮。
               这一串是拿去粘到别处用的,能看见不等于能拿走。 -->
          <div class="hostcell">
            <span class="hosttxt" :title="`${c.host}:${c.port}`">
              <span v-for="(p, i) in hi(`${c.host}:${c.port}`)" :key="i" :class="{ hit: p.on }">{{ p.t }}</span>
            </span>
            <button class="ghost cp" :title="copiedId === c.id ? $t('copied') : $t('copy')" @click="copyHost(c)">
              <component :is="copiedId === c.id ? Check : Copy" :size="12" />
            </button>
          </div>
          <div class="mono" :style="{ color: roleColor(c.defaultRole) }">{{ c.defaultRole }}</div>
          <div>
            <select v-if="isAdmin" class="polsel" :class="polCls(c.policy)" :value="c.policy" :title="$t('fPolicy')" @change="setPolicy(c, ($event.target as HTMLSelectElement).value)">
              <option v-for="p in policyOpts" :key="p" :value="p">{{ p }}</option>
            </select>
            <span v-else class="polpill" :class="polCls(c.policy)">{{ c.policy }}</span>
          </div>
          <!-- 状态回到"一颗点 + 两个字"。它此前是个填色胶囊,和同一行里的策略
               胶囊长得一样重,而两者一个是事实、一个是配置。 -->
          <div>
            <span class="st" :class="[c.status, { click: isAdmin }]" :title="isAdmin ? $t('tipToggleStatus') : ''" @click="toggle(c)">
              <span class="sdot" />{{ c.status === 'online' ? $t('online') : $t('maint') }}
            </span>
            <!-- 巡检结论只在本次巡检里存在,不写库:它是"刚才连通了吗",
                 和实例的维护态是两件事。 -->
            <span v-if="checkResult[c.id]" class="ck" :class="checkResult[c.id]">
              {{ checkResult[c.id] === 'ok' ? $t('connCheckOk') : $t('connCheckBad') }}
            </span>
          </div>
          <div class="opscell">
            <!-- 同步结果只在本次会话里存在,和巡检结论同一个理由 -->
            <span v-if="syncResult[c.id]" class="ck" :class="syncResult[c.id].ok ? 'ok' : 'bad'">{{ syncResult[c.id].text }}</span>
            <button v-if="isAdmin" class="ghost" :title="$t('connMetaSync')" :disabled="syncBusy[c.id]" @click="syncMeta(c)">
              <DatabaseZap :size="14" :class="{ spin: syncBusy[c.id] }" />
            </button>
            <button v-if="isAdmin" class="ghost" :title="$t('connEdit')" @click="openEdit(c)"><Pencil :size="14" /></button>
          </div>
        </div>
        <!-- 库一级:归属定在这里 -->
        <div v-if="openDbs[c.id]" class="dbpanel">
          <div v-if="dbsBusy[c.id]" class="dbnote">{{ $t('schemaLoading') }}</div>
          <div v-else-if="dbsErr[c.id]" class="dbnote err">{{ dbsErr[c.id] }}</div>
          <div v-else-if="!(dbsOf[c.id] || []).length" class="dbnote">{{ $t('schemaEmpty') }}</div>
          <div v-for="db in dbsOf[c.id] || []" :key="db.name" class="dbrow">
            <span class="dbname">{{ db.name }}</span>
            <select v-if="isAdmin" class="prsel" :value="db.projectId || 0" :title="$t('connProject')"
                    @change="setDbProject(c, db, Number(($event.target as HTMLSelectElement).value))">
              <option :value="0">{{ $t('connNoProject') }}</option>
              <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <span v-else-if="db.projectId" class="prchip">{{ projectName(db.projectId) }}</span>
            <span v-else class="dbnone">{{ $t('connNoProject') }}</span>
          </div>
        </div>
        </template>
        </template>
        <div v-if="!group.types.length" class="typerow empty">{{ $t('connGroupEmpty') }}</div>
        <!-- 预算之外的行不进 DOM。这一页每行都带着标签、下拉框和可展开的库面板,
             一次铺开的不是 N 行文本,而是上千个节点。 -->
        <div v-if="group.hidden > 0" class="morerow">
          <button class="mbtn" @click="showMore(group.key)">{{ $t('connShowMore', { n: group.hidden }) }}</button>
        </div>
        </template>
      </template>
      <div v-if="!envGroups.some((g) => g.count > 0)" class="noresult">{{ $t('connNoMatch') }}</div>
      </div>
    </div>

    <!-- 悬浮批量操作条。只在选中后出现,并且始终显示"选了几台" ——
         批量动作最怕的是不知道自己正在对多少东西下手。 -->
    <Teleport to="body">
      <div v-if="selCount" class="fab">
        <span class="fcnt">{{ $t('connSelected', { n: selCount }) }}</span>
        <span v-if="selGuarded" class="fprod">{{ $t('connSelGuarded', { n: selGuarded }) }}</span>
        <select v-model="batchPolicy" class="fsel" :disabled="batchBusy" @change="runBatchPolicy">
          <option value="">{{ $t('connBatchPolicy') }}</option>
          <option v-for="p in policyOpts" :key="p" :value="p">{{ p }}</option>
        </select>
        <button class="fbtn" :disabled="batchBusy" @click="runBatchCheck"><Activity :size="13" />{{ $t('connBatchCheck') }}</button>
        <button class="fbtn" :disabled="batchBusy" @click="exportSelected"><Download :size="13" />{{ $t('connBatchExport') }}</button>
        <button class="fbtn ghosty" :disabled="batchBusy" @click="clearSel"><X :size="13" />{{ $t('connSelClear') }}</button>
      </div>
    </Teleport>

    <TagEditModal
      :open="tagModal.open" :title="$t('tagConnTitle')" :subtitle="tagModal.conn ? tagModal.conn.name : ''"
      :tags="tagArr(tagModal.conn?.tags || '')" :suggestions="allTags"
      @close="tagModal.open = false" @save="saveTags"
    />

    <!-- Bulk-import modal -->
    <Teleport to="body">
      <div v-if="impModal" class="ce-mask" @click.self="impBusy || (impModal = false)">
        <div class="ce-card imp">
          <div class="ce-head">
            <div class="ce-title">{{ $t('impTitle') }}</div>
            <div class="ce-sub">{{ $t('impSub') }}</div>
          </div>
          <div class="ce-body">
            <div class="imp-bar">
              <VButton variant="secondary" @click="impFile?.click()">{{ $t('impPick') }}</VButton>
              <VButton variant="secondary" @click="downloadTemplate">{{ $t('impTemplate') }}</VButton>
              <input ref="impFile" type="file" accept=".csv,text/csv,text/plain" hidden @change="pickImportFile" />
            </div>
            <div class="fl">{{ $t('impPaste') }}</div>
            <textarea v-model="impText" class="imp-ta" spellcheck="false" :placeholder="IMPORT_TEMPLATE"></textarea>
            <div class="imp-hint">{{ $t('impCols') }}</div>

            <div v-if="impParsed.rows.length" class="imp-ok">{{ $t('impPreview', { n: impParsed.rows.length }) }}</div>
            <div v-if="impParsed.errors.length" class="imp-bad">
              <div class="imp-bad-t">{{ $t('impErrTitle', { n: impParsed.errors.length }) }}</div>
              <div v-for="(e, i) in impParsed.errors" :key="i" class="imp-bad-l">{{ e.message }}</div>
            </div>
            <div v-if="impFailures.length" class="imp-bad">
              <div v-for="(f, i) in impFailures" :key="'f' + i" class="imp-bad-l">{{ f }}</div>
            </div>
          </div>
          <div class="ce-foot">
            <VButton variant="secondary" :disabled="impBusy" @click="impModal = false">{{ $t('mCancel') }}</VButton>
            <VButton variant="primary" :disabled="impBusy || !impParsed.rows.length" @click="runImport">
              {{ impBusy ? $t('impRunning', { done: impDone, total: impParsed.rows.length }) : $t('impRun') }}
            </VButton>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- New-instance modal (same shape as the edit one) -->
    <Teleport to="body">
      <div v-if="newModal" class="ce-mask" @click.self="newModal = false">
        <div class="ce-card">
          <div class="ce-head"><div class="ce-title">{{ $t('formNewConn') }}</div><div class="ce-sub">{{ $t('formNewConnSub') }}</div></div>
          <div class="ce-body">
            <div class="fgrid">
              <div><div class="fl">{{ $t('fName') }}</div><input v-model="draft.name" placeholder="order-cluster-2" /></div>
              <div><div class="fl">{{ $t('fEngine') }}</div><VSelect v-model="draft.engine" :options="engineOpts" /></div>
              <div><div class="fl">{{ $t('fEnv') }}</div><VSelect v-model="draft.envLabel" :options="envOpts.map((o) => o.label)" /></div>
              <div><div class="fl">{{ $t('fAddr') }}</div><input v-model="draft.host" placeholder="10.20.3.12:3306" /></div>
              <div><div class="fl">{{ $t('fPolicy') }}</div><VSelect v-model="draft.policy" :options="policyOpts" /></div>
              <div><div class="fl">{{ $t('fDatabase') }}</div><input v-model="draft.database" :placeholder="dbHint(draft.engine)" /></div>
              <div><div class="fl">{{ $t('fUser') }}</div><input v-model="draft.username" placeholder="app_ro" /></div>
              <div><div class="fl">{{ $t('fPassword') }}</div><input v-model="draft.password" type="password" placeholder="••••••" /></div>
            </div>
            <div class="credhint">{{ $t('connCredHint') }}</div>
          </div>
          <div class="ce-foot">
            <VButton variant="secondary" @click="newModal = false">{{ $t('mCancel') }}</VButton>
            <VButton variant="primary" :disabled="newBusy" @click="add">{{ newBusy ? $t('connTestSaving') : $t('connTestSave') }}</VButton>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Edit-instance modal -->
    <Teleport to="body">
      <div v-if="editModal.open" class="ce-mask" @click.self="editModal.open = false">
        <div class="ce-card">
          <div class="ce-head"><div class="ce-title">{{ $t('formEditConn') }}</div><div class="ce-sub">{{ $t('formEditConnSub') }}</div></div>
          <div class="ce-body">
            <div class="fgrid">
              <div><div class="fl">{{ $t('fName') }}</div><input v-model="editDraft.name" placeholder="order-cluster-2" /></div>
              <div><div class="fl">{{ $t('fEngine') }}</div><VSelect v-model="editDraft.engine" :options="engineOpts" /></div>
              <div><div class="fl">{{ $t('fEnv') }}</div><VSelect v-model="editDraft.envLabel" :options="envOpts.map((o) => o.label)" /></div>
              <div><div class="fl">{{ $t('fAddr') }}</div><input v-model="editDraft.host" placeholder="10.20.3.12:3306" /></div>
              <div><div class="fl">{{ $t('fPolicy') }}</div><VSelect v-model="editDraft.policy" :options="policyOpts" /></div>
              <div><div class="fl">{{ $t('fDatabase') }}</div><input v-model="editDraft.database" :placeholder="dbHint(editDraft.engine)" /></div>
              <div><div class="fl">{{ $t('fUser') }}</div><input v-model="editDraft.username" placeholder="app_ro" /></div>
              <div><div class="fl">{{ $t('fPassword') }}</div><input v-model="editDraft.password" type="password" :placeholder="$t('fPasswordKeep')" /></div>
            </div>
            <div class="credhint">{{ $t('connCredHint') }}</div>
          </div>
          <div class="ce-foot">
            <VButton variant="secondary" @click="editModal.open = false">{{ $t('mCancel') }}</VButton>
            <VButton variant="primary" :disabled="editBusy" @click="saveEdit">{{ editBusy ? $t('connTestSaving') : $t('connTestSave') }}</VButton>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
/* 悬浮条会盖住最后一两行 —— 给页面垫一段底,让它盖的是空白而不是数据。 */
.page.hasfab { padding-bottom: calc(var(--page-pad) + 72px); }
.head { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 16px; }
.hleft { min-width: 0; }
.hbadges { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 7px; }
/* 轻标签:只是把两个数字从句子里拎出来,不是状态,所以不带语义色。 */
.hbadge {
  display: inline-flex; align-items: center; height: 22px; padding: 0 9px;
  border-radius: var(--radius-full); background: var(--surface-sunken);
  border: 1px solid var(--border-subtle); color: var(--text-muted);
  font: 600 11px var(--font-mono);
}
.prpanel { margin-bottom: 16px; }
.dbtoggle { display: inline-flex; align-items: center; gap: 3px; padding: 1px 7px; border-radius: 5px; border: 1px dashed var(--border-strong); color: var(--text-muted); font: 600 10px var(--font-body); cursor: pointer; }
.dbtoggle:hover, .dbtoggle.on { border-style: solid; border-color: var(--accent-text); color: var(--accent-text); }
.dbpanel { margin: 0 0 10px 18px; padding: 8px 12px; border-left: 2px solid var(--border-default); display: flex; flex-direction: column; gap: 6px; }
.dbrow { display: flex; align-items: center; gap: 10px; }
.dbname { min-width: 180px; font: 600 11.5px var(--font-mono); color: var(--text-body); }
.dbnone { font: 500 10.5px var(--font-body); color: var(--text-faint); }
.dbnote { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.dbnote.err { color: var(--danger-text); }
.prsel { height: 20px; max-width: 150px; padding: 0 4px; border: 1px solid var(--accent-text); border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-body); cursor: pointer; }
.prchip { padding: 1px 7px; border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-body); }
.toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 14px; }
/* 260–320px 之间伸缩:窄屏让给筛选器,宽屏也不无谓拉长 —— 搜的是实例名,不是句子。 */
.searchbox {
  display: flex; align-items: center; gap: 8px; flex: 0 1 320px; min-width: 260px;
  padding: 0 12px; height: 36px; border: 1px solid var(--border-default);
  border-radius: var(--radius-md); background: var(--surface-card); color: var(--text-faint);
}
.searchbox:focus-within { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
/* 快捷过滤下拉:和搜索框同高,但更窄更安静 —— 它们是副条件。 */
.fsel {
  height: 36px; padding: 0 10px; border: 1px solid var(--border-default);
  border-radius: var(--radius-md); background: var(--surface-card); color: var(--text-body);
  font: 500 12px var(--font-body); cursor: pointer; outline: none;
}
.fsel:focus-visible { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--focus-ring); }
.tbright { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.tbtn {
  display: inline-flex; align-items: center; gap: 6px; height: 34px; padding: 0 11px;
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-card); color: var(--text-muted);
  font: 600 11.5px var(--font-body); cursor: pointer;
}
.tbtn:hover { color: var(--accent-text); border-color: var(--accent-text); }
.seg.dens { padding: 3px; }
.seg.dens .segi { padding: 0 9px; height: 28px; }
.sin { flex: 1; min-width: 0; border: none; outline: none; background: transparent; font: 500 12.5px var(--font-body); color: var(--text-strong); }
.sclear { cursor: pointer; color: var(--text-muted); }
/* 分段控制器。凹槽一条,选中的那一段用卡片色浮起来 + 一点投影 —— 这是"同一组、
   只能选一个"的读法。环境是运维自建的,数量不定,所以允许换行而不是硬挤。 */
.seg {
  display: flex; align-items: center; gap: 3px; flex-wrap: wrap;
  padding: 3px; border-radius: var(--radius-md);
  background: var(--surface-sunken); border: 1px solid var(--border-subtle);
}
.segi {
  display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 11px;
  border: none; border-radius: var(--radius-sm); background: transparent;
  font: 600 11.5px var(--font-body); color: var(--text-muted);
  cursor: pointer; user-select: none;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.segi:hover { color: var(--text-body); }
.segi.on { background: var(--surface-card); color: var(--text-strong); box-shadow: var(--shadow-xs); }
.segi i { font: 600 10px var(--font-mono); font-style: normal; color: var(--text-faint); }
.segi.on i { color: var(--accent-text); }
.segi .ed { width: 6px; height: 6px; border-radius: 50%; background: var(--text-faint); }
.segi .ed.danger { background: var(--danger); }
.segi .ed.warning { background: var(--warning); }
.segi .ed.success { background: var(--success); }
.segi .ed.info { background: #3b82f6; }
.grouprow.click { cursor: pointer; user-select: none; }
.grouprow.click:hover { background: var(--surface-raised); }
.chev { transition: transform var(--dur-fast) var(--ease-out); color: var(--text-faint); }
.chev.closed { transform: rotate(-90deg); }
.noresult { padding: 36px 0; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-faint); }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.acts { margin-left: auto; display: flex; gap: 10px; align-items: center; flex-shrink: 0; }
.acts :deep(.vbtn) { display: inline-flex; align-items: center; gap: 6px; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.table {
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg);
  background: var(--surface-card); box-shadow: var(--shadow-sm);
  overflow-x: auto; overflow-y: hidden;
}
/* 表头与数据行是**两个各自独立的 grid**,所以列宽只能用与内容无关的值(fr / px)。
   写 auto / min-content 会让每一行按自己那行的内容各算一套,分栏当场错开。 */
.tscroll { min-width: 1000px; }
/* 管理员多一列复选框;只读用户没有批量动作,那一列也就不该占位置。 */
.thead, .trow { display: grid; grid-template-columns: 1.6fr 0.9fr 1.8fr 0.85fr 0.85fr 0.7fr 64px; gap: 12px; }
.table.adm .thead, .table.adm .trow { grid-template-columns: 34px 1.6fr 0.9fr 1.8fr 0.85fr 0.85fr 0.7fr 64px; }
.tar { text-align: right; }
.cbcell { display: flex; align-items: center; }
.cb { width: 14px; height: 14px; margin: 0; cursor: pointer; accent-color: var(--accent-text); }
.trow.sel { background: var(--accent-subtle); }
.namecell { min-width: 0; }
/* 命中高亮。片段是切好的文本节点,不走 v-html —— 实例名和 host 是别人填进库里的。 */
.hit { border-radius: 3px; padding: 0 1px; background: var(--warning-subtle); color: var(--warning-text); }

/* 紧凑模式:行高压到 40px,副标题与标签行整行不渲染(不是藏起来)。 */
.table.dense .trow { padding: 5px 16px; }
.table.dense .cn { font: 600 12px var(--font-body); }
.table.dense .engcell, .table.dense .mono { font-size: 11.5px; }
.table.dense .eic { width: 18px; height: 18px; }
.table.dense .polsel, .table.dense .polpill { height: 21px; }
.table.dense .ghost { width: 22px; height: 22px; }
.table.dense .grouprow { padding: 7px 16px; }
.table.dense .typerow { padding-top: 4px; padding-bottom: 4px; }
.thead { padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 10.5px var(--font-mono); letter-spacing: 0.07em; color: var(--text-faint); text-transform: uppercase; }
.trow { padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; transition: background var(--dur-fast) var(--ease-out); }
.trow:hover { background: var(--surface-sunken); }
/* 折叠条:中性浅底 + 上下细线,像个小节标题,而不是一条警示带。 */
.grouprow {
  padding: 9px 16px; display: flex; align-items: center; gap: 9px;
  background: var(--surface-sunken);
  border-top: 1px solid var(--border-subtle); border-bottom: 1px solid var(--border-subtle);
  font: 600 11.5px var(--font-body); color: var(--text-body);
}
.glabel { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.grouprow .gcnt { margin-left: 0; }
.grouprow .ghealth { margin-left: auto; }
/* 颜色只剩这一颗点。来源仍是 envtier.dotForEnv —— 与树、审批列表同一套,
   不在这一页另配一份。 */
.grouprow .d { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--text-faint); }
.grouprow .d.danger { background: var(--danger); }
/* `warn` 是旧类名;分层调色板发出来的是 `warning`。 */
.grouprow .d.warn, .grouprow .d.warning { background: var(--warning); }
.grouprow .d.success { background: var(--success); }
.grouprow .d.info { background: #3b82f6; }
/* 解析不出分层的环境:中性 —— 谁也不能替它声称一个管控级别。 */
.grouprow .d.muted { background: var(--text-faint); }
/* 健康概况:两颗小点 + 数字。全部正常时不摆一个恒定的红色 0 —— 常年为零的
   告警数字,过一阵谁都不看了。 */
.ghealth { display: inline-flex; align-items: center; gap: 5px; margin-left: 10px; font: 600 10.5px var(--font-mono); color: var(--text-faint); }
.ghealth .hd { width: 6px; height: 6px; border-radius: 50%; }
.ghealth .hd.ok { background: var(--success); }
.ghealth .hd.bad { background: var(--danger); margin-left: 4px; }
.ghealth.bad { color: var(--danger-text); }
.ck { display: inline-flex; align-items: center; height: 18px; padding: 0 7px; border-radius: var(--radius-full); font: 700 9.5px var(--font-mono); }
.ck.ok { background: var(--success-subtle); color: var(--success-text); }
.ck.bad { background: var(--danger-subtle); color: var(--danger-text); }
.morerow { padding: 10px 16px; display: flex; justify-content: center; border-bottom: 1px solid var(--border-subtle); }
.mbtn {
  height: 28px; padding: 0 14px; border: 1px solid var(--border-default); border-radius: var(--radius-full);
  background: var(--surface-card); color: var(--text-muted); font: 600 11.5px var(--font-body); cursor: pointer;
}
.mbtn:hover { color: var(--accent-text); border-color: var(--accent-text); background: var(--accent-subtle); }
.gcnt {
  margin-left: auto; flex-shrink: 0; display: inline-flex; align-items: center;
  height: 19px; padding: 0 8px; border-radius: var(--radius-full);
  background: var(--surface-card); border: 1px solid var(--border-subtle);
  font: 600 10px var(--font-mono); color: var(--text-muted);
}
/* Second-level group: quieter than the environment row, so the environment stays
   the structure the eye follows down the table. */
.typerow {
  padding: 5px 16px 5px 32px; display: flex; align-items: center;
  font: 600 10px var(--font-mono); letter-spacing: 0.06em; text-transform: uppercase;
  color: var(--text-faint); background: transparent;
  border-bottom: 1px solid var(--border-subtle);
}
.typerow .gcnt { margin-left: 8px; height: 17px; padding: 0 7px; background: var(--surface-sunken); }
.typerow.empty { padding-left: 16px; font-style: italic; text-transform: none; letter-spacing: 0; }
.cn { font: 700 13px var(--font-body); color: var(--text-strong); letter-spacing: -0.01em; }
.cl { margin-top: 1px; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.tags { margin-top: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: 5px; }
/* 访问标签是中性的:一行里已经有真连接/仿真、归属项目、网关策略三种带色的东西,
   标签再上色就只剩一片花。项目片保留强调色 —— 它是这一堆里唯一可点进去的线索。 */
.tchip { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: var(--radius-full); background: var(--surface-sunken); border: 1px solid var(--border-subtle); color: var(--text-muted); font: 600 10px var(--font-mono); }
.rbadge { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: 999px; font: 700 10px var(--font-mono); }
.rbadge.real { background: var(--success-subtle); color: var(--success-text); }
.rbadge.sim { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.tedit { display: inline-flex; align-items: center; gap: 3px; height: 19px; padding: 0 7px; border-radius: 999px; border: 1px dashed var(--border-default); color: var(--text-faint); font: 600 10px var(--font-mono); cursor: pointer; }
.tedit:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.mono { font: 500 12px var(--font-mono); color: var(--text-body); }
.engcell { display: flex; align-items: center; gap: 7px; min-width: 0; font: 600 12px var(--font-body); color: var(--text-body); }
.eic { width: 20px; height: 20px; flex-shrink: 0; border-radius: var(--radius-sm); display: grid; place-items: center; background: var(--surface-sunken); color: var(--text-muted); }
.hostcell { display: flex; align-items: center; gap: 4px; min-width: 0; }
/* 截断而不是折行:一条 60 字符的 RDS 域名折起来能把整行撑成三倍高,而这一列
   的作用是"认出是哪台",完整值给 title 和复制按钮。 */
.hosttxt { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 500 11.5px var(--font-mono); color: var(--text-muted); }
/* 网关策略:紧凑的小 select,底色只到"淡"为止。strict 用的是同一枚玫瑰淡底,
   而不是一块饱和红 —— 这一列是**配置**,不是告警。 */
.polsel, .polpill {
  display: inline-flex; align-items: center; height: 24px; max-width: 100%;
  padding: 0 8px; border-radius: var(--radius-full);
  font: 600 10.5px var(--font-mono); outline: none;
}
.polsel { cursor: pointer; border: 1px solid transparent; -webkit-appearance: none; appearance: none; padding-right: 8px; }
.polsel:focus-visible { box-shadow: 0 0 0 3px var(--focus-ring); }
.polsel option { background: var(--surface-card); color: var(--text-body); }
.polsel.strict, .polpill.strict { background: var(--danger-subtle); color: var(--danger-text); border-color: var(--danger-subtle-border, transparent); }
.polsel.approve, .polpill.approve { background: var(--warning-subtle); color: var(--warning-text); }
.polsel.audit, .polpill.audit { background: var(--surface-sunken); color: var(--text-muted); border-color: var(--border-subtle); }
.opscell { display: flex; align-items: center; justify-content: flex-end; gap: 6px; }
.opscell .ghost:disabled { opacity: .55; cursor: default; }
/* 同步时让图标转起来 —— 一个点下去没有任何反应的按钮会被连点,
   而每一次点击都是一次真的登录目标库。 */
.spin { animation: metaspin 1s linear infinite; }
@keyframes metaspin { to { transform: rotate(360deg); } }
/* 状态:一颗点 + 两个字。点会呼吸,但只在"在线"时 —— 呼吸表示"还活着",
   而维护态恰恰是不动的那个。 */
.st { display: inline-flex; align-items: center; gap: 6px; font: 600 11.5px var(--font-body); color: var(--text-muted); }
.st.click { cursor: pointer; }
.st .sdot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--text-faint); }
.st.online { color: var(--success-text); }
.st.online .sdot { background: var(--success); animation: breathe 2.4s ease-in-out infinite; }
.st.maintenance { color: var(--warning-text); }
.st.maintenance .sdot { background: var(--warning); }
@keyframes breathe { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
@media (prefers-reduced-motion: reduce) { .st.online .sdot { animation: none; } }
/* 幽灵图标按钮:常态弱,悬停才亮起来并托一层圆底。 */
.ghost {
  width: 26px; height: 26px; flex-shrink: 0; display: inline-flex; align-items: center; justify-content: center;
  border: none; border-radius: var(--radius-full); background: transparent;
  color: var(--text-faint); cursor: pointer;
  transition: color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.ghost:hover { color: var(--accent-text); background: var(--accent-subtle); }
.ghost.cp { width: 22px; height: 22px; opacity: 0; }
/* 复制按钮平时不显形,免得每行右边都挂一个图标;悬停整行、或键盘聚焦时出现。 */
.trow:hover .ghost.cp, .ghost.cp:focus-visible { opacity: 1; }
/* edit-instance modal */
.ce-mask { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; background: var(--surface-overlay); backdrop-filter: blur(3px); padding: 24px; }
.ce-card { width: 100%; max-width: 640px; max-height: 90vh; overflow-y: auto; border: 1px solid var(--border-default); border-radius: 16px; background: var(--surface-card); box-shadow: 0 20px 60px rgba(0, 0, 0, 0.35); }
.ce-head { padding: 18px 22px; border-bottom: 1px solid var(--border-subtle); }
.ce-title { font: 700 15px var(--font-display); color: var(--text-strong); }
.ce-sub { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.ce-body { padding: 18px 22px; }
.ce-foot { display: flex; justify-content: flex-end; gap: 10px; padding: 14px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); border-radius: 0 0 16px 16px; }
.fgrid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.fgrid input { width: 100%; box-sizing: border-box; height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-mono); color: var(--text-body); outline: none; }
.fgrid input:focus { border-color: var(--accent-text); }
.ce-card.imp { max-width: 720px; }
.imp-bar { display: flex; gap: 8px; margin-bottom: 12px; }
.imp-ta { width: 100%; min-height: 180px; resize: vertical; padding: 10px 12px; border: 1px solid var(--border-subtle);
  border-radius: 10px; background: var(--surface-page); color: var(--text-body); font: 400 12px/1.6 var(--font-mono); }
.imp-hint { font: 500 11px var(--font-body); color: var(--text-faint); margin-top: 8px; }
.imp-ok { margin-top: 12px; font: 600 12px var(--font-mono); color: var(--success-text); }
.imp-bad { margin-top: 12px; border-left: 3px solid var(--danger-text); padding-left: 10px; }
.imp-bad-t { font: 600 12px var(--font-body); color: var(--danger-text); margin-bottom: 4px; }
.imp-bad-l { font: 400 12px/1.7 var(--font-mono); color: var(--text-muted); }
.credhint { margin-top: 14px; font: 500 11.5px var(--font-mono); color: var(--text-faint); }
/* 悬浮批量操作条:贴底居中,始终写着选了几台。 */
.fab {
  position: fixed; left: 50%; bottom: 26px; transform: translateX(-50%); z-index: 70;
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap; max-width: min(920px, 94vw);
  padding: 10px 14px; border-radius: var(--radius-lg);
  background: var(--surface-card); border: 1px solid var(--border-default); box-shadow: var(--shadow-lg);
}
.fcnt { font: 700 12.5px var(--font-body); color: var(--text-strong); }
/* 选中里有生产实例时先说出来,而不是等到确认框才提 —— 那时人已经在按确定了。 */
.fprod { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: var(--radius-full); background: var(--danger-subtle); color: var(--danger-text); font: 700 10.5px var(--font-mono); }
.fab .fsel { height: 30px; font-size: 11.5px; }
.fbtn {
  display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 12px;
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-sunken); color: var(--text-body); font: 600 11.5px var(--font-body); cursor: pointer;
}
.fbtn:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); background: var(--accent-subtle); }
.fbtn:disabled { opacity: 0.55; cursor: default; }
.fbtn.ghosty { border-color: transparent; background: transparent; color: var(--text-faint); }
</style>
