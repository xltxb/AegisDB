<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clock, Loader, CircleCheck, CircleX, Hourglass, Play } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { CODE_OK, CODE_INTERCEPTED } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { Connection, AsyncJob } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const conns = ref<Connection[]>([])
const connId = ref<number>(0)
const db = ref('')
const dbOptions = ref<string[]>([])
const sql = ref('CALL your_long_running_proc()')
const reason = ref('')
const busy = ref(false)
const err = ref('')
const jobs = ref<AsyncJob[]>([])
const openId = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

const connLabel = computed(() => conns.value.map((c) => `${c.env}-${c.name}`))
const connByLabel = (l: string) => conns.value.find((c) => `${c.env}-${c.name}` === l)
const selectedLabel = computed({
  get: () => { const c = conns.value.find((x) => x.id === connId.value); return c ? `${c.env}-${c.name}` : '' },
  set: (l: string) => { const c = connByLabel(l); if (c) { connId.value = c.id; loadDbs(c.id) } },
})
const anyActive = computed(() => jobs.value.some((j) => j.status === 'pending' || j.status === 'running'))
const openJob = computed(() => jobs.value.find((j) => j.id === openId.value) || null)

async function loadJobs() {
  try { jobs.value = await api.asyncJobs() } catch { /* ignore */ }
}
// Refresh the open job's log/status while it runs.
async function refreshOpen() {
  if (!openId.value) return
  try {
    const j = await api.asyncJob(openId.value)
    const i = jobs.value.findIndex((x) => x.id === j.id)
    if (i >= 0) jobs.value[i] = j; else jobs.value.unshift(j)
  } catch { /* ignore */ }
}

async function loadDbs(id: number) {
  db.value = ''; dbOptions.value = []
  if (!id) return
  try {
    const sc = await api.connectionSchema(id)
    dbOptions.value = sc.databases.map((d) => d.name)
    const c = conns.value.find((x) => x.id === id)
    if (c?.database && dbOptions.value.includes(c.database)) db.value = c.database
    else if (dbOptions.value.length) db.value = dbOptions.value[0]
  } catch { /* leave default */ }
}

onMounted(async () => {
  try {
    conns.value = await api.connections()
    const first = conns.value[0]
    if (first) { connId.value = first.id; await loadDbs(first.id) }
  } catch { /* ignore */ }
  await loadJobs()
  timer = setInterval(() => { if (anyActive.value) { loadJobs(); refreshOpen() } }, 2000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

async function submit() {
  const q = sql.value.trim()
  if (!connId.value) { err.value = t('pickInstance'); return }
  if (!q) { err.value = t('enterSql'); return }
  busy.value = true; err.value = ''
  try {
    const env = await api.execAsync(connId.value, q, db.value, reason.value.trim())
    if (env.code === CODE_OK && env.data?.jobId) {
      await loadJobs(); openId.value = env.data.jobId; return
    }
    if (env.code === CODE_INTERCEPTED) {
      err.value = t('interceptedNeedAppr') + ' ' + (env.data?.approvalNo || '')
      return
    }
    err.value = env.msg || t('submitFailed')
  } catch (e: any) { err.value = e?.message || t('submitFailed') }
  finally { busy.value = false }
}

function statusMeta(s: string) {
  if (s === 'running') return { icon: Loader, c: 'var(--accent-text)', spin: true }
  if (s === 'done') return { icon: CircleCheck, c: 'var(--success-text)', spin: false }
  if (s === 'failed') return { icon: CircleX, c: 'var(--danger-text)', spin: false }
  return { icon: Hourglass, c: 'var(--warning-text)', spin: false }
}
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">{{ $t('asyncTitle') }}</div>
        <div class="sub">{{ $t('asyncSub') }}</div>
      </div>
    </div>

    <div class="grid">
      <!-- submit form -->
      <div class="card form">
        <div class="fl">{{ $t('mInst') }}</div>
        <VSelect v-model="selectedLabel" :options="connLabel" />
        <div class="fl">{{ $t('fDatabase') }}</div>
        <VSelect v-if="dbOptions.length" v-model="db" :options="dbOptions" />
        <input v-else v-model="db" class="in" :placeholder="$t('fDatabase')" />
        <div class="fl">SQL</div>
        <textarea v-model="sql" class="sqlbox" spellcheck="false"></textarea>
        <div class="fl">{{ $t('reason') }}</div>
        <input v-model="reason" class="in" :placeholder="$t('asyncReasonPh')" />
        <div class="acts">
          <span v-if="err" class="err">{{ err }}</span>
          <VButton variant="primary" height="38px" :disabled="busy" @click="submit"><Play :size="15" />{{ $t('asyncSubmit') }}</VButton>
        </div>
        <div class="hint">{{ $t('asyncHint') }}</div>
      </div>

      <!-- job list + log -->
      <div class="card jobs">
        <div class="jhead">{{ $t('asyncJobs') }}</div>
        <div v-if="!jobs.length" class="empty">{{ $t('asyncEmpty') }}</div>
        <div v-for="j in jobs" :key="j.id" class="job" :class="{ open: openId === j.id }" @click="openId = openId === j.id ? 0 : j.id; refreshOpen()">
          <div class="jrow">
            <component :is="statusMeta(j.status).icon" :size="15" :class="{ spin: statusMeta(j.status).spin }" :color="statusMeta(j.status).c" />
            <span class="jno">#{{ j.id }}</span>
            <span class="jinst mono">{{ j.instance }}<span v-if="j.database" class="dbtag"> / {{ j.database }}</span></span>
            <span class="jsql mono">{{ j.sql }}</span>
            <span class="jtime mono">{{ fmt(j.createdAt) }}</span>
          </div>
          <div v-if="openId === j.id" class="log">
            <pre class="logpre">{{ j.log || '· …' }}</pre>
            <div v-if="j.status === 'done'" class="logfoot ok">✓ {{ $t('asyncDone', { n: j.rows }) }}</div>
            <div v-else-if="j.status === 'failed'" class="logfoot err">✕ {{ j.error }}</div>
            <div v-else class="logfoot run"><Loader :size="12" class="spin" />{{ $t('asyncRunning') }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; overflow: auto; }
.head { margin-bottom: 16px; }
.eyebrow { font: 600 15px var(--font-display); color: var(--text-strong); }
.sub { font: 500 12.5px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.grid { display: grid; grid-template-columns: 380px 1fr; gap: 18px; align-items: start; }
.card { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 16px 18px; }
.form .fl { font: 500 11px var(--font-body); color: var(--text-faint); margin: 12px 0 6px; }
.form .fl:first-child { margin-top: 0; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); padding: 0 12px; font: 500 13px var(--font-mono); color: var(--text-strong); outline: none; }
.sqlbox { width: 100%; box-sizing: border-box; min-height: 120px; resize: vertical; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); padding: 10px 12px; font: 500 13px var(--font-mono); color: var(--text-strong); outline: none; }
.acts { display: flex; align-items: center; gap: 12px; margin-top: 14px; }
.err { flex: 1; font: 600 12px var(--font-mono); color: var(--danger-text); }
.hint { margin-top: 10px; font: 500 11px var(--font-body); color: var(--text-faint); line-height: 1.5; }
.jobs { min-height: 200px; }
.jhead { font: 600 13px var(--font-display); color: var(--text-strong); margin-bottom: 10px; }
.empty { padding: 30px 0; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.job { border: 1px solid var(--border-subtle); border-radius: 10px; margin-bottom: 8px; cursor: pointer; overflow: hidden; }
.job.open { border-color: var(--accent-text); }
.jrow { display: grid; grid-template-columns: 20px 44px 1.2fr 2fr 1.1fr; gap: 10px; align-items: center; padding: 10px 12px; }
.jno { font: 600 12px var(--font-mono); color: var(--text-muted); }
.jinst { color: var(--text-body); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dbtag { color: var(--text-faint); }
.jsql { color: var(--text-muted); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jtime { color: var(--text-faint); font-size: 11px; text-align: right; }
.mono { font-family: var(--font-mono); }
.log { border-top: 1px solid var(--border-subtle); background: var(--surface-sunken); padding: 10px 12px; }
.logpre { margin: 0; max-height: 360px; overflow: auto; font: 500 12px var(--font-mono); color: var(--text-body); white-space: pre-wrap; word-break: break-word; }
.logfoot { margin-top: 8px; font: 600 12px var(--font-mono); display: flex; align-items: center; gap: 6px; }
.logfoot.ok { color: var(--success-text); }
.logfoot.err { color: var(--danger-text); }
.logfoot.run { color: var(--accent-text); }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
