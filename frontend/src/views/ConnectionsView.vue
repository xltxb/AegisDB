<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Database, Tag, Pencil, Search, ChevronDown, X } from 'lucide-vue-next'
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
const q = ref('')
const envFilter = ref('') // '' = 全部环境
const collapsed = ref(new Set<string>())
function toggleGroup(key: string) {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsed.value = next
}
const isCollapsed = (key: string) => !q.value.trim() && collapsed.value.has(key)
// 搜索匹配:名称/地址/库名/引擎/标签/角色,一个框全找
function matches(c: Connection, needle: string) {
  const hay = [c.name, c.host + ':' + c.port, c.database, c.engine, c.tags, c.defaultRole, c.layer]
    .join(' ').toLowerCase()
  return hay.includes(needle)
}
const filtered = computed(() => {
  let rows = conns.value
  if (envFilter.value) rows = rows.filter((c) => c.env === envFilter.value)
  const needle = q.value.trim().toLowerCase()
  if (needle) rows = rows.filter((c) => matches(c, needle))
  return rows
})
// 环境 chips 的计数按"搜索后"算:筛选器要回答"命中的都在哪",不是全量分布
const envCounts = computed(() => {
  const m: Record<string, number> = {}
  const needle = q.value.trim().toLowerCase()
  for (const c of conns.value) {
    if (needle && !matches(c, needle)) continue
    m[c.env] = (m[c.env] || 0) + 1
  }
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
  types: { key: string; label: string; rows: Connection[] }[]
}
const envGroups = computed<ConnGroup[]>(() => {
  const byEnv: Record<string, Connection[]> = {}
  for (const c of filtered.value) (byEnv[c.env] ||= []).push(c)

  const byType = (rows: Connection[]) => {
    const m: Record<string, Connection[]> = {}
    for (const c of rows) (m[engineDisplay(c.engine)] ||= []).push(c)
    return Object.keys(m).sort(byCatalogueOrder).map((k) => ({ key: k, label: k, rows: m[k] }))
  }

  const groups: ConnGroup[] = envtier.environments.map((e) => ({
    key: e.code,
    label: envtier.envLabel(e.code),
    cls: envtier.dotForEnv(e.code),
    count: (byEnv[e.code] ?? []).length,
    types: byType(byEnv[e.code] ?? []),
  }))
  const known = new Set(envtier.environments.map((e) => e.code))
  for (const k of Object.keys(byEnv).filter((x) => !known.has(x)).sort()) {
    groups.push({
      key: k, label: `${k} · ${t('envUnknown')}`, cls: 'muted',
      count: byEnv[k].length, types: byType(byEnv[k]),
    })
  }
  // 过滤态下空组只是噪音;全量视图仍显示空环境(它回答"这个环境还没接入实例")
  if (q.value.trim() || envFilter.value) return groups.filter((g) => g.count > 0)
  return groups
})

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    conns.value = await api.connections()
    try { projects.value = await api.projects() } catch { /* 归属下拉降级为空 */ }
    ui.pageSub = t('subDb', { n: conns.value.length })
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

function polMeta(p: string) {
  if (p === 'strict') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
  if (p === 'audit-only') return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)' }
  return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}
function roleColor(r: string) { return r.startsWith('dba') ? '#8facff' : 'var(--text-muted)' }
function stMeta(s: string) {
  return s === 'online'
    ? { bg: 'var(--success-subtle)', c: 'var(--success-text)' }
    : { bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
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
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">CONNECTIONS</div>
        <div class="sub">{{ conns.length }} {{ $t('connSub') }}</div>
      </div>
      <div class="acts">
        <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
        <template v-else>
          <VButton variant="secondary" @click="openImport">{{ $t('importInst') }}</VButton>
          <VButton variant="primary" @click="openNew">{{ $t('newConn') }}</VButton>
        </template>
      </div>
    </div>

    <div class="toolbar">
      <div class="searchbox">
        <Search :size="14" />
        <input v-model="q" class="sin" :placeholder="$t('connSearchPh')" />
        <X v-if="q" :size="13" class="sclear" @click="q = ''" />
      </div>
      <div class="envchips">
        <span class="echip" :class="{ on: envFilter === '' }" @click="envFilter = ''">{{ $t('connAllEnv') }}<i>{{ filtered.length }}</i></span>
        <span v-for="e in envtier.environments" :key="e.code" class="echip" :class="[{ on: envFilter === e.code }, envtier.dotForEnv(e.code)]"
          @click="envFilter = envFilter === e.code ? '' : e.code">
          <span class="ed" />{{ envtier.envLabel(e.code) }}<i>{{ envCounts[e.code] || 0 }}</i>
        </span>
      </div>
    </div>

    <div class="table">
      <div class="thead">
        <span>{{ $t('colInst') }}</span><span>{{ $t('colEngine') }}</span><span>{{ $t('colAddr') }}</span>
        <span>{{ $t('colRole') }}</span><span>{{ $t('colPolicy') }}</span><span>{{ $t('colStatus') }}</span>
      </div>

      <template v-for="group in envGroups" :key="group.key">
        <div class="grouprow click" :class="group.cls" @click="toggleGroup(group.key)">
          <ChevronDown :size="13" class="chev" :class="{ closed: isCollapsed(group.key) }" />
          <span class="d" />{{ group.label }}<span class="gcnt">{{ group.count }}</span>
        </div>
        <template v-if="!isCollapsed(group.key)">
        <template v-for="ty in group.types" :key="ty.key">
        <!-- Second level: database type. The engine decides which driver reaches
             the instance and how its commands are read, so it groups rather than
             sitting in a column to be scanned for. -->
        <div class="typerow">{{ ty.label }}<span class="gcnt">{{ ty.rows.length }}</span></div>
        <template v-for="c in ty.rows" :key="c.id">
        <div class="trow">
          <div>
            <div class="cn">{{ c.name }}</div>
            <div class="cl">{{ c.layer }}</div>
            <div class="tags">
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
          <div class="mono">{{ c.engine }}</div>
          <div class="mono mute">{{ c.host }}:{{ c.port }}</div>
          <div class="mono" :style="{ color: roleColor(c.defaultRole) }">{{ c.defaultRole }}</div>
          <div>
            <select v-if="isAdmin" class="polsel" :style="{ background: polMeta(c.policy).bg, color: polMeta(c.policy).c }" :value="c.policy" :title="$t('fPolicy')" @change="setPolicy(c, ($event.target as HTMLSelectElement).value)">
              <option v-for="p in policyOpts" :key="p" :value="p">{{ p }}</option>
            </select>
            <span v-else class="pill" :style="{ background: polMeta(c.policy).bg, color: polMeta(c.policy).c }">{{ c.policy }}</span>
          </div>
          <div class="statuscell">
            <span class="pill" :class="{ click: isAdmin }" :style="{ background: stMeta(c.status).bg, color: stMeta(c.status).c }" :title="isAdmin ? $t('tipToggleStatus') : ''" @click="toggle(c)">
              <span class="dotc" />{{ c.status === 'online' ? $t('online') : $t('maint') }}
            </span>
            <button v-if="isAdmin" class="editbtn" :title="$t('connEdit')" @click="openEdit(c)"><Pencil :size="14" /></button>
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
        </template>
      </template>
      <div v-if="!envGroups.some((g) => g.count > 0)" class="noresult">{{ $t('connNoMatch') }}</div>
    </div>

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
.head { display: flex; align-items: center; margin-bottom: 14px; }
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
.toolbar { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-bottom: 14px; }
.searchbox { display: flex; align-items: center; gap: 8px; width: 300px; padding: 0 12px; height: 36px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-card); color: var(--text-faint); }
.searchbox:focus-within { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.sin { flex: 1; min-width: 0; border: none; outline: none; background: transparent; font: 500 12.5px var(--font-body); color: var(--text-strong); }
.sclear { cursor: pointer; color: var(--text-muted); }
.envchips { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.echip { display: inline-flex; align-items: center; gap: 6px; padding: 5px 11px; border-radius: 999px; border: 1px solid var(--border-default); background: var(--surface-card); font: 600 11.5px var(--font-body); color: var(--text-muted); cursor: pointer; user-select: none; }
.echip i { font: 600 10px var(--font-mono); font-style: normal; color: var(--text-faint); }
.echip.on { background: var(--accent-subtle); border-color: var(--accent-text); color: var(--accent-text); }
.echip.on i { color: var(--accent-text); }
.echip .ed { width: 6px; height: 6px; border-radius: 50%; background: currentColor; opacity: 0.55; }
.grouprow.click { cursor: pointer; user-select: none; }
.grouprow.click:hover { filter: brightness(0.97); }
.chev { transition: transform var(--dur-fast) var(--ease-out); color: var(--text-faint); }
.chev.closed { transform: rotate(-90deg); }
.noresult { padding: 36px 0; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-faint); }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.acts { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.table { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.thead, .trow { display: grid; grid-template-columns: 1.5fr 1fr 1.7fr 1fr 1fr 0.9fr; gap: 12px; }
.thead { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-faint); text-transform: uppercase; }
.trow { padding: 14px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.grouprow { padding: 9px 18px; display: flex; align-items: center; gap: 8px; font: 600 11px var(--font-mono); }
.grouprow.danger { background: rgba(240, 71, 62, 0.06); color: var(--danger-text); }
.grouprow.info { background: rgba(59, 130, 246, 0.07); color: var(--info-text, #2563eb); }
/* `warn` is the legacy class name; `warning` is what the tier palette emits. */
.grouprow.warn, .grouprow.warning { background: rgba(245, 165, 36, 0.06); color: var(--warning-text); }
.grouprow.success { background: rgba(24, 179, 104, 0.06); color: var(--success-text); }
/* An environment that no longer resolves to a tier — neutral, since no control
   level can be claimed for it. */
.grouprow.muted { background: var(--surface-sunken); color: var(--text-faint); }
.grouprow .d { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.gcnt { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
/* Second-level group: quieter than the environment row, so the environment stays
   the structure the eye follows down the table. */
.typerow {
  padding: 6px 18px 6px 30px; display: flex; align-items: center;
  font: 600 10.5px var(--font-mono); letter-spacing: 0.04em; color: var(--text-faint);
  background: var(--surface-sunken);
}
.typerow.empty { padding-left: 18px; font-style: italic; }
.cn { font: 600 13px var(--font-body); color: var(--text-strong); }
.cl { font: 500 11px var(--font-mono); color: var(--text-faint); }
.tags { margin-top: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: 5px; }
.tchip { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.rbadge { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: 999px; font: 700 10px var(--font-mono); }
.rbadge.real { background: var(--success-subtle); color: var(--success-text); }
.rbadge.sim { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.tedit { display: inline-flex; align-items: center; gap: 3px; height: 19px; padding: 0 7px; border-radius: 999px; border: 1px dashed var(--border-default); color: var(--text-faint); font: 600 10px var(--font-mono); cursor: pointer; }
.tedit:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.mono { font: 500 12px var(--font-mono); color: var(--text-body); }
.mono.mute { color: var(--text-muted); }
.pill { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.pill.click { cursor: pointer; }
.polsel { height: 24px; padding: 0 6px; border: 1px solid var(--border-default); border-radius: 999px; font: 600 11px var(--font-mono); cursor: pointer; outline: none; }
.polsel:focus { border-color: var(--accent-text); }
.polsel option { background: var(--surface-card); color: var(--text-body); }
.dotc { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.statuscell { display: flex; align-items: center; gap: 10px; }
.editbtn { width: 28px; height: 26px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-muted); cursor: pointer; display: inline-flex; align-items: center; justify-content: center; }
.editbtn:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
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
</style>
