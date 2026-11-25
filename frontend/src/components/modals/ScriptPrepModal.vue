<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { FileUp, CircleCheck, Loader2, Circle } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import type { ScriptProgress } from '@/types'

// 选好脚本文件之后、扫描报告出来之前的进度页(2026-10)。
//
// 从前选完文件界面上什么都没有:浏览器读文件、把整份脚本发上去、服务端逐条扫描判定,一份几 MB
// 的脚本要十几秒,人只能干等,分不清是在跑还是没反应。这里把三步摆出来,各自给进度:
//   读取文件(浏览器本地,百分比)→ 上传(请求体,百分比)→ 扫描(服务端逐条,第 i / N 条)。
// 从「已上传脚本」里选的、示例脚本,文件已经在服务器上或就在页面里,只有扫描这一步。
const props = defineProps<{
  open: boolean
  filename: string
  /** 当前在哪一步 */
  step: 'read' | 'upload' | 'scan'
  readPct: number
  uploadPct: number
  /** 是否有"读取文件 / 上传"两步(选本地文件时才有) */
  fileSteps: boolean
  /** 服务端扫描进度(轮询来的;还没拿到时为 null) */
  progress: ScriptProgress | null
}>()
const emit = defineEmits<{ cancel: [] }>()

type StepKey = 'read' | 'upload' | 'scan'
const order: StepKey[] = ['read', 'upload', 'scan']
const steps = computed(() => (props.fileSteps ? order : (['scan'] as StepKey[])))
function state(s: StepKey): 'done' | 'active' | 'todo' {
  const cur = order.indexOf(props.step)
  const i = order.indexOf(s)
  return i < cur ? 'done' : i === cur ? 'active' : 'todo'
}

// 扫描分两段:字典扫描(scan)之后逐条判定(judge)。两段各占一半进度条,人看到的是一根一直往前走的条。
const scanPct = computed(() => {
  const p = props.progress
  if (!p || !p.total) return 0
  const part = Math.min(1, p.done / p.total)
  return Math.round((p.phase === 'judge' ? 0.5 + part / 2 : part / 2) * 100)
})
function pctOf(s: StepKey) {
  if (state(s) === 'done') return 100
  if (state(s) === 'todo') return 0
  return s === 'read' ? props.readPct : s === 'upload' ? props.uploadPct : scanPct.value
}

// 已用时间:每秒走一格。开着才走,关了就停 —— 不在后台空转。
const now = ref(Date.now())
const openedAt = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null
watch(() => props.open, (v) => {
  if (timer) { clearInterval(timer); timer = null }
  if (v) {
    openedAt.value = now.value = Date.now()
    timer = setInterval(() => { now.value = Date.now() }, 1000)
  }
}, { immediate: true })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
const elapsed = computed(() => Math.max(0, Math.floor((now.value - openedAt.value) / 1000)))
</script>

<template>
  <div v-if="open" class="prep-overlay" data-testid="script-prep">
    <div class="prep-mask" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><FileUp :size="21" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="titlerow"><div class="title">{{ $t('spTitle') }}</div><span class="fn">{{ filename }}</span></div>
            <div class="desc">{{ $t('spSub') }}</div>
          </div>
        </div>

        <div class="steps">
          <div v-for="s in steps" :key="s" class="step" :class="state(s)" :data-testid="'sp-step-' + s">
            <div class="srow">
              <CircleCheck v-if="state(s) === 'done'" :size="16" class="si done" />
              <Loader2 v-else-if="state(s) === 'active'" :size="16" class="si spin" />
              <Circle v-else :size="16" class="si" />
              <span class="sname">{{ $t(('spStep_' + s) as any) }}</span>
              <span class="sval" :data-testid="'sp-val-' + s">
                <template v-if="state(s) === 'done'">{{ $t('spDone') }}</template>
                <template v-else-if="state(s) === 'active' && s === 'scan'">
                  <template v-if="progress && progress.total">{{ $t(progress.phase === 'judge' ? 'spJudging' : 'spScanning', { d: progress.done, n: progress.total }) }}</template>
                  <template v-else>{{ $t('spWaiting') }}</template>
                </template>
                <template v-else-if="state(s) === 'active'">{{ pctOf(s) }}%</template>
              </span>
            </div>
            <div class="bar"><div class="fill" :style="{ width: pctOf(s) + '%' }" /></div>
            <div v-if="s === 'scan' && state(s) === 'active' && progress?.current" class="cur" :title="progress.current">{{ progress.current }}</div>
          </div>
        </div>
      </div>
      <div class="footer">
        <span class="el" data-testid="sp-elapsed">{{ $t('spElapsed', { s: elapsed }) }}</span>
        <VButton variant="secondary" data-testid="sp-cancel" @click="emit('cancel')">{{ $t('spCancel') }}</VButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.prep-overlay { position: fixed; inset: 0; z-index: 50; }
.prep-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 520px; max-width: 94vw; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.pad { padding: 20px 24px 4px; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; min-width: 0; }
.titlerow { display: flex; align-items: center; gap: 10px; min-width: 0; }
.title { font: 700 17px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; flex-shrink: 0; }
.fn { font: 600 12px var(--font-mono); color: #8facff; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.desc { margin-top: 3px; font: 400 12.5px/1.5 var(--font-body); color: var(--text-muted); }
.steps { margin-top: 18px; display: flex; flex-direction: column; gap: 14px; }
.step { padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 11px; background: var(--surface-sunken); }
.step.active { border-color: var(--accent-subtle-border); background: var(--accent-subtle); }
.srow { display: flex; align-items: center; gap: 9px; }
.si { color: var(--text-faint); flex-shrink: 0; }
.si.done { color: var(--success-text); }
.step.active .si { color: var(--accent-text); }
.sname { font: 600 13px var(--font-body); color: var(--text-strong); }
.step.todo .sname { color: var(--text-muted); }
.sval { margin-left: auto; font: 600 12px var(--font-mono); color: var(--accent-text); }
.step.done .sval { color: var(--success-text); }
.bar { margin-top: 9px; height: 6px; border-radius: 999px; background: var(--border-subtle); overflow: hidden; }
.fill { height: 100%; border-radius: 999px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); transition: width 0.3s ease; }
.step.done .fill { background: var(--success-text); }
.cur { margin-top: 8px; font: 500 11.5px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.footer { margin-top: 18px; display: flex; align-items: center; gap: 12px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.el { margin-right: auto; font: 600 11.5px var(--font-mono); color: var(--text-faint); }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
