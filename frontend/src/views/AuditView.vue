<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  Filter, Calendar, CircleCheck, Hourglass, CircleX, TriangleAlert, ChevronLeft, ChevronRight, X, FileText,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import { extractTables, tablesLabel } from '@/lib/sqlTables'
import { useUIStore } from '@/stores/ui'
import type { AuditRow, AuditQuery } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const router = useRouter()
const authStore = useAuthStore()
// An audited action carries the ticket that authorised it; following that link is
// the natural next question ("who approved this?"). Only offer it to someone who
// can actually open the approvals page — otherwise the click would land on a
// guard and look broken.
const canOpenApproval = computed(() => !!authStore.menus.approve)
// Same reasoning as the approval queue: the row says which data was touched,
// the full command lives in a card. An audited command is frequently a script,
// and the audit row also carries context worth reading together with it (who,
// where, the authorising ticket, and who approved it).
const detail = ref<AuditRow | null>(null)
function openDetail(r: AuditRow) { detail.value = r }
function tablesOf(cmd: string) { return tablesLabel(extractTables(cmd)) }

function openApproval(apNo?: string) {
  if (!apNo || !canOpenApproval.value) return
  router.push({ name: 'approvals', query: { ap: apNo } })
}
const rows = ref<AuditRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 100
const filter = ref<'all' | 'high' | 'mid' | 'low'>('all')
const timeIdx = ref(0)
const from = ref('') // absolute lower bound (datetime-local)
const to = ref('')   // absolute upper bound (datetime-local)
const exported = ref('')

const filterCycle = ['all', 'high', 'mid', 'low'] as const
const timeOpts = ['t24h', 't7d', 't30d', 'tAll']
const rangeKeys = ['24h', '7d', '30d', '']

const useAbsolute = computed(() => !!from.value || !!to.value)
const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))

function query(): AuditQuery {
  return { risk: filter.value, range: rangeKeys[timeIdx.value], from: from.value, to: to.value, page: page.value, pageSize }
}

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    const res = await api.audit(query())
    rows.value = res.items
    total.value = res.total
    page.value = res.page
  } catch (e) {
    ui.notifyError(e, '加载失败')
  }
}
onMounted(load)

// Any filter change resets to the first page before reloading.
async function reload() { page.value = 1; await load() }

// Risk-type filter as a dropdown: VSelect works on display labels, so map the
// selected label back to its internal risk value (index-aligned with filterCycle).
const riskLabelOpts = computed(() => [t('allRisk'), t('highTag'), t('med'), t('low')])
const riskLabel = computed<string>({
  get: () => riskLabelOpts.value[filterCycle.indexOf(filter.value)] ?? riskLabelOpts.value[0],
  set: (l) => {
    const i = riskLabelOpts.value.indexOf(l)
    if (i >= 0) { filter.value = filterCycle[i]; reload() }
  },
})

// Relative time range as a dropdown. Picking a preset clears any absolute window
// so the preset actually takes effect (absolute from/to otherwise wins server-side).
const timeLabelOpts = computed(() => timeOpts.map((k) => t(k as any)))
const timeLabel = computed<string>({
  get: () => timeLabelOpts.value[timeIdx.value] ?? timeLabelOpts.value[0],
  set: (l) => {
    const i = timeLabelOpts.value.indexOf(l)
    if (i >= 0) { timeIdx.value = i; from.value = ''; to.value = ''; reload() }
  },
})

async function clearAbsolute() { from.value = ''; to.value = ''; await reload() }
async function goto(p: number) {
  const np = Math.min(pages.value, Math.max(1, p))
  if (np === page.value) return
  page.value = np
  await load()
}

async function doExport() {
  // M14: 导出失败以 toast 呈现
  try {
    const token = localStorage.getItem('vela_token') || ''
    const res = await fetch(api.auditExportUrl(query()), { headers: { Authorization: `Bearer ${token}` } })
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'audit_export.csv'
    a.click()
    URL.revokeObjectURL(url)
    exported.value = `✓ 已导出 ${total.value} 条`
    setTimeout(() => (exported.value = ''), 2600)
  } catch (e) {
    ui.notifyError(e, '导出失败')
  }
}

function riskMeta(r: string) {
  if (r === 'high') return { t: t('highTag'), bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
  if (r === 'mid') return { t: t('med'), bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
  return { t: t('low'), bg: 'var(--accent-subtle)', c: 'var(--accent-text)' }
}
function resMeta(s: string) {
  if (s === 'pending') return { t: t('rPending'), c: 'var(--warning-text)', icon: Hourglass }
  if (s === 'executed') return { t: t('rExecuted'), c: 'var(--success-text)', icon: CircleCheck }
  if (s === 'rejected') return { t: t('rRejected'), c: 'var(--danger-text)', icon: CircleX }
  return { t: t('rWarn'), c: 'var(--warning-text)', icon: TriangleAlert }
}
function kw(cmd: string) { return cmd.split(/\s+/)[0] }
function rest(cmd: string) { return cmd.slice(kw(cmd).length) }
function kwColor(r: string) { return r === 'high' ? 'var(--danger-text)' : r === 'mid' ? 'var(--warning-text)' : 'var(--cyan-400)' }
// Rows can span days once paginated/filtered by absolute time, so show date+time.
function fmtTime(s: string) {
  const d = new Date(s)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">AUDIT LOG</div>
        <div class="sub">{{ $t('auditSub') }}</div>
      </div>
      <div class="acts">
        <span v-if="exported" class="exp">{{ exported }}</span>
        <!-- risk-type filter (dropdown) -->
        <div class="fsel"><Filter :size="14" /><VSelect v-model="riskLabel" :options="riskLabelOpts" /></div>
        <!-- time-range preset (dropdown) -->
        <div class="fsel" :class="{ off: useAbsolute }"><Calendar :size="14" /><VSelect v-model="timeLabel" :options="timeLabelOpts" /></div>
        <!-- absolute time window (takes precedence over the relative preset) -->
        <div class="ctl range">
          <input type="datetime-local" v-model="from" :aria-label="$t('auditFrom')" @change="reload" />
          <span class="dash">—</span>
          <input type="datetime-local" v-model="to" :aria-label="$t('auditTo')" @change="reload" />
          <X v-if="useAbsolute" class="clr" :size="14" :title="$t('auditClearRange')" @click="clearAbsolute" />
        </div>
        <VButton variant="secondary" height="36px" @click="doExport">{{ $t('exportCsv') }}</VButton>
      </div>
    </div>

    <!-- table -->
    <div class="table">
      <div class="th"><span>{{ $t('colTime') }}</span><span>{{ $t('colWho') }}</span><span>{{ $t('mInst') }}</span><span>{{ $t('colTables') }}</span><span>{{ $t('colRisk') }}</span><span>{{ $t('colResult') }}</span><span>{{ $t('colAp') }}</span><span></span></div>
      <div v-for="r in rows" :key="r.id" class="tr">
        <span class="mono mute">{{ fmtTime(r.occurredAt) }}</span>
        <span class="who">{{ r.actor }}</span>
        <span class="mono mute">{{ r.instance }}<span v-if="r.database" class="dbtag"> / {{ r.database }}</span></span>
        <span class="mono cmd" :title="r.command">
          <span :style="{ color: kwColor(r.risk) }">{{ kw(r.command) }}</span>
          <span class="tbl">{{ tablesOf(r.command) }}</span>
        </span>
        <span><span class="rbadge" :style="{ background: riskMeta(r.risk).bg, color: riskMeta(r.risk).c }">{{ riskMeta(r.risk).t }}</span></span>
        <span class="res" :style="{ color: resMeta(r.result).c }"><component :is="resMeta(r.result).icon" :size="12" />{{ resMeta(r.result).t }}</span>
        <span
          class="ap" :class="{ link: r.approvalNo && canOpenApproval }"
          :style="{ color: r.approvalNo ? '#8facff' : 'var(--text-faint)' }"
          :title="r.approvalNo && canOpenApproval ? $t('apOpenTicket') : ''"
          @click="openApproval(r.approvalNo)"
        >{{ r.approvalNo ? '#' + r.approvalNo : '—' }}</span>
        <button class="detbtn" :title="$t('auditDetail')" @click="openDetail(r)">
          <FileText :size="13" />{{ $t('auditDetail') }}
        </button>
      </div>
    </div>
    <!-- detail card: the executed command in full, with its context -->
    <div v-if="detail" class="overlay">
      <div class="mask" @click="detail = null" />
      <div class="dcard">
        <div class="dhead">
          <span class="rbadge" :style="{ background: riskMeta(detail.risk).bg, color: riskMeta(detail.risk).c }">{{ riskMeta(detail.risk).t }}</span>
          <span class="dtitle">{{ $t('colCmd') }}</span>
          <span class="mono mute">{{ fmtTime(detail.occurredAt) }}</span>
          <X :size="18" class="dx" @click="detail = null" />
        </div>
        <div class="dbody">
          <pre class="dcmd">{{ detail.command }}</pre>
          <div class="dgrid">
            <div><span class="ml">{{ $t('colWho') }}</span><div class="dv">{{ detail.actor }}</div></div>
            <div><span class="ml">{{ $t('mInst') }}</span><div class="dv mono">{{ detail.instance }}<template v-if="detail.database"> / {{ detail.database }}</template></div></div>
            <div><span class="ml">{{ $t('colResult') }}</span><div class="dv" :style="{ color: resMeta(detail.result).c }">{{ resMeta(detail.result).t }}</div></div>
            <div><span class="ml">{{ $t('colTables') }}</span><div class="dv mono">{{ tablesOf(detail.command) }}</div></div>
            <div v-if="detail.operator"><span class="ml">{{ $t('colOperator') }}</span><div class="dv">{{ detail.operator }}</div></div>
            <div v-if="detail.approvalNo">
              <span class="ml">{{ $t('colAp') }}</span>
              <div class="dv ap" :class="{ link: canOpenApproval }" @click="openApproval(detail.approvalNo)">#{{ detail.approvalNo }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="foot">
      <span class="hint">{{ $t('auditFootHint') }}</span>
      <span class="total">{{ $t('auditTotal', { n: total }) }}</span>
      <div class="pager">
        <button class="pg" :disabled="page <= 1" @click="goto(page - 1)"><ChevronLeft :size="15" /></button>
        <span class="pgn">{{ $t('auditPageOf', { p: page, n: pages }) }}</span>
        <button class="pg" :disabled="page >= pages" @click="goto(page + 1)"><ChevronRight :size="15" /></button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.detbtn { display: inline-flex; align-items: center; justify-content: center; gap: 4px; padding: 3px 8px;
  border: 1px solid var(--border-subtle); border-radius: 7px; background: transparent;
  color: var(--text-muted); font: 500 11px var(--font-body); cursor: pointer; }
.detbtn:hover { color: var(--accent-text); border-color: var(--accent-text); }

.cmd .tbl { color: var(--text-muted); margin-left: 6px; }
.overlay { position: fixed; inset: 0; z-index: 60; display: grid; place-items: center; }
.mask { position: absolute; inset: 0; background: rgba(0,0,0,.45); }
.dcard { position: relative; width: min(760px, 92vw); max-height: 86vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; }
.dhead { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--border-subtle); }
.dtitle { font: 600 14px var(--font-display); color: var(--text-strong); }
.dx { margin-left: auto; cursor: pointer; color: var(--text-muted); }
.dbody { padding: 14px 16px; overflow: auto; }
.dcmd { margin: 0; padding: 10px 12px; border-radius: 9px; background: var(--surface-page);
  border: 1px solid var(--border-subtle); font: 400 12px/1.65 var(--font-mono); color: var(--text-strong);
  white-space: pre-wrap; word-break: break-word; }
.dgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 12px; }
.ml { font: 600 11px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; }
.dv { font: 500 12px var(--font-body); color: var(--text-body); margin-top: 4px; }
.dv.ap { color: #8facff; }

.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.acts { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.exp { font: 600 12px var(--font-mono); color: var(--success-text); }
.ctl { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; font: 500 12px var(--font-mono); cursor: pointer; color: var(--text-muted); }
.ctl.off { opacity: 0.45; }
/* dropdown filter (risk type / time range) */
.fsel { display: flex; align-items: center; gap: 7px; color: var(--text-muted); }
.fsel > svg { flex: none; }
.fsel.off { opacity: 0.5; }
.fsel :deep(.vsel) { min-width: 122px; }
.fsel :deep(.control) { height: 36px; border-radius: 10px; }
.ctl.range { cursor: default; gap: 6px; }
.ctl.range input { background: transparent; border: none; outline: none; color: var(--text-body); font: 500 12px var(--font-mono); cursor: pointer; padding: 0; }
.ctl.range .dash { color: var(--text-faint); }
.ctl.range .clr { color: var(--text-faint); cursor: pointer; }
.ctl.range .clr:hover { color: var(--danger-text); }
.mono { font: 500 13px var(--font-mono); }
.mute { color: var(--text-muted); }
.dbtag { color: var(--text-faint); }
/* table */
.table { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.th, .tr { display: grid; grid-template-columns: 0.9fr 1fr 1.1fr 2.2fr 0.8fr 1fr 0.9fr 78px; gap: 12px; }
.th { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.05em; color: var(--text-faint); text-transform: uppercase; }
.tr { padding: 13px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.who { font: 500 12px var(--font-body); color: var(--text-body); }
.cmd { color: var(--text-body); }
.rbadge { display: inline-flex; height: 20px; padding: 0 8px; align-items: center; border-radius: 999px; font: 600 10px var(--font-mono); }
.res { display: flex; align-items: center; gap: 5px; font: 600 11px var(--font-mono); }
.ap.link { cursor: pointer; text-decoration: underline dotted; text-underline-offset: 3px; }
.ap.link:hover { filter: brightness(1.25); }
.ap { font: 600 11px var(--font-mono); }
.foot { margin-top: 12px; display: flex; align-items: center; gap: 14px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.foot .total { color: var(--text-muted); }
.pager { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.pg { display: inline-flex; align-items: center; justify-content: center; width: 30px; height: 30px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.pg:hover:not(:disabled) { border-color: var(--accent-text); color: var(--accent-text); }
.pg:disabled { opacity: 0.4; cursor: not-allowed; }
.pgn { min-width: 78px; text-align: center; color: var(--text-muted); }
</style>
