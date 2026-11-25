<script setup lang="ts">
// AI 巡检报告(.scratch/inspection-report 工单 04)。
//
// 一个页面三种视图,由地址里的参数决定 —— 刷新、分享链接都回到同一处:
//   - 无参数:实例列表,每个实例一行,异常的在前;
//   - ?instance=ID:这个实例的报告历史;
//   - ?instance=ID&report=ID:报告查看器。
//
// 报告是外部系统生成的 HTML,放进 sandbox="allow-scripts" 的 iframe 里显示(地址是 .../view,不用 srcdoc:
// srcdoc 会继承控制台页面的 CSP,报告里的脚本就跑不了了):脚本能跑
// (内联图表照常画),但报告运行在"无来源"的隔离环境里,读不到平台的登录凭证和本地存储、
// 动不了父页面、不能跳走或弹窗。**不要**给它加 allow-same-origin。
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Search, ChevronLeft, Download, CircleAlert, TriangleAlert, CircleCheck, CircleHelp } from 'lucide-vue-next'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useEnvTierStore } from '@/stores/envtier'
import { saveBlob } from '@/lib/download'
import type { InspectionLatestRow, InspectionReport } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const route = useRoute()
const router = useRouter()
const envtier = useEnvTierStore()

const instanceId = computed(() => Number(route.query.instance) || 0)
const reportId = computed(() => Number(route.query.report) || 0)

// ---- 实例列表 ----
const rows = ref<InspectionLatestRow[]>([])
const today = ref('')
const loaded = ref(false)
const kw = ref('')
type Filter = 'all' | 'critical' | 'warning' | 'missing' | 'ok'
const filter = ref<Filter>('all')

// 状态:严重 > 警告 > 今天没收到 > 正常。今天没收到 = 从没收到过,或最新一份不是今天的。
type State = 'critical' | 'warning' | 'missing' | 'ok'
function state(r: InspectionLatestRow): State {
  if (r.report && r.report.reportDate === today.value) {
    if (r.report.level === 'critical') return 'critical'
    if (r.report.level === 'warning') return 'warning'
    return 'ok'
  }
  return 'missing'
}
const rank: Record<State, number> = { critical: 0, warning: 1, missing: 2, ok: 3 }
const tierCode = (env: string) => (envtier.tierOf(env)?.code || env || '').toUpperCase()

const counts = computed(() => {
  const n = { critical: 0, warning: 0, missing: 0, ok: 0 }
  for (const r of rows.value) n[state(r)]++
  return n
})
const received = computed(() => rows.value.length - counts.value.missing)
const shown = computed(() => {
  const q = kw.value.trim().toLowerCase()
  return rows.value
    .filter((r) => (filter.value === 'all' || state(r) === filter.value) && (!q || r.instance.toLowerCase().includes(q)))
    .slice()
    .sort((a, b) => rank[state(a)] - rank[state(b)] || a.instance.localeCompare(b.instance))
})

async function loadLatest() {
  try {
    await envtier.load().catch(() => {})
    const r = await api.inspectionLatest()
    rows.value = r.items
    today.value = r.today
  } catch (e) { ui.notifyError(e, t('loadFailed')) } finally { loaded.value = true }
}

// ---- 报告历史 ----
const history = ref<InspectionReport[]>([])
const historyTotal = ref(0)
const page = ref(1)
const pageSize = 30
const currentRow = computed(() => rows.value.find((r) => r.connectionId === instanceId.value) || null)
async function loadHistory() {
  if (!instanceId.value) return
  try {
    const r = await api.inspectionHistory(instanceId.value, page.value, pageSize)
    history.value = r.items
    historyTotal.value = r.total
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}

// ---- 查看器 ----
const report = ref<InspectionReport | null>(null)
const showAllFindings = ref(false)
const findings = computed(() => {
  const order: Record<string, number> = { critical: 0, warning: 1, info: 2 }
  const all = (report.value?.findings || []).slice().sort((a, b) => (order[a.severity] ?? 9) - (order[b.severity] ?? 9))
  return showAllFindings.value ? all : all.slice(0, 5)
})
async function loadReport() {
  report.value = null; showAllFindings.value = false
  if (!reportId.value) return
  try { report.value = await api.inspectionReport(reportId.value) } catch (e) { ui.notifyError(e, t('loadFailed')); return }
}
async function download() {
  if (!report.value) return
  let html: string
  try { html = await api.inspectionHtml(report.value.id) } catch (e) { ui.notifyError(e, t('loadFailed')); return }
  saveBlob(new Blob([html], { type: 'text/html;charset=utf-8' }), `${report.value.instanceName}-${report.value.reportDate}.html`)
}

function openInstance(id: number) { router.push({ query: { instance: String(id) } }) }
function openReport(id: number) { router.push({ query: { instance: String(instanceId.value), report: String(id) } }) }
function back() {
  if (reportId.value) router.push({ query: { instance: String(instanceId.value) } })
  else router.push({ query: {} })
}

onMounted(loadLatest)
watch(instanceId, () => { page.value = 1; loadHistory() }, { immediate: true })
watch(reportId, loadReport, { immediate: true })
watch(page, loadHistory)

const levelText = (l?: string) => t('inspLevel_' + (l || 'none'))
const scoreText = (s?: number | null) => (s === undefined || s === null ? '—' : String(s))
</script>

<template>
  <div class="scy page">
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">INSPECTION</div>
        <div class="v-h1">{{ $t('t_inspection') }}</div>
        <div class="v-sub" data-testid="insp-summary">
          {{ $t('inspSummary', { got: received, total: rows.length, c: counts.critical, w: counts.warning }) }}
        </div>
      </div>
    </div>

    <!-- 查看器 -->
    <template v-if="reportId">
      <button class="backlink" @click="back"><ChevronLeft :size="14" />{{ $t('inspBackHistory') }}</button>
      <div v-if="report" class="card viewer" data-testid="insp-viewer">
        <div class="vhead">
          <div class="grow">
            <div class="vt">{{ report.instanceName }} · {{ report.reportDate }}</div>
            <div class="vs">
              <span class="lv" :class="report.level">{{ levelText(report.level) }}</span>
              <span>{{ $t('inspScore') }} {{ scoreText(report.score) }}</span>
              <span>{{ $t('inspCounts', { c: report.criticalCount, w: report.warningCount, i: report.infoCount }) }}</span>
            </div>
          </div>
          <button class="ghostbtn" data-testid="insp-download" @click="download"><Download :size="14" />{{ $t('inspDownload') }}</button>
        </div>
        <div v-if="findings.length" class="findings">
          <div v-for="(f, i) in findings" :key="i" class="frow" :class="f.severity">
            <span class="lv" :class="f.severity === 'info' ? 'ok' : f.severity">{{ $t('inspSev_' + f.severity) }}</span>
            <span v-if="f.category" class="fcat">{{ f.category }}</span>
            <span class="ftitle">{{ f.title }}</span>
            <span v-if="f.object" class="fobj mono">{{ f.object }}</span>
          </div>
          <button v-if="(report.findings || []).length > 5" class="morebtn" @click="showAllFindings = !showAllFindings">
            {{ showAllFindings ? $t('inspFindingsLess') : $t('inspFindingsAll', { n: (report.findings || []).length }) }}
          </button>
        </div>
        <!-- 沙箱:只给 allow-scripts。不要加 allow-same-origin / allow-top-navigation / allow-popups(见文件头)。 -->
        <iframe class="frame" sandbox="allow-scripts" :src="api.inspectionViewUrl(report.id)" :title="report.instanceName" data-testid="insp-frame" />
      </div>
    </template>

    <!-- 报告历史 -->
    <template v-else-if="instanceId">
      <button class="backlink" @click="back"><ChevronLeft :size="14" />{{ $t('inspBackList') }}</button>
      <div class="card">
        <div class="chead">
          <div class="ct">{{ currentRow?.instance || '' }}</div>
          <div class="cs">{{ $t('inspHistorySub', { n: historyTotal }) }}</div>
        </div>
        <div v-if="!history.length" class="empty">{{ $t('inspHistoryNone') }}</div>
        <div v-for="h in history" :key="h.id" class="hrow" data-testid="insp-history" @click="openReport(h.id)">
          <span class="hdate mono">{{ h.reportDate }}</span>
          <span class="lv" :class="h.level">{{ levelText(h.level) }}</span>
          <span class="hscore">{{ $t('inspScore') }} {{ scoreText(h.score) }}</span>
          <span class="hcnt">{{ $t('inspCounts', { c: h.criticalCount, w: h.warningCount, i: h.infoCount }) }}</span>
        </div>
        <div v-if="historyTotal > pageSize" class="pager">
          <button class="ghostbtn" :disabled="page <= 1" @click="page--">{{ $t('inspPrevPage') }}</button>
          <span>{{ page }} / {{ Math.ceil(historyTotal / pageSize) }}</span>
          <button class="ghostbtn" :disabled="page * pageSize >= historyTotal" @click="page++">{{ $t('inspNextPage') }}</button>
        </div>
      </div>
    </template>

    <!-- 实例列表 -->
    <template v-else>
      <div class="toolbar">
        <div class="search"><Search :size="14" /><input v-model="kw" :placeholder="$t('inspSearchPh')" data-testid="insp-search" /></div>
        <div class="chips">
          <button v-for="f in (['all', 'critical', 'warning', 'missing', 'ok'] as const)" :key="f" class="chip" :class="{ on: filter === f }" :data-testid="'insp-filter-' + f" @click="filter = f">
            {{ $t('inspFilter_' + f) }}<template v-if="f !== 'all'"> {{ counts[f] }}</template>
          </button>
        </div>
      </div>
      <div class="card">
        <div v-if="loaded && !rows.length" class="empty">{{ $t('inspNoInstances') }}</div>
        <div v-else-if="loaded && rows.every((r) => !r.report)" class="empty">{{ $t('inspNoReportsYet') }}</div>
        <div v-for="r in shown" :key="r.connectionId" class="irow" :class="state(r)" data-testid="insp-row" @click="openInstance(r.connectionId)">
          <CircleAlert v-if="state(r) === 'critical'" :size="15" class="hi" />
          <TriangleAlert v-else-if="state(r) === 'warning'" :size="15" class="hi" />
          <CircleHelp v-else-if="state(r) === 'missing'" :size="15" class="hi" />
          <CircleCheck v-else :size="15" class="hi" />
          <span class="iname">{{ r.instance }}</span>
          <span class="itier">{{ tierCode(r.env) }}</span>
          <span class="ieng">{{ r.engine }}</span>
          <span class="idate mono">{{ r.report ? r.report.reportDate : '—' }}</span>
          <span class="lv" :class="state(r) === 'missing' ? 'missing' : r.report?.level">{{ state(r) === 'missing' ? $t('inspMissing') : levelText(r.report?.level) }}</span>
          <span class="iscore">{{ scoreText(r.report?.score) }}</span>
          <span class="icnt">{{ r.report ? $t('inspCounts', { c: r.report.criticalCount, w: r.report.warningCount, i: r.report.infoCount }) : '' }}</span>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; display: flex; flex-direction: column; gap: 12px; }
.page > * { flex-shrink: 0; }
.card { border-radius: var(--radius-lg); background: var(--surface-card); border: 1px solid var(--border-subtle); overflow: hidden; }
.chead { display: flex; align-items: baseline; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.ct { font: 700 13.5px var(--font-display); color: var(--text-strong); }
.cs { font: 500 11.5px var(--font-body); color: var(--text-faint); }
.empty { padding: 26px 16px; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-faint); }
.mono { font-family: var(--font-mono); }
.ghostbtn { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--surface-card); color: var(--text-body); font: 600 12px var(--font-body); cursor: pointer; }
.ghostbtn:disabled { opacity: 0.55; cursor: default; }
.backlink { align-self: flex-start; display: inline-flex; align-items: center; gap: 4px; border: none; background: none; color: var(--accent-text); font: 600 12.5px var(--font-body); cursor: pointer; padding: 0; }
.toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.search { display: flex; align-items: center; gap: 7px; height: 34px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--surface-card); color: var(--text-faint); min-width: 260px; }
.search input { flex: 1; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12.5px var(--font-body); }
.chips { display: flex; gap: 6px; flex-wrap: wrap; }
.chip { height: 30px; padding: 0 11px; border: 1px solid var(--border-default); border-radius: 15px; background: var(--surface-card); color: var(--text-muted); font: 600 12px var(--font-body); cursor: pointer; }
.chip.on { border-color: var(--accent-text); color: var(--accent-text); background: var(--accent-subtle); }
/* 等级标签:严重红、警告黄、正常绿、没收到灰 */
.lv { flex-shrink: 0; padding: 1px 7px; border-radius: 4px; font: 600 11px var(--font-body); background: var(--surface-sunken); color: var(--text-muted); }
.lv.critical { background: var(--danger-subtle); color: var(--danger-text); }
.lv.warning { background: var(--warning-subtle); color: var(--warning-text); }
.lv.ok { background: var(--success-subtle); color: var(--success-text); }
.irow { display: grid; grid-template-columns: 18px minmax(160px, 1.6fr) 64px 110px 96px 76px 64px minmax(150px, 1fr); align-items: center; gap: 10px; padding: 9px 16px; border-top: 1px solid var(--border-subtle); font: 500 12.5px var(--font-body); cursor: pointer; }
.irow:first-child { border-top: none; }
.irow:hover { background: var(--surface-sunken); }
.irow .hi { color: var(--success-text); }
.irow.critical .hi { color: var(--danger-text); }
.irow.warning .hi { color: var(--warning-text); }
.irow.missing .hi { color: var(--text-faint); }
.iname { color: var(--text-strong); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.itier { justify-self: start; padding: 1px 6px; border-radius: 4px; background: var(--surface-sunken); color: var(--text-muted); font-size: 10.5px; font-weight: 600; }
.ieng, .idate, .iscore, .icnt { color: var(--text-muted); font-size: 11.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hrow { display: flex; align-items: center; gap: 14px; padding: 10px 16px; border-top: 1px solid var(--border-subtle); font: 500 12.5px var(--font-body); cursor: pointer; }
.hrow:hover { background: var(--surface-sunken); }
.hdate { color: var(--text-strong); font-weight: 600; width: 96px; }
.hscore, .hcnt { color: var(--text-muted); font-size: 11.5px; }
.pager { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 10px; border-top: 1px solid var(--border-subtle); font: 500 12px var(--font-body); color: var(--text-muted); }
.viewer { display: flex; flex-direction: column; }
.vhead { display: flex; align-items: center; gap: 12px; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.grow { flex: 1; min-width: 0; }
.vt { font: 700 14px var(--font-display); color: var(--text-strong); }
.vs { margin-top: 4px; display: flex; align-items: center; gap: 12px; font: 500 12px var(--font-body); color: var(--text-muted); }
.findings { padding: 8px 16px; border-bottom: 1px solid var(--border-subtle); }
.frow { display: flex; align-items: center; gap: 8px; padding: 4px 0; font: 500 12px var(--font-body); }
.fcat { color: var(--text-muted); font-size: 11px; }
.ftitle { color: var(--text-strong); }
.fobj { color: var(--text-faint); font-size: 11px; }
.morebtn { margin-top: 4px; border: none; background: none; padding: 0; color: var(--accent-text); font: 600 11.5px var(--font-body); cursor: pointer; }
.frame { width: 100%; height: 75vh; border: none; background: #fff; }
@media (max-width: 900px) {
  .irow { grid-template-columns: 18px 1fr auto; }
  .irow .itier, .irow .ieng, .irow .idate, .irow .iscore, .irow .icnt { display: none; }
}
</style>
