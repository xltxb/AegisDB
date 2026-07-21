<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Filter, Calendar, CircleCheck, Hourglass, CircleX, TriangleAlert,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import type { AuditRow } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const rows = ref<AuditRow[]>([])
const filter = ref<'all' | 'high' | 'mid' | 'low'>('all')
const timeIdx = ref(0)
const exported = ref('')

const filterCycle = ['all', 'high', 'mid', 'low'] as const
const timeOpts = ['t24h', 't7d', 't30d']
const rangeKeys = ['24h', '7d', '30d']

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    rows.value = await api.audit(filter.value, rangeKeys[timeIdx.value])
  } catch (e) {
    ui.notifyError(e, '加载失败')
  }
}
onMounted(load)

async function cycleFilter() {
  filter.value = filterCycle[(filterCycle.indexOf(filter.value) + 1) % 4]
  await load()
}
async function cycleTime() {
  timeIdx.value = (timeIdx.value + 1) % 3
  await load()
}

const filterLabel = computed(() => ({ all: t('allRisk'), high: t('highTag'), mid: t('med'), low: t('low') }[filter.value]))

async function doExport() {
  // M14: 导出失败以 toast 呈现
  try {
    const token = localStorage.getItem('vela_token') || ''
    const res = await fetch(api.auditExportUrl(filter.value, rangeKeys[timeIdx.value]), { headers: { Authorization: `Bearer ${token}` } })
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'audit_export.csv'
    a.click()
    URL.revokeObjectURL(url)
    exported.value = `✓ 已导出 ${rows.value.length} 条`
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
function fmtTime(s: string) { return new Date(s).toLocaleTimeString('en-GB') }
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
        <div class="ctl" :style="{ color: filter === 'all' ? 'var(--text-muted)' : 'var(--accent-text)' }" @click="cycleFilter"><Filter :size="14" />{{ filterLabel }}</div>
        <div class="ctl" @click="cycleTime"><Calendar :size="14" />{{ $t(timeOpts[timeIdx] as any) }}</div>
        <VButton variant="secondary" height="36px" @click="doExport">{{ $t('exportCsv') }}</VButton>
      </div>
    </div>

    <!-- table -->
    <div class="table">
      <div class="th"><span>{{ $t('colTime') }}</span><span>{{ $t('colWho') }}</span><span>{{ $t('mInst') }}</span><span>{{ $t('colCmd') }}</span><span>{{ $t('colRisk') }}</span><span>{{ $t('colResult') }}</span><span>{{ $t('colAp') }}</span></div>
      <div v-for="r in rows" :key="r.id" class="tr">
        <span class="mono mute">{{ fmtTime(r.occurredAt) }}</span>
        <span class="who">{{ r.actor }}</span>
        <span class="mono mute">{{ r.instance }}<span v-if="r.database" class="dbtag"> / {{ r.database }}</span></span>
        <span class="mono cmd"><span :style="{ color: kwColor(r.risk) }">{{ kw(r.command) }}</span>{{ rest(r.command) }}</span>
        <span><span class="rbadge" :style="{ background: riskMeta(r.risk).bg, color: riskMeta(r.risk).c }">{{ riskMeta(r.risk).t }}</span></span>
        <span class="res" :style="{ color: resMeta(r.result).c }"><component :is="resMeta(r.result).icon" :size="12" />{{ resMeta(r.result).t }}</span>
        <span class="ap" :style="{ color: r.approvalNo ? '#8facff' : 'var(--text-faint)' }">{{ r.approvalNo ? '#' + r.approvalNo : '—' }}</span>
      </div>
    </div>
    <div class="foot">{{ $t('auditFootHint') }}</div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.acts { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.exp { font: 600 12px var(--font-mono); color: var(--success-text); }
.ctl { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; font: 500 12px var(--font-mono); cursor: pointer; color: var(--text-muted); }
.mono { font: 500 13px var(--font-mono); }
.mute { color: var(--text-muted); }
.dbtag { color: var(--text-faint); }
/* table */
.table { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.th, .tr { display: grid; grid-template-columns: 0.9fr 1fr 1.1fr 2.2fr 0.8fr 1fr 0.9fr; gap: 12px; }
.th { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.05em; color: var(--text-faint); text-transform: uppercase; }
.tr { padding: 13px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.who { font: 500 12px var(--font-body); color: var(--text-body); }
.cmd { color: var(--text-body); }
.rbadge { display: inline-flex; height: 20px; padding: 0 8px; align-items: center; border-radius: 999px; font: 600 10px var(--font-mono); }
.res { display: flex; align-items: center; gap: 5px; font: 600 11px var(--font-mono); }
.ap { font: 600 11px var(--font-mono); }
.foot { margin-top: 12px; font: 500 11px var(--font-mono); color: var(--text-faint); }
</style>
