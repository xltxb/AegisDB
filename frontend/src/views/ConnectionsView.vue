<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Tag, Pencil } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import TagEditModal from '@/components/modals/TagEditModal.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { parseConnectionImport, IMPORT_TEMPLATE, type ImportRow } from '@/lib/connectionImport'
import type { Connection } from '@/types'

const auth = useAuthStore()
// Instance configuration is platform-admin only (backend enforces it too).
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))

const { t } = useI18n()
const ui = useUIStore()

const conns = ref<Connection[]>([])
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

// Computed, not a constant: built once at module scope the labels would stay in
// whichever language was active at load time.
const envOpts = computed(() => [
  { label: t('envProd'), env: 'prod' },
  { label: t('envGli'), env: 'gli' },
  { label: t('envStaging'), env: 'staging' },
  { label: t('envDev'), env: 'dev' },
])
const engineOpts = ['MySQL 8.0', 'TiDB', 'GaussDB (DWS)', 'Oracle', 'PostgreSQL 15', 'ClickHouse', 'Redis 7']
// Oracle identifies the target DB by a service name (or a SID via the "sid/" prefix),
// not a plain schema name — hint that in the 数据库名 field placeholder.
const dbHint = (engine: string) => (/oracle/i.test(engine) ? t('dbHintOracle') : 'orders_db')
const policyOpts = ['strict', 'approve-1', 'audit-only']

const blankDraft = () => ({ name: '', host: '', engine: 'MySQL 8.0', envLabel: t('envProd'), policy: 'strict', username: '', password: '', database: '' })
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
const impParsed = computed(() => parseConnectionImport(impText.value))

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
  const url = URL.createObjectURL(new Blob([IMPORT_TEMPLATE], { type: 'text/csv;charset=utf-8' }))
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
    envLabel: envOpts.value.find((o) => o.env === c.env)?.label || envOpts.value[0].label,
    policy: c.policy,
    username: c.username || '',
    password: '',
    database: c.database || '',
  }
  editModal.value = { open: true, id: c.id }
}
async function saveEdit() {
  if (!editDraft.value.name.trim() || !editDraft.value.host.trim()) return
  const env = envOpts.value.find((o) => o.label === editDraft.value.envLabel)?.env || 'prod'
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

const prod = computed(() => conns.value.filter((c) => c.env === 'prod'))
const gli = computed(() => conns.value.filter((c) => c.env === 'gli'))
const staging = computed(() => conns.value.filter((c) => c.env === 'staging'))
const dev = computed(() => conns.value.filter((c) => c.env === 'dev'))

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    conns.value = await api.connections()
    ui.pageSub = t('subDb', { n: conns.value.length })
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
onMounted(async () => { await load(); try { allTags.value = await api.tags() } catch { /* ignore */ } })

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

    <div class="table">
      <div class="thead">
        <span>{{ $t('colInst') }}</span><span>{{ $t('colEngine') }}</span><span>{{ $t('colAddr') }}</span>
        <span>{{ $t('colRole') }}</span><span>{{ $t('colPolicy') }}</span><span>{{ $t('colStatus') }}</span>
      </div>

      <template v-for="(group, gi) in [{ rows: prod, cls: 'danger', label: 'prodRow' }, { rows: gli, cls: 'info', label: 'gliRow' }, { rows: staging, cls: 'warn', label: 'stgRow' }, { rows: dev, cls: 'success', label: 'devRow' }]" :key="gi">
        <div class="grouprow" :class="group.cls"><span class="d" />{{ $t(group.label as any) }}</div>
        <div v-for="c in group.rows" :key="c.id" class="trow">
          <div>
            <div class="cn">{{ c.name }}</div>
            <div class="cl">{{ c.layer }}</div>
            <div class="tags">
              <span class="rbadge" :class="c.username ? 'real' : 'sim'">{{ c.username ? $t('connReal') : $t('connSim') }}</span>
              <span v-for="tg in tagArr(c.tags)" :key="tg" class="tchip">{{ tg }}</span>
              <span v-if="isAdmin" class="tedit" @click="openTagEdit(c)"><Tag :size="10" />{{ tagArr(c.tags).length ? $t('edit') : $t('tagAdd') }}</span>
            </div>
          </div>
          <div class="mono">{{ c.engine }}</div>
          <div class="mono mute">{{ c.host }}:{{ c.port }}</div>
          <div class="mono" :style="{ color: roleColor(c.defaultRole) }">{{ c.defaultRole }}</div>
          <div>
            <select v-if="isAdmin" class="polsel" :style="{ background: polMeta(c.policy).bg, color: polMeta(c.policy).c }" :value="c.policy" title="网关策略" @change="setPolicy(c, ($event.target as HTMLSelectElement).value)">
              <option v-for="p in policyOpts" :key="p" :value="p">{{ p }}</option>
            </select>
            <span v-else class="pill" :style="{ background: polMeta(c.policy).bg, color: polMeta(c.policy).c }">{{ c.policy }}</span>
          </div>
          <div class="statuscell">
            <span class="pill" :class="{ click: isAdmin }" :style="{ background: stMeta(c.status).bg, color: stMeta(c.status).c }" :title="isAdmin ? '切换状态' : ''" @click="toggle(c)">
              <span class="dotc" />{{ c.status === 'online' ? $t('online') : $t('maint') }}
            </span>
            <button v-if="isAdmin" class="editbtn" :title="$t('connEdit')" @click="openEdit(c)"><Pencil :size="14" /></button>
          </div>
        </div>
      </template>
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
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
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
.grouprow.warn { background: rgba(245, 165, 36, 0.06); color: var(--warning-text); }
.grouprow.success { background: rgba(24, 179, 104, 0.06); color: var(--success-text); }
.grouprow .d { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
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
