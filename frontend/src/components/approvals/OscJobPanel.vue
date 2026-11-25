<script setup lang="ts">
// 在线变更工单的进度与控制(.scratch/osc-product 工单 05)。
//
// 数据来自 GET /osc/jobs/:id —— 进度由后端每 3 秒从引擎落库,这里读库,刷新、换人看都一样。
// 进行中每 3 秒再取一次;结束了就停。控制按钮只给 canControl 的人(能访问实例、对这条 DDL 有
// 生产操作权限),中止前要确认:中止 = 删掉影子表,原表不动。
// 网关重启打断的任务是 interrupted(工单 07):影子表与进度都在,给「续跑」与「放弃」。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pause, Play, OctagonX, Loader } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { localizeText } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import { confirmAction } from '@/lib/confirm'
import { oscItemText, humanSize, thousands, oscTime, oscDuration, type OSCItem } from '@/lib/oscText'
import type { OSCJobView } from '@/types'

const props = defineProps<{ jobId: number }>()
const emit = defineEmits<{ changed: [] }>()
const { t, te, locale } = useI18n()
const ui = useUIStore()

const job = ref<OSCJobView | null>(null)
const busy = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined

// 限流原因:主库负载 / 从库延迟 / 从库延迟未知。原因是引擎的规范中文,中文界面直接带上
const throttleText = computed(() => {
  const r = job.value?.throttleReason
  if (!r) return t('oscThrottled')
  if (locale.value.startsWith('zh')) return t('oscThrottledBy', { reason: r })
  // 其它语言:原因有身份(迁移 0061 之后)就译出来,没有就只说"限流中"
  const s = localizeText(r, job.value?.throttleReasonRef)
  return s !== r ? t('oscThrottledBy', { reason: s }) : t('oscThrottled')
})
const warnings = computed<OSCItem[]>(() => {
  try { return job.value?.warnings ? JSON.parse(job.value.warnings) : [] } catch { return [] }
})
const running = computed(() => job.value?.status === 'running')
// 开始 / 结束时刻与用时。执行中的"已运行"随每 3 秒的轮询刷新
const timesText = computed(() => {
  const j = job.value
  if (!j?.startedAt) return ''
  const start = new Date(j.startedAt).getTime()
  let s = t('oscStartedAt', { at: oscTime(j.startedAt) })
  if (j.finishedAt) s += t('oscEndedAt', { at: oscTime(j.finishedAt), dur: oscDuration(new Date(j.finishedAt).getTime() - start, t) })
  else if (j.status === 'running') s += t('oscRunningFor', { dur: oscDuration(Date.now() - start, t) })
  return s
})
const pct = computed(() => {
  const j = job.value
  if (!j || !j.rowsEstimated) return 0
  if (j.status === 'done') return 100
  return Math.min(100, Math.floor((j.rowsCopied * 100) / j.rowsEstimated))
})

async function load() {
  clearTimeout(timer)
  try {
    const before = job.value?.status
    job.value = await api.oscJob(props.jobId)
    // 结束的那一刻让工单列表也刷新一次(执行状态由后端回写到工单上)
    if (before === 'running' && job.value.status !== 'running') emit('changed')
  } catch { /* 拿不到就保持上一次的样子 */ }
  if (job.value?.status === 'running' || job.value?.status === 'awaiting') timer = setTimeout(load, 3000)
}
onMounted(load)
onUnmounted(() => clearTimeout(timer))
watch(() => props.jobId, load)

const interrupted = computed(() => job.value?.status === 'interrupted')

async function control(action: 'pause' | 'resume' | 'abort' | 'continue') {
  if (action === 'abort' && !(await confirmAction(t(interrupted.value ? 'oscAbandonConfirm' : 'oscAbortConfirm')))) return
  busy.value = true
  try {
    await api.oscControl(props.jobId, action)
    await load()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally {
    busy.value = false
  }
}

const phaseKey = (p?: string) => (p && te('oscPhase.' + p) ? 'oscPhase.' + p : '')
</script>

<template>
  <div v-if="job" class="oscp" data-testid="osc-panel">
    <div class="ohead">
      <span class="otag">{{ $t('oscTitle') }}</span>
      <code class="ostmt">{{ job.statement }}</code>
    </div>
    <div class="ometa">
      {{ job.database }}.{{ job.table }} · {{ $t('oscSize', { rows: thousands(job.sizeRows), size: humanSize(job.sizeBytes) }) }}
    </div>
    <ul v-if="warnings.length" class="owarn">
      <li v-for="w in warnings" :key="w.code">⚠ {{ oscItemText(w, locale, t, te) }}</li>
    </ul>

    <template v-if="job.status !== 'awaiting'">
      <div class="oprog">
        <div class="obar"><div class="ofill" :class="job.status" :style="{ width: pct + '%' }" /></div>
        <span class="opct" data-testid="osc-pct">{{ pct }}%</span>
      </div>
      <div class="ostat" data-testid="osc-stat">
        <Loader v-if="running && !job.paused" :size="13" class="spin" />
        <b>{{ phaseKey(job.phase) ? $t(phaseKey(job.phase)) : job.phase || '—' }}</b>
        <span>{{ $t('oscCopied', { n: thousands(job.rowsCopied), total: thousands(job.rowsEstimated) }) }}</span>
        <span>{{ $t('oscReplayed', { n: thousands(job.rowsReplayed) }) }}</span>
        <span v-if="job.cutOverAttempts">{{ $t('oscCutOvers', { n: job.cutOverAttempts }) }}</span>
        <span v-if="job.paused" class="obadge">{{ $t('oscPaused') }}</span>
        <span v-if="interrupted" class="obadge warn" data-testid="osc-interrupted">{{ $t('oscInterrupted') }}</span>
        <span v-if="job.throttled" class="obadge warn" :title="job.throttleReason || ''" data-testid="osc-throttle">{{ throttleText }}</span>
      </div>
      <div v-if="timesText" class="otimes" data-testid="osc-times">{{ timesText }}</div>
      <div v-if="job.status === 'done' && job.oldTable" class="onote" data-testid="osc-old">{{ $t('oscOldTable', { table: job.oldTable }) }}</div>
      <div v-if="(job.status === 'failed' || job.status === 'aborted') && job.error" class="oerr">{{ localizeText(job.error, job.errorRef) }}</div>
      <div v-if="interrupted" class="onote" data-testid="osc-interrupted-hint">{{ locale.startsWith('zh') && job.error ? job.error : $t('oscInterruptedHint') }}</div>
      <div v-if="interrupted && job.canControl" class="oacts">
        <VButton variant="primary" height="30px" :disabled="busy" data-testid="osc-continue" @click="control('continue')"><Play :size="13" />{{ $t('oscContinue') }}</VButton>
        <VButton variant="danger" height="30px" :disabled="busy" data-testid="osc-abandon" @click="control('abort')"><OctagonX :size="13" />{{ $t('oscAbandon') }}</VButton>
      </div>
      <div v-if="running && job.canControl" class="oacts">
        <VButton v-if="!job.paused" variant="secondary" height="30px" :disabled="busy" data-testid="osc-pause" @click="control('pause')"><Pause :size="13" />{{ $t('oscPause') }}</VButton>
        <VButton v-else variant="secondary" height="30px" :disabled="busy" data-testid="osc-resume" @click="control('resume')"><Play :size="13" />{{ $t('oscResume') }}</VButton>
        <VButton variant="danger" height="30px" :disabled="busy" data-testid="osc-abort" @click="control('abort')"><OctagonX :size="13" />{{ $t('oscAbort') }}</VButton>
      </div>
    </template>
    <div v-else class="onote">{{ $t('oscAwaiting') }}</div>
  </div>
</template>

<style scoped>
.otimes { font: 500 12px var(--font-mono); color: var(--text-muted); }
.oscp { margin-top: 10px; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); display: flex; flex-direction: column; gap: 8px; }
.ohead { display: flex; align-items: center; gap: 8px; min-width: 0; }
.otag { flex-shrink: 0; padding: 1px 8px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11px var(--font-body); }
.ostmt { font: 500 12px var(--font-mono); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ometa { font: 500 12px var(--font-mono); color: var(--text-muted); }
.owarn { margin: 0; padding: 0; list-style: none; font: 500 12px var(--font-body); color: var(--warning-text); display: flex; flex-direction: column; gap: 3px; }
.oprog { display: flex; align-items: center; gap: 10px; }
.obar { flex: 1; height: 8px; border-radius: 999px; background: var(--surface-card); overflow: hidden; }
.ofill { height: 100%; background: var(--accent-text); transition: width .4s; }
.ofill.done { background: var(--success-text); }
.ofill.failed, .ofill.aborted { background: var(--danger-text); }
.opct { font: 600 12px var(--font-mono); color: var(--text-strong); min-width: 36px; text-align: right; }
.ostat { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; font: 500 12px var(--font-body); color: var(--text-muted); }
.ostat b { color: var(--text-strong); }
.obadge { padding: 0 7px; border-radius: 999px; background: var(--surface-card); color: var(--text-body); font-weight: 600; }
.obadge.warn { background: var(--warning-subtle); color: var(--warning-text); }
.onote { font: 500 12px var(--font-body); color: var(--text-body); }
.oerr { font: 500 12px var(--font-mono); color: var(--danger-text); white-space: pre-wrap; }
.oacts { display: flex; gap: 8px; }
.spin { animation: oscspin .9s linear infinite; color: var(--accent-text); }
@keyframes oscspin { to { transform: rotate(360deg); } }
</style>
