<script setup lang="ts">
// 实例名旁的巡检标记(.scratch/inspection-report 工单 05):只在最近一份报告是严重 / 警告时出现,
// 正常不显示。悬停看日期与问题摘要;点一下在新标签页打开报告 —— 不打断当前页面(终端会话)。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { CircleAlert, TriangleAlert } from 'lucide-vue-next'
import type { InspectionReport } from '@/types'

const props = defineProps<{ report?: InspectionReport | null }>()
const { t } = useI18n()
const show = computed(() => props.report && (props.report.level === 'critical' || props.report.level === 'warning'))
const tip = computed(() => {
  const r = props.report
  if (!r) return ''
  const head = t('inspBadgeTip', { date: r.reportDate, level: t('inspLevel_' + r.level), c: r.criticalCount, w: r.warningCount })
  return r.topFinding ? head + '\n' + r.topFinding : head
})
function open() {
  const r = props.report
  if (r) window.open(`#/inspection?instance=${r.connectionId}&report=${r.id}`, '_blank')
}
</script>

<template>
  <span v-if="show" class="ibadge" :class="report!.level" :title="tip" data-testid="insp-badge" @click.stop="open">
    <CircleAlert v-if="report!.level === 'critical'" :size="12" />
    <TriangleAlert v-else :size="12" />
  </span>
</template>

<style scoped>
.ibadge { display: inline-flex; align-items: center; flex-shrink: 0; cursor: pointer; margin-left: 4px; }
.ibadge.critical { color: var(--danger-text); }
.ibadge.warning { color: var(--warning-text); }
</style>
