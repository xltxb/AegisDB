<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { DatabaseZap, KeyRound, Download, Copy, Check, Eye, EyeOff, FolderCog, TriangleAlert, Loader, CircleCheck, CircleX, Clock } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { CODE_OK, CODE_EXPORT_PATH_UNSET } from '@/api/http'
import type { Connection, ExportJob } from '@/types'

const router = useRouter()
const conns = ref<Connection[]>([])
const connId = ref<number>(0)
const db = ref('')                       // target database within the instance ('' = connection default)
const dbOptions = ref<string[]>([])
const sql = ref('SELECT id, name, status, created_at FROM users')
const name = ref('')
const busy = ref(false)
const err = ref('')
const needPath = ref(false)
const jobs = ref<ExportJob[]>([])
const copiedId = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

const activeConn = computed(() => conns.value.find((c) => c.id === connId.value) || null)
const anyActive = computed(() => jobs.value.some((j) => j.status === 'pending' || j.status === 'running'))

async function loadJobs() {
  try { jobs.value = await api.exportJobs() } catch { /* ignore */ }
}

// Load the selectable databases for the chosen instance (same live introspection
// the terminal uses). Defaults to the connection's own database when it has one.
async function loadDbs(id: number) {
  db.value = ''
  dbOptions.value = []
  if (!id) return
  try {
    const sc = await api.connectionSchema(id)
    dbOptions.value = sc.databases.map((d) => d.name)
    const c = conns.value.find((x) => x.id === id)
    // Pre-select a real database so an export isn't submitted with no schema (which
    // fails on the target with "No database selected"): the connection's own
    // database if it has one, else the first introspected database.
    if (c?.database && dbOptions.value.includes(c.database)) db.value = c.database
    else if (dbOptions.value.length) db.value = dbOptions.value[0]
  } catch { /* leave on default */ }
}
// The instance picker is a searchable VSelect, which works on display labels, so
// the selection round-trips through `env-name` (the same label the terminal and
// the async-exec page use). A deployment can carry hundreds of instances; the
// label is what an operator actually types to find one.
const connLabel = (c: Connection) => `${c.env}-${c.name}`
const connLabels = computed(() => conns.value.map(connLabel))
const selectedLabel = computed({
  get: () => {
    const c = conns.value.find((x) => x.id === connId.value)
    return c ? connLabel(c) : ''
  },
  set: (l: string) => {
    const c = conns.value.find((x) => connLabel(x) === l)
    if (c) { connId.value = c.id; loadDbs(c.id) }
  },
})

onMounted(async () => {
  try {
    conns.value = await api.connections()
    const first = conns.value.find((c) => c.env === 'prod') ?? conns.value[0]
    if (first) { connId.value = first.id; await loadDbs(first.id) }
  } catch { /* ignore */ }
  await loadJobs()
  timer = setInterval(() => { if (anyActive.value) loadJobs() }, 2000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

async function submit() {
  const q = sql.value.trim()
  if (!connId.value) { err.value = '请选择数据库实例'; return }
  if (!q) { err.value = '请输入要导出的 SQL'; return }
  busy.value = true
  err.value = ''
  needPath.value = false
  try {
    const env = await api.exportData(connId.value, q, name.value.trim(), db.value)
    if (env.code === CODE_EXPORT_PATH_UNSET) { needPath.value = true; return }
    if (env.code === CODE_OK) { await loadJobs(); return } // job queued
    err.value = env.msg || '提交失败'
  } catch { err.value = '提交失败' }
  finally { busy.value = false }
}

// Export passwords are masked by default and only shown on explicit reveal, so
// they don't sit in the DOM across the 2s job polling (L4).
const shownPw = ref<Set<number>>(new Set())
function togglePw(id: number) {
  const s = new Set(shownPw.value)
  s.has(id) ? s.delete(id) : s.add(id)
  shownPw.value = s
}

async function copyPw(j: ExportJob) {
  try { await navigator.clipboard.writeText(j.password); copiedId.value = j.id; setTimeout(() => (copiedId.value = 0), 1600) } catch { /* ignore */ }
}
const partsOf = (j: ExportJob) => (j.files ? j.files.split('\n').map((f) => f.trim()).filter(Boolean) : [])

async function downloadOne(file: string) {
  const blob = await api.exportDownload(file)
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.split(/[\\/]/).pop() || 'export.zip'
  a.click()
  URL.revokeObjectURL(url)
}
// Download every part file of a finished job (sequentially to avoid popup blocks).
async function download(j: ExportJob) {
  try {
    for (const f of partsOf(j)) {
      await downloadOne(f)
      await new Promise((r) => setTimeout(r, 250))
    }
  } catch { err.value = '下载失败' }
}
function goSettings() { router.push('/settings') }
const kb = (n: number) => (n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB')
const stMeta: Record<string, { t: string; icon: any; cls: string }> = {
  pending: { t: 'exportStPending', icon: Clock, cls: 'wait' },
  running: { t: 'exportStRunning', icon: Loader, cls: 'run' },
  done: { t: 'exportStDone', icon: CircleCheck, cls: 'ok' },
  failed: { t: 'exportStFailed', icon: CircleX, cls: 'bad' },
}
const meta = (s: string) => stMeta[s] || stMeta.pending
</script>

<template>
  <div class="scy page">
    <div class="col">
      <!-- submit form -->
      <section class="card">
        <div class="shead">
          <div class="sic"><DatabaseZap :size="18" color="var(--accent-text)" /></div>
          <div><div class="st">{{ $t('exportTitle') }}</div><div class="ss">{{ $t('exportSub') }}</div></div>
        </div>
        <div class="body">
          <div v-if="needPath" class="notice">
            <FolderCog :size="15" /><span>{{ $t('exportNoPath') }}</span>
            <VButton variant="secondary" height="30px" @click="goSettings">{{ $t('scPathGo') }}</VButton>
          </div>

          <div class="grid2">
            <div class="pick">
              <div class="lbl">{{ $t('exportConn') }}</div>
              <VSelect v-model="selectedLabel" :options="connLabels" searchable height="38px" />
            </div>
            <div>
              <div class="lbl">{{ $t('exportDb') }}</div>
              <select v-model="db" class="sel">
                <option value="">{{ $t('exportDbDefault') }}</option>
                <option v-for="d in dbOptions" :key="d" :value="d">{{ d }}</option>
              </select>
            </div>
          </div>
          <div class="lbl">{{ $t('exportName') }}</div>
          <input v-model="name" class="nameinput" :placeholder="$t('exportNamePh')" />
          <div v-if="activeConn?.env === 'prod'" class="prodwarn"><TriangleAlert :size="13" />{{ $t('opWarnPrefix') }} <b>PROD · {{ activeConn.name }}</b> · {{ $t('opWarnCaution') }}</div>

          <div class="lbl">{{ $t('exportSql') }}</div>
          <textarea v-model="sql" class="sqlarea" rows="4" spellcheck="false" placeholder="SELECT ... FROM ..." />
          <div v-if="err" class="err">{{ err }}</div>

          <div class="acts">
            <VButton variant="primary" :disabled="busy" @click="submit">{{ busy ? $t('exportRunning') : $t('exportSubmit') }}</VButton>
          </div>
        </div>
      </section>

      <!-- job list -->
      <section class="card">
        <div class="shead">
          <div class="sic"><Download :size="18" color="var(--accent-text)" /></div>
          <div><div class="st">{{ $t('exportTasks') }}</div><div class="ss">{{ $t('exportTasksSub') }}</div></div>
          <span v-if="anyActive" class="running-tag"><Loader :size="13" class="spin" />{{ $t('exportStRunning') }}</span>
        </div>
        <div class="jobs">
          <div v-if="!jobs.length" class="jempty">{{ $t('exportNoTasks') }}</div>
          <!-- One job = three fixed rows, so every card lines up with its
               neighbours: title+badge | id+time, SQL excerpt, then stats +
               password + download on ONE footer row. The stats used to sit in
               the title row behind the badge, so they wrapped differently per
               card and the download buttons floated at differing heights. -->
          <div v-for="j in jobs" :key="j.id" class="job">
            <div class="jtop">
              <span class="jinst">{{ j.instance }}<span v-if="j.database" class="jdb"> / {{ j.database }}</span></span>
              <span class="jbadge" :class="meta(j.status).cls"><component :is="meta(j.status).icon" :size="12" :class="{ spin: j.status === 'running' }" />{{ $t(meta(j.status).t) }}</span>
              <span class="jtime">#{{ j.id }} · {{ j.createdAt?.slice(5, 16).replace('T', ' ') }}</span>
            </div>
            <div class="jsql" :title="j.sql">{{ j.sql }}</div>
            <div v-if="j.status === 'done'" class="jfoot">
              <span class="jmeta">{{ j.rows }} 行 · {{ kb(j.bytes) }} · {{ j.parts }} {{ $t('exportParts') }}</span>
              <span class="jpw">
                <KeyRound :size="12" /><span class="pl">{{ $t('exportPassword') }}</span>
                <code class="pw">{{ shownPw.has(j.id) ? j.password : '••••••••••' }}</code>
                <button class="copy" title="显示/隐藏" @click="togglePw(j.id)"><component :is="shownPw.has(j.id) ? EyeOff : Eye" :size="14" /></button>
                <button class="copy" :title="$t('copy')" @click="copyPw(j)"><component :is="copiedId === j.id ? Check : Copy" :size="14" /></button>
              </span>
              <span class="jdl">
                <VButton variant="secondary" height="32px" @click="download(j)"><Download :size="14" />{{ $t('exportDownload') }}{{ j.parts > 1 ? ` (${j.parts})` : '' }}</VButton>
              </span>
            </div>
            <div v-else-if="j.status === 'failed'" class="jerr">{{ j.error || $t('exportStFailed') }}</div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
/* Wide screens: submit form on the left (sticky, so it stays at hand while the
   job history scrolls), job list filling the rest — the single 760px column
   left half the viewport blank. Narrow screens fall back to one column. */
.col { max-width: 1500px; display: grid; grid-template-columns: minmax(380px, 440px) minmax(0, 1fr); gap: 18px; align-items: start; }
.col > .card:first-child { position: sticky; top: 0; }
@media (max-width: 1100px) {
  .col { display: flex; flex-direction: column; max-width: 760px; }
  .col > .card:first-child { position: static; }
}
.card { border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); overflow: hidden; }
.shead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.sic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.st { font: 600 14px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.running-tag { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font: 600 11px var(--font-mono); color: var(--accent-text); }
.body { padding: 16px 20px 20px; }
.notice { display: flex; align-items: center; gap: 9px; padding: 11px 14px; margin-bottom: 12px; border-radius: 10px; background: var(--warning-subtle); color: var(--warning-text); font: 600 12px var(--font-body); }
.notice span { flex: 1; }
.grid2 { display: grid; grid-template-columns: 1.4fr 1fr; gap: 14px; }
.lbl { margin-top: 14px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.grid2 .lbl:first-child, .grid2 > div > .lbl { margin-top: 0; }
.sel { margin-top: 7px; width: 100%; box-sizing: border-box; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); color: var(--text-strong); font: 600 12.5px var(--font-mono); outline: none; cursor: pointer; }
.sel:focus { border-color: var(--accent-text); }
/* Match the searchable instance picker to the native <select> beside it — same
   offset, height, radius and type, so the two form a single row. */
.pick :deep(.vsel) { margin-top: 7px; }
.pick :deep(.control) { border-radius: 9px; font: 600 12.5px var(--font-mono); color: var(--text-strong); }
.nameinput { margin-top: 7px; width: 100%; box-sizing: border-box; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12.5px var(--font-mono); outline: none; }
.nameinput:focus { border-color: var(--accent-text); }
.prodwarn { margin-top: 8px; display: flex; align-items: center; gap: 6px; font: 700 11.5px var(--font-mono); color: var(--danger-text); }
.sqlarea { margin-top: 7px; width: 100%; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); color: var(--text-strong); padding: 10px 12px; resize: vertical; font: 500 12.5px var(--font-mono); line-height: 1.6; outline: none; }
.sqlarea:focus { border-color: var(--accent-text); }
.err { margin-top: 10px; font: 600 12px var(--font-body); color: var(--danger-text); }
.acts { margin-top: 16px; display: flex; justify-content: flex-end; }
.jobs { display: flex; flex-direction: column; }
.jempty { padding: 30px; text-align: center; font: 500 12.5px var(--font-mono); color: var(--text-faint); }
.job { padding: 13px 20px 14px; border-bottom: 1px solid var(--border-subtle); }
.job:last-child { border-bottom: none; }
.jtop { display: flex; align-items: center; gap: 10px; min-width: 0; }
.jinst { font: 600 13px var(--font-mono); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jdb { color: var(--text-faint); font-weight: 500; }
.jbadge { flex-shrink: 0; display: inline-flex; align-items: center; gap: 5px; height: 20px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); }
.jbadge.wait { background: var(--surface-sunken); color: var(--text-muted); }
.jbadge.run { background: var(--accent-subtle); color: var(--accent-text); }
.jbadge.ok { background: var(--success-subtle); color: var(--success-text); }
.jbadge.bad { background: var(--danger-subtle); color: var(--danger-text); }
.jmeta { flex-shrink: 0; font: 500 11px var(--font-mono); color: var(--text-muted); }
.jtime { margin-left: auto; flex-shrink: 0; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.jsql { margin-top: 6px; font: 500 12px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jfoot { margin-top: 9px; display: flex; align-items: center; gap: 12px; min-width: 0; }
.jpw { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--text-faint); }
.jpw .pl { font: 600 10px var(--font-mono); color: var(--text-faint); white-space: nowrap; }
.pw { min-width: 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 5px 10px; border-radius: 7px; background: var(--accent-subtle); color: var(--accent-text); font: 800 12.5px var(--font-mono); letter-spacing: 1px; }
.copy { width: 30px; height: 28px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-sunken); color: var(--text-body); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.copy:hover { color: var(--accent-text); }
.jerr { margin-top: 7px; font: 600 11.5px var(--font-body); color: var(--danger-text); }
.jdl { margin-left: auto; flex-shrink: 0; }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
