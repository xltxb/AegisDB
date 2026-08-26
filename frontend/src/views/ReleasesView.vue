<script setup lang="ts">
// 数据库变更发布 (CI/CD) — 发布单列表 + 流水线状态。
//
// The pipeline strip is the point of this page: a release is a sequence of
// stages, and what an operator needs to know at a glance is WHICH stage it is
// sitting on and why. So every stage is drawn even before it runs (pending), a
// stage blocked on a person is drawn differently from one doing work, and
// clicking a stage shows the log or the review findings it produced rather than
// a generic "failed".
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Rocket, Plus, X, Check, Loader, Hourglass, CircleX, Minus, Play,
  ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Search, Bell, CircleAlert,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { CODE_MFA_REQUIRED } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Connection, Pipeline, Release, ReleaseStage, ReviewResult } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()

const releases = ref<Release[]>([])
const openId = ref(0)
const scope = ref<'mine' | 'all'>('mine')
const pipelines = ref<Pipeline[]>([])
const conns = ref<Connection[]>([])
const openStage = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

const open = computed(() => releases.value.find((r) => r.id === openId.value) || null)
// A run is "live" while it can still change without anyone touching THIS page.
// `waiting` counts: the decision arrives through the approval queue (or the 飞书
// card, or the external callback) and the server-side sweeper resumes the run —
// so a page that stopped polling there would sit on "等待处理" long after the
// pipeline had finished.
const anyLive = computed(() =>
  releases.value.some((r) => r.status === 'pending' || r.status === 'running' || r.status === 'waiting'),
)

async function loadList() {
  try {
    const page = await api.releases(scope.value, '', 1, 50)
    releases.value = page.items
    if (!openId.value && page.items.length) openId.value = page.items[0].id
    ui.pageSub = t('rlSub2', { n: page.total })
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
async function refreshOpen() {
  if (!openId.value) return
  try {
    const r = await api.release(openId.value)
    const i = releases.value.findIndex((x) => x.id === r.id)
    if (i >= 0) releases.value[i] = r
  } catch { /* the list keeps the last known state */ }
}
function select(r: Release) {
  openId.value = r.id
  openStage.value = 0
  refreshOpen()
}

onMounted(async () => {
  try { conns.value = await api.connections() } catch { /* the form falls back to an empty picker */ }
  try { pipelines.value = await api.pipelines() } catch { /* same */ }
  await loadList()
  await refreshOpen()
  timer = setInterval(() => { if (anyLive.value) { loadList(); refreshOpen() } }, 2500)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

// ---- stage presentation ----
const stageIcon: Record<string, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, verify: Search, manual: Hourglass, notify: Bell,
}
function statusMeta(s: string) {
  switch (s) {
    case 'success': return { icon: Check, c: 'var(--success-text)', bg: 'var(--success-subtle)', spin: false }
    case 'running': return { icon: Loader, c: 'var(--accent-text)', bg: 'var(--accent-subtle)', spin: true }
    case 'waiting': return { icon: Hourglass, c: 'var(--warning-text)', bg: 'var(--warning-subtle)', spin: false }
    case 'failed': return { icon: CircleX, c: 'var(--danger-text)', bg: 'var(--danger-subtle)', spin: false }
    case 'skipped': return { icon: Minus, c: 'var(--text-faint)', bg: 'var(--surface-sunken)', spin: false }
    case 'aborted': return { icon: CircleX, c: 'var(--text-muted)', bg: 'var(--surface-sunken)', spin: false }
    default: return { icon: Minus, c: 'var(--text-faint)', bg: 'var(--surface-sunken)', spin: false }
  }
}
// The connector between two stages reports what has actually flowed: solid
// behind finished work, animated into the stage running right now, flat ahead
// of it. Computed from the PAIR, which is why it cannot live in the template.
function connClass(stages: ReleaseStage[], i: number) {
  const prevDone = ['success', 'skipped'].includes(stages[i - 1]?.status)
  if (!prevDone) return ''
  return stages[i].status === 'running' ? 'live' : 'done'
}
function duration(st: ReleaseStage) {
  if (!st.startedAt) return ''
  const end = st.finishedAt ? new Date(st.finishedAt).getTime() : Date.now()
  const ms = end - new Date(st.startedAt).getTime()
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

// A review stage carries its findings as JSON so a blocked release can still be
// explained after the rule library has moved on.
function findingsOf(st: ReleaseStage): ReviewResult | null {
  if (!st.findings) return null
  try { return JSON.parse(st.findings) as ReviewResult } catch { return null }
}
const openStageObj = computed(() => open.value?.stages.find((s) => s.id === openStage.value) || null)

// ---- actions ----
async function abort(r: Release) {
  if (!confirmAction(t('rlAbortConfirm', { no: r.relNo }))) return
  try {
    const env = await api.abortRelease(r.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    await loadList(); await refreshOpen()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
async function continueStage(r: Release, st: ReleaseStage) {
  try {
    const env = await api.continueStage(r.id, st.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    ui.notify(t('rlContinued'), 'success')
    setTimeout(() => { loadList(); refreshOpen() }, 400)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// ---- create form ----
const formOpen = ref(false)
// changeType: '' = 自动推断;声明了就必须与内容一致(后端强校验,DML/DDL 不得同单)
const f = ref({ title: '', pipelineId: 0, connectionId: 0, database: '', sql: '', changeType: '', reason: '', mfaCode: '' })
const needMfa = ref(false)
const busy = ref(false)
const preview = ref<ReviewResult | null>(null)
const dbOptions = ref<string[]>([])

const connLabels = computed(() => conns.value.map((c) => `${c.env}-${c.name}`))
const connLabel = computed({
  get: () => {
    const c = conns.value.find((x) => x.id === f.value.connectionId)
    return c ? `${c.env}-${c.name}` : ''
  },
  set: (l: string) => {
    const c = conns.value.find((x) => `${x.env}-${x.name}` === l)
    if (c) { f.value.connectionId = c.id; loadDbs(c.id) }
  },
})
const pipeLabels = computed(() => pipelines.value.filter((p) => p.enabled).map(pipeLabel))
function pipeLabel(p: Pipeline) {
  return p.tierCode ? `${p.name} (${p.tierCode.toUpperCase()})` : p.name
}
const pipeLabelSel = computed({
  get: () => {
    const p = pipelines.value.find((x) => x.id === f.value.pipelineId)
    return p ? pipeLabel(p) : ''
  },
  set: (l: string) => {
    const p = pipelines.value.find((x) => pipeLabel(x) === l)
    if (p) f.value.pipelineId = p.id
  },
})

async function loadDbs(id: number) {
  f.value.database = ''; dbOptions.value = []
  try {
    const sc = await api.connectionSchema(id)
    dbOptions.value = sc.databases.map((d) => d.name)
    const c = conns.value.find((x) => x.id === id)
    if (c?.database && dbOptions.value.includes(c.database)) f.value.database = c.database
    else if (dbOptions.value.length) f.value.database = dbOptions.value[0]
  } catch { /* the field stays free text */ }
}

function openForm() {
  const firstPipe = pipelines.value.find((p) => p.enabled && p.isDefault) || pipelines.value.find((p) => p.enabled)
  f.value = {
    title: '', pipelineId: firstPipe?.id || 0, connectionId: conns.value[0]?.id || 0,
    database: '', sql: '', changeType: '', reason: '', mfaCode: '',
  }
  preview.value = null
  needMfa.value = false
  if (f.value.connectionId) loadDbs(f.value.connectionId)
  formOpen.value = true
}

// Pre-flight review: the same library the pipeline's review stage will run, so
// the operator sees what would block them before a release number exists.
async function preflight() {
  if (!f.value.sql.trim() || !f.value.connectionId) return
  try {
    const r = await api.reviewCheck({ connectionId: f.value.connectionId, sql: f.value.sql })
    preview.value = r.result
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function submit() {
  if (!f.value.title.trim() || !f.value.connectionId || !f.value.sql.trim()) {
    ui.notify(t('rlFormIncomplete'), 'error'); return
  }
  busy.value = true
  try {
    const env = await api.createRelease({
      title: f.value.title.trim(), pipelineId: f.value.pipelineId, connectionId: f.value.connectionId,
      database: f.value.database, sql: f.value.sql, changeType: f.value.changeType, reason: f.value.reason.trim(), mfaCode: f.value.mfaCode.trim(),
    })
    if (env.code === CODE_MFA_REQUIRED) { needMfa.value = true; ui.notify(t('rlNeedMfa'), 'error'); return }
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    formOpen.value = false
    await loadList()
    if (env.data?.id) { openId.value = env.data.id; await refreshOpen() }
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { busy.value = false }
}

const canSeeAll = computed(() => auth.me?.canApprove || (auth.me?.roleCodes || []).includes('admin'))
function riskCls(r: string) { return r === 'high' ? 'bad' : r === 'mid' ? 'warn' : 'ok' }
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">RELEASE PIPELINE</div>
        <div class="sub">{{ $t('rlSub') }}</div>
      </div>
      <div class="hacts">
        <div v-if="canSeeAll" class="seg small">
          <div class="si" :class="{ active: scope === 'mine' }" @click="scope = 'mine'; loadList()">{{ $t('rlMine') }}</div>
          <div class="si" :class="{ active: scope === 'all' }" @click="scope = 'all'; loadList()">{{ $t('rlAll') }}</div>
        </div>
        <VButton variant="primary" @click="openForm"><Plus :size="15" />{{ $t('rlNew') }}</VButton>
      </div>
    </div>

    <div class="wrap">
      <!-- release list -->
      <div class="list">
        <div v-for="r in releases" :key="r.id" class="ritem" :class="{ on: r.id === openId }" @click="select(r)">
          <div class="rtop">
            <span class="rno">{{ r.relNo }}</span>
            <span v-if="r.changeType" class="ctb" :class="r.changeType">{{ r.changeType.toUpperCase() }}</span>
            <span class="pill" :class="r.status">{{ $t('rlSt_' + r.status) }}</span>
          </div>
          <div class="rtitle">{{ r.title }}</div>
          <div class="rmeta">
            <span class="tag">{{ r.env }}</span>
            <span>{{ r.instance }}</span>
            <span v-if="r.database" class="dim">/ {{ r.database }}</span>
          </div>
          <div class="rmeta">
            <span class="dim">{{ r.pipelineName }}</span>
            <span class="dim">·</span>
            <span class="dim">{{ r.creator }}</span>
            <!-- An externally-raised ticket says so, with the system that raised
                 it and that system's own ticket number. -->
            <span v-if="r.source === 'api'" class="api" :title="r.clientName">API</span>
            <span v-if="r.externalRef" class="dim">{{ r.externalRef }}</span>
          </div>
        </div>
        <div v-if="!releases.length" class="empty">{{ $t('rlEmpty') }}</div>
      </div>

      <!-- detail -->
      <div v-if="open" class="detail">
        <div class="dhead">
          <div class="dic"><Rocket :size="18" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="dt">{{ open.relNo }} · {{ open.title }}<span v-if="open.changeType" class="ctb big" :class="open.changeType">{{ open.changeType.toUpperCase() }}</span></div>
            <div class="ds">
              {{ open.instance }}<span v-if="open.database"> / {{ open.database }}</span> ·
              {{ open.pipelineName }} · {{ fmt(open.createdAt) }}
              <span v-if="open.source === 'api'"> · {{ $t('rlFromApi', { name: open.clientName || 'API' }) }}<span v-if="open.externalRef"> · {{ open.externalRef }}</span></span>
            </div>
          </div>
          <span class="pill" :class="riskCls(open.risk)">{{ $t('rlRisk') }} {{ (open.risk || 'low').toUpperCase() }}</span>
          <VButton v-if="open.status === 'pending' || open.status === 'waiting'" height="32px" @click="abort(open)">
            {{ $t('rlAbort') }}
          </VButton>
        </div>

        <!-- pipeline strip -->
        <div class="pipe scx">
          <template v-for="(st, i) in open.stages" :key="st.id">
            <div v-if="i > 0" class="conn" :class="connClass(open.stages, i)" />
            <div class="node" :class="{ sel: openStage === st.id }" @click="openStage = openStage === st.id ? 0 : st.id">
              <div
                class="dot" :class="{ halo: st.status === 'running' }"
                :style="{ background: statusMeta(st.status).bg, color: statusMeta(st.status).c }"
              >
                <component :is="statusMeta(st.status).icon" :size="16" :class="{ spin: statusMeta(st.status).spin }" />
              </div>
              <div class="nname">
                <component :is="stageIcon[st.type] || Terminal" :size="11" />
                {{ st.name }}
              </div>
              <div class="nst" :style="{ color: statusMeta(st.status).c }">
                {{ $t('rlSt_' + st.status) }}<span v-if="duration(st)" class="dim"> · {{ duration(st) }}</span>
              </div>
            </div>
          </template>
        </div>

        <div v-if="open.error" class="errbar"><CircleAlert :size="14" />{{ open.error }}</div>

        <!-- stage detail -->
        <div v-if="openStageObj" class="sbox">
          <div class="sbhead">
            <span class="sbt">{{ openStageObj.name }}</span>
            <span class="pill" :class="openStageObj.status">{{ $t('rlSt_' + openStageObj.status) }}</span>
            <span v-if="openStageObj.approvalNo" class="apno">{{ openStageObj.approvalNo }}</span>
            <!-- 两种人工闸共用一个"继续"入口:manual 是流程里配置的确认点,
                 execute 是内置执行闸(必须有人点击才落库)。按钮文案区分语义。 -->
            <VButton
              v-if="(openStageObj.type === 'manual' || openStageObj.type === 'execute') && openStageObj.status === 'waiting'"
              variant="primary" height="30px" @click="continueStage(open, openStageObj)"
            >
              <Play :size="13" />{{ openStageObj.type === 'execute' ? $t('rlConfirmExec') : $t('rlContinue') }}
            </VButton>
          </div>
          <pre v-if="openStageObj.log" class="log">{{ openStageObj.log }}</pre>
          <div v-else class="empty">{{ $t('rlNoLog') }}</div>

          <!-- review findings -->
          <template v-if="findingsOf(openStageObj)">
            <div class="fsum">
              {{ $t('srCounts', {
                s: findingsOf(openStageObj)!.statements, e: findingsOf(openStageObj)!.errors,
                w: findingsOf(openStageObj)!.warnings, i: findingsOf(openStageObj)!.infos,
              }) }}
            </div>
            <div v-for="(fd, i) in findingsOf(openStageObj)!.findings" :key="i" class="find" :class="fd.level">
              <div class="fname">{{ fd.name }} <span class="floc">{{ $t('srStmtAt', { n: fd.stmt, line: fd.line }) }}</span></div>
              <div class="fmsg">{{ fd.message }}</div>
              <div class="fsql">{{ fd.sql }}</div>
            </div>
          </template>
        </div>
        <div v-else class="hint">{{ $t('rlPickStage') }}</div>

        <div class="sqlbox2">
          <div class="fl">{{ $t('rlContent') }}</div>
          <pre class="log">{{ open.sql || $t('rlScriptRef') }}</pre>
          <div v-if="open.reason" class="reason">{{ $t('reason') }}: {{ open.reason }}</div>
        </div>
      </div>
      <div v-else class="detail empty">{{ $t('rlPickRelease') }}</div>
    </div>

    <!-- create modal -->
    <div v-if="formOpen" class="overlay">
      <div class="mask" @click="formOpen = false" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><Rocket :size="18" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ $t('rlNew') }}</div><div class="ms">{{ $t('rlNewSub') }}</div></div>
          <X :size="18" class="mx" @click="formOpen = false" />
        </div>
        <div class="mbody">
          <div><div class="fl">{{ $t('rlTitle') }}</div><input v-model="f.title" class="in" :placeholder="$t('rlTitlePh')" /></div>
          <div class="grid2">
            <div><div class="fl">{{ $t('mInst') }}</div><VSelect v-model="connLabel" :options="connLabels" searchable /></div>
            <div>
              <div class="fl">{{ $t('fDatabase') }}</div>
              <VSelect v-if="dbOptions.length" v-model="f.database" :options="dbOptions" />
              <input v-else v-model="f.database" class="in" />
            </div>
          </div>
          <div><div class="fl">{{ $t('rlPipeline') }}</div><VSelect v-model="pipeLabelSel" :options="pipeLabels" /></div>
          <div>
            <div class="fl">{{ $t('rlChangeType') }}</div>
            <div class="ctseg">
              <span class="ct" :class="{ on: f.changeType === '' }" @click="f.changeType = ''">{{ $t('rlCtAuto') }}</span>
              <span class="ct" :class="{ on: f.changeType === 'dml' }" @click="f.changeType = 'dml'">DML</span>
              <span class="ct" :class="{ on: f.changeType === 'ddl' }" @click="f.changeType = 'ddl'">DDL</span>
            </div>
            <div class="hint">{{ $t('rlChangeTypeHint') }}</div>
          </div>
          <div><div class="fl">{{ $t('rlContent') }}</div><textarea v-model="f.sql" class="sqlbox" spellcheck="false" :placeholder="$t('rlSqlPh')" /></div>
          <div><div class="fl">{{ $t('reason') }}</div><input v-model="f.reason" class="in" :placeholder="$t('rlReasonPh')" /></div>
          <div v-if="needMfa"><div class="fl">{{ $t('loginMfaLabel') }}</div><input v-model="f.mfaCode" class="in" :placeholder="$t('loginMfaPh')" /></div>

          <div v-if="preview" class="pv" :class="preview.passed ? 'ok' : 'bad'">
            {{ preview.passed ? $t('srPassed') : $t('srBlocked') }} ·
            {{ $t('srCounts', { s: preview.statements, e: preview.errors, w: preview.warnings, i: preview.infos }) }}
            <div v-for="(fd, i) in preview.findings.slice(0, 8)" :key="i" class="pvline">
              [{{ fd.level.toUpperCase() }}] {{ fd.name }} — {{ fd.message }}
            </div>
          </div>
          <div class="hint">{{ $t('rlSubmitHint') }}</div>
        </div>
        <div class="mfoot">
          <VButton @click="preflight"><ShieldCheck :size="14" />{{ $t('rlPreflight') }}</VButton>
          <VButton variant="primary" :disabled="busy" @click="submit"><Rocket :size="14" />{{ $t('rlSubmit') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.hacts { margin-left: auto; display: flex; align-items: center; gap: 10px; }
.eyebrow { font: 600 10.5px var(--font-mono); letter-spacing: var(--tracking-caps); color: var(--accent-text); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.wrap { display: grid; grid-template-columns: minmax(240px, 0.6fr) minmax(0, 2fr); gap: 16px; align-items: start; }
@media (max-width: 1100px) { .wrap { grid-template-columns: 1fr; } }
.list { display: flex; flex-direction: column; gap: 8px; max-height: 76vh; overflow-y: auto; }
/* Elevation, not an outline: hover LIFTS (you are pointing at me), selection
   shows the brand rail (you are looking at me). Two questions, two signals. */
.ritem { position: relative; border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs);
  padding: 14px 16px; cursor: pointer; transition: box-shadow var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out); }
.ritem:hover { box-shadow: var(--shadow-md); transform: translateY(-1px); }
.ritem:active { transform: translateY(0); }
.ritem.on { box-shadow: var(--shadow-md); }
.ritem.on::before { content: ""; position: absolute; left: 0; top: 14px; bottom: 14px; width: 3px; border-radius: 0 3px 3px 0;
  background: linear-gradient(180deg, var(--accent), var(--glow-accent)); }
.rtop { display: flex; align-items: center; gap: 8px; }
.ctseg { display: inline-flex; gap: 6px; }
.ct { padding: 5px 14px; border-radius: 8px; border: 1px solid var(--border-default); background: var(--surface-sunken); font: 600 11.5px var(--font-mono); color: var(--text-muted); cursor: pointer; user-select: none; }
.ct.on { background: var(--accent-subtle); border-color: var(--accent-text); color: var(--accent-text); }
.ctb { padding: 1px 7px; border-radius: 5px; font: 700 9.5px var(--font-mono); letter-spacing: 0.04em; }
.ctb.dml { background: var(--success-subtle); color: var(--success-text); }
.ctb.ddl { background: var(--warning-subtle); color: var(--warning-text); }
.ctb.big { margin-left: 8px; vertical-align: 2px; }
.rno { font: 600 11.5px var(--font-mono); font-variant-numeric: tabular-nums; color: var(--text-faint); letter-spacing: 0.02em; }
.rtitle { font: 600 14px var(--font-body); color: var(--text-strong); margin-top: 5px; letter-spacing: -0.005em; }
.rmeta { display: flex; align-items: center; gap: 7px; margin-top: 6px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.dim { color: var(--text-faint); }
.tag { padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 9.5px var(--font-mono); }
.api { padding: 1px 6px; border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 700 9px var(--font-mono); }
/* The leading dot carries the state as well as the colour does — colour alone
   fails for anyone who cannot tell the green from the red. */
.pill { margin-left: auto; display: inline-flex; align-items: center; gap: 5px; padding: 3px 9px; border-radius: var(--radius-full);
  font: 600 10.5px var(--font-body); background: var(--surface-sunken); color: var(--text-muted); white-space: nowrap; }
.pill::before { content: ""; width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.pill.success, .pill.ok { background: var(--success-subtle); color: var(--success-text); }
.pill.failed, .pill.bad { background: var(--danger-subtle); color: var(--danger-text); }
.pill.waiting, .pill.warn { background: var(--warning-subtle); color: var(--warning-text); }
.pill.running { background: var(--accent-subtle); color: var(--accent-text); }
/* Running is the one state that has to say "still moving" without the reader
   watching for a number to change. */
.pill.running::before { animation: statePulse 1.4s var(--ease-out) infinite; }
.detail { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm);
  padding: 18px 20px; min-height: 320px; }
.dhead { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; }
.dic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.dt { font: 600 14.5px var(--font-display); color: var(--text-strong); }
.ds { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.dhead .pill { margin-left: 0; }
.pipe { display: flex; align-items: flex-start; gap: 0; padding: 16px 10px; border: none; border-radius: var(--radius-lg);
  background: var(--surface-sunken); overflow-x: auto; }
.node { display: flex; flex-direction: column; align-items: center; gap: 5px; min-width: 104px; padding: 4px 6px; border-radius: 10px; cursor: pointer; }
.node { transition: var(--transition-colors); }
.node:hover { background: color-mix(in oklch, var(--surface-card) 75%, transparent); }
.node.sel { background: var(--surface-card); box-shadow: var(--shadow-xs); }
.dot { width: 32px; height: 32px; border-radius: 50%; display: flex; align-items: center; justify-content: center; }
.nname { display: flex; align-items: center; gap: 4px; font: 600 11.5px var(--font-body); color: var(--text-body); white-space: nowrap; }
.nst { font: 500 10px var(--font-mono); white-space: nowrap; }
/* The connector reports what has actually FLOWED: solid behind finished work,
   animated into the stage running now, flat ahead of it. A uniform hairline
   made a pipeline that had not started look identical to one half done. */
.conn { flex: 1; min-width: 28px; height: 3px; margin-top: 22px; border-radius: var(--radius-full); background: var(--border-subtle); }
.conn.done { background: var(--success); }
.conn.live { background: linear-gradient(90deg, var(--success) 0%, var(--accent) 50%, var(--border-subtle) 50%);
  background-size: 200% 100%; animation: connFlow 1.6s linear infinite; }
@keyframes connFlow { from { background-position: 100% 0; } to { background-position: 0 0; } }
.spin { animation: spin 1s linear infinite; }
/* A halo on the stage that is running: the spinner says "this icon is busy",
   the halo says "this NODE is where the pipeline is" — readable from across a
   room, which is how a release board is actually watched. */
.dot.halo { animation: nodeHalo 1.8s var(--ease-out) infinite; }
@keyframes nodeHalo {
  0% { box-shadow: 0 0 0 0 color-mix(in oklch, var(--accent) 50%, transparent); }
  70% { box-shadow: 0 0 0 12px transparent; }
  100% { box-shadow: 0 0 0 0 transparent; }
}
@keyframes spin { to { transform: rotate(360deg); } }
.errbar { display: flex; align-items: center; gap: 7px; margin-top: 12px; padding: 9px 12px; border-radius: 10px; background: var(--danger-subtle); color: var(--danger-text); font: 500 12px var(--font-body); }
.sbox { margin-top: 14px; border: 1px solid var(--border-subtle); border-radius: 12px; padding: 12px 14px; }
.sbhead { display: flex; align-items: center; gap: 9px; margin-bottom: 9px; }
.sbt { font: 600 13px var(--font-body); color: var(--text-strong); }
.sbhead .pill { margin-left: 0; }
.apno { font: 600 10.5px var(--font-mono); color: var(--accent-text); }
.sbhead :deep(.vbtn) { margin-left: auto; }
.log { margin: 0; padding: var(--space-3) 14px; border-radius: var(--radius-md);
  background: color-mix(in oklch, var(--surface-sunken) 55%, var(--surface-card));
  box-shadow: inset 0 0 0 1px var(--border-subtle); border: none;
  font: 500 11.5px var(--font-mono); line-height: 1.65; color: var(--text-body);
  white-space: pre-wrap; word-break: break-word; max-height: 260px; overflow-y: auto; }
.fsum { margin-top: 10px; font: 600 11.5px var(--font-mono); color: var(--text-muted); }
.find { margin-top: 6px; padding: 8px 11px; border-radius: 9px; background: var(--surface-sunken); border-left: 3px solid var(--border-strong); }
.find.error { border-left-color: var(--danger-text); }
.find.warn { border-left-color: var(--warning-text); }
.fname { font: 600 12px var(--font-body); color: var(--text-strong); }
.floc { font: 500 10px var(--font-mono); color: var(--text-faint); margin-left: 6px; }
.fmsg { font: 500 11.5px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.fsql { font: 500 10.5px var(--font-mono); color: var(--text-faint); margin-top: 3px; word-break: break-all; }
.sqlbox2 { margin-top: 14px; }
.reason { margin-top: 6px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.hint { margin-top: 12px; font: 500 11.5px var(--font-body); color: var(--text-faint); }
.empty { padding: 22px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin: 10px 0 6px; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.sqlbox { width: 100%; box-sizing: border-box; min-height: 150px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 10px 12px; font: 500 12px var(--font-mono); color: var(--text-body); outline: none; resize: vertical; }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 10px; overflow: hidden; }
.seg.small .si { height: 32px; line-height: 32px; padding: 0 12px; }
.si { text-align: center; font: 600 11.5px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer; }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 680px; max-width: 94vw; max-height: 90vh; overflow-y: auto; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; box-shadow: var(--shadow-xl); }
.mhead { display: flex; align-items: center; gap: 12px; padding: 16px 22px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 700 15px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.mbody { padding: 4px 22px 18px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.pv { margin-top: 12px; padding: 10px 12px; border-radius: 10px; font: 600 11.5px var(--font-mono); }
.pv.ok { background: var(--success-subtle); color: var(--success-text); }
.pv.bad { background: var(--danger-subtle); color: var(--danger-text); }
.pvline { font: 500 11px var(--font-body); margin-top: 4px; }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
