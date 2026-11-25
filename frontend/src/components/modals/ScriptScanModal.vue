<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { FileSearch, X, ShieldAlert, ShieldCheck, TriangleAlert, Hourglass, FolderArchive, Database, CircleCheck, Loader2 } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import type { ScriptProgress, ScriptScanResp } from '@/types'
import { localizeText } from '@/api/http'

const props = defineProps<{
  open: boolean; scan: ScriptScanResp | null; submitted: boolean
  savePath?: string; instance?: string; databases?: string[]; targetDb?: string
  /** 正在下发中 —— 按钮要立刻锁住,而不是等响应回来 */
  running?: boolean
  /** 这次弹窗里已经跑过/提交过了。带上时刻与执行人,让人看得出是"刚刚自己点的"。 */
  done?: { at: string; by: string; kind: 'ran' | 'submitted' } | null
  /** 下发中的进度(轮询 /scripts/progress 得来;还没拿到时为 null,遮罩退回转圈) */
  progress?: ScriptProgress | null
}>()
const emit = defineEmits<{ close: []; run: []; 'update:targetDb': [string] }>()

const risky = computed(() => props.scan?.hasRisky ?? false)
// A target database must be chosen before the script can be dispatched.
const db = computed({ get: () => props.targetDb || '', set: (v: string) => emit('update:targetDb', v) })
// 能不能点执行:选了库、没有正在跑、这次还没跑过。
// 三个条件缺一不可 —— 少了后两个,连点两下就是两次真的执行。
// 有语句被禁止:整份脚本不会执行,也不会送审 —— 按钮直接灰掉,原因写在横幅上,
// 不让人点下去才收到一句拒绝。
const blocked = computed(() => props.scan?.blocked ?? false)
const canRun = computed(() => !!db.value && !props.running && !props.done && !blocked.value)

// 下发期间整个弹窗都不接受操作 —— 关闭也不行。
//
// 按钮变灰挡得住"再点一次执行",挡不住"关掉再来一次":重新扫一遍脚本会把
// scRunning / scDone 一起重置(见 TerminalSession 里 scriptScan 之后那两行),
// 于是上一次还在飞的下发就没人记得了。这个遮罩把这条路一起封上。
//
// 不怕把人困住:scriptExecute 带的是 SCRIPT_TIMEOUT_MS 这个有界超时,
// 无论成败 finally 都会把 running 放掉,遮罩跟着消失。
function tryClose() {
  if (props.running) return
  emit('close')
}
const banner = computed(() =>
  blocked.value
    ? { text: 'scBlocked', bg: 'var(--danger-subtle)', color: 'var(--danger-text)', icon: ShieldAlert }
    : risky.value
    ? { text: 'scRisky', bg: 'var(--danger-subtle)', color: 'var(--danger-text)', icon: ShieldAlert }
    // 本该审批的语句由执行窗口放行:说出是哪个窗口,而不是笼统的"未检出需审批的语句"
    // —— 高危条数照样显示在上面,两句话放一起读才不矛盾。
    : props.scan?.window
    ? { text: 'scWindow', bg: 'var(--success-subtle)', color: 'var(--success-text)', icon: ShieldCheck }
    : { text: 'scClean', bg: 'var(--success-subtle)', color: 'var(--success-text)', icon: ShieldCheck },
)
// 执行进度卡片(2026-10):下发期间的遮罩不只转圈,说出跑到第几条、正在跑哪条、跑了多久。
// 复扫(scan / judge)与逐条执行(run)两段:复扫很快,条到 run 才是主要的那一段。
const runPct = computed(() => {
  const p = props.progress
  if (!p || !p.total) return 0
  return Math.min(100, Math.round((p.done / p.total) * 100))
})
const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | null = null
watch(() => props.running, (v) => {
  if (tick) { clearInterval(tick); tick = null }
  if (v) { now.value = Date.now(); tick = setInterval(() => { now.value = Date.now() }, 1000) }
}, { immediate: true })
onBeforeUnmount(() => { if (tick) clearInterval(tick) })
const runElapsed = computed(() => {
  const at = props.progress?.startedAt ? Date.parse(props.progress.startedAt) : NaN
  return Number.isNaN(at) ? 0 : Math.max(0, Math.floor((now.value - at) / 1000))
})
function badgeMeta(r: string) {
  if (r === 'high') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', t: 'scHigh' }
  if (r === 'mid') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', t: 'scMid' }
  return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', t: 'scSafe' }
}
</script>

<template>
  <div v-if="open && scan" class="scan-overlay">
    <div class="scan-mask" @click="tryClose" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><FileSearch :size="21" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="titlerow"><div class="title">{{ $t('scTitle') }}</div><span class="fn">{{ scan.filename }}</span></div>
            <div class="desc">{{ $t('scSub') }}</div>
          </div>
          <div class="x" :class="{ off: running }" @click="tryClose"><X :size="18" /></div>
        </div>

        <div class="stats">
          <div class="stat"><div class="n">{{ scan.total }}</div><div class="l">{{ $t('scTotal') }}</div></div>
          <div class="stat danger"><div class="n">{{ scan.high }}</div><div class="l">{{ $t('scHigh') }}</div></div>
          <div class="stat warn"><div class="n">{{ scan.mid }}</div><div class="l">{{ $t('scMid') }}</div></div>
          <div class="stat"><div class="n green">{{ scan.safe }}</div><div class="l">{{ $t('scSafe') }}</div></div>
        </div>

        <div class="banner" :style="{ background: banner.bg }">
          <component :is="banner.icon" :size="17" :color="banner.color" />
          <span :style="{ color: banner.color }" data-testid="sc-banner">{{ $t(banner.text as any, { w: scan?.window ?? '' }) }}<template v-if="blocked && scan?.blockedRule"> · {{ localizeText(scan.blockedRule, scan.blockedRuleRef) }}</template></span>
        </div>

        <div v-if="savePath" class="archive"><FolderArchive :size="13" /><span class="al">{{ $t('scArchiveAt') }}</span><code class="ap">{{ savePath }}</code></div>

        <!-- required: choose the target database within the instance -->
        <div class="target">
          <Database :size="14" color="var(--accent-text)" />
          <span class="tl">{{ $t('scTargetDb') }}</span>
          <select v-model="db" class="tsel">
            <option value="" disabled>{{ $t('scPickDb') }}</option>
            <option v-for="d in (databases || [])" :key="d" :value="d">{{ d }}</option>
          </select>
          <span class="tinst">{{ instance }}</span>
        </div>

        <div class="eyebrow">{{ $t('scStmtList') }}</div>
      </div>

      <div class="scy list-wrap">
        <div class="list">
          <div v-for="s in scan.statements" :key="s.index" class="srow" :style="{ background: s.risk === 'high' ? 'rgba(240,71,62,.05)' : 'transparent' }">
            <span class="idx">{{ String(s.index).padStart(2, '0') }}</span>
            <div class="sgrow">
              <div class="sql">{{ s.sql }}</div>
              <div v-if="s.noWhere" class="nw"><TriangleAlert :size="11" />{{ $t('scNoWhere') }}</div>
            </div>
            <span class="bdg" :style="{ background: badgeMeta(s.risk).bg, color: badgeMeta(s.risk).c }">{{ $t(badgeMeta(s.risk).t as any) }}</span>
          </div>
        </div>
      </div>

      <div v-if="submitted" class="submitted"><Hourglass :size="16" color="var(--warning-text)" /><span>{{ $t('scSubmitted') }}</span></div>

      <div class="footer">
        <div class="fl">
          <span v-if="canRun" class="confirm">{{ $t('scConfirm', { i: instance, d: db }) }}</span>
          <span v-else class="mustpick">{{ $t('scMustPick') }}</span>
        </div>
        <div class="acts">
          <!-- 已经跑过就把话说清楚:什么时候、谁点的。一个只是变灰的按钮,人只会
               再点两下然后以为界面卡住了。 -->
          <span v-if="done" class="ranmark">
            <CircleCheck :size="13" />
            {{ done.kind === 'submitted' ? $t('scAlreadySubmitted', { at: done.at, by: done.by }) : $t('scAlreadyRan', { at: done.at, by: done.by }) }}
          </span>
          <VButton variant="secondary" :disabled="running" @click="tryClose">{{ $t('scClose') }}</VButton>
          <VButton variant="primary" :disabled="!canRun" @click="canRun && emit('run')">
            {{ running ? $t('scRunning') : done ? $t('scDone') : risky ? $t('scSubmitAppr') : $t('scRun') }}
          </VButton>
        </div>
      </div>

      <!-- 下发中的遮罩。它盖住的是**整个弹窗**,不只是那颗按钮:这段时间里没有
           任何一个操作是有意义的,而一颗变灰的按钮旁边仍然可点的关闭/下拉框,
           只会让人以为界面卡住了去乱点。 -->
      <div v-if="running" class="busy" data-testid="sc-busy">
        <!-- 拿到进度且在逐条执行:进度卡片;送审、复扫、还没拿到进度时退回转圈 -->
        <div v-if="!risky && progress && progress.total" class="pcard" data-testid="sc-progress">
          <div class="ph">
            <Loader2 class="spin" :size="16" color="var(--accent-text)" />
            <span class="pt">{{ progress.phase === 'run' ? $t('scProgRun', { d: progress.done, n: progress.total }) : $t('scProgCheck', { d: progress.done, n: progress.total }) }}</span>
            <span class="pp">{{ progress.phase === 'run' ? runPct + '%' : '' }}</span>
          </div>
          <div class="pbar"><div class="pfill" :style="{ width: (progress.phase === 'run' ? runPct : 0) + '%' }" /></div>
          <div v-if="progress.current" class="pcur" :title="progress.current">{{ progress.phase === 'run' ? '#' + Math.min(progress.done + 1, progress.total) + ' ' : '' }}{{ progress.current }}</div>
          <div class="pmeta"><span>{{ $t('scProgElapsed', { s: runElapsed }) }}</span><span>{{ $t('scBusyHint') }}</span></div>
        </div>
        <template v-else>
          <Loader2 class="spin" :size="26" color="var(--accent-text)" />
          <div class="bt">{{ risky ? $t('scBusySubmit') : $t('scBusyRun') }}</div>
          <div class="bs">{{ $t('scBusyHint') }}</div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.scan-overlay { position: fixed; inset: 0; z-index: 50; }
.scan-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 680px; max-width: 94vw; max-height: 86vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.target { display: flex; align-items: center; gap: 9px; margin: 12px 0 4px; padding: 10px 12px; border: 1px solid var(--accent-subtle-border); background: var(--accent-subtle); border-radius: 10px; }
.tl { font: 600 12px var(--font-body); color: var(--text-strong); }
.tsel { flex: 1; height: 32px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-strong); font: 600 12.5px var(--font-mono); padding: 0 10px; outline: none; cursor: pointer; }
.tinst { font: 600 11px var(--font-mono); color: var(--text-faint); }
.confirm { font: 600 12px var(--font-mono); color: var(--accent-text); }
.mustpick { font: 600 12px var(--font-mono); color: var(--warning-text); }
.pad { padding: 20px 24px 0; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.titlerow { display: flex; align-items: center; gap: 10px; }
.title { font: 700 17px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.fn { font: 600 12px var(--font-mono); color: #8facff; }
.desc { margin-top: 3px; font: 400 12.5px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.stats { margin-top: 16px; display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; }
.stat { border: 1px solid var(--border-subtle); border-radius: 11px; padding: 11px 14px; background: var(--surface-sunken); }
.stat.danger { border-color: rgba(240, 71, 62, 0.3); background: var(--danger-subtle); }
.stat.warn { border-color: rgba(245, 165, 36, 0.3); background: var(--warning-subtle); }
.stat .n { font: 700 22px var(--font-display); color: var(--text-strong); }
.stat.danger .n { color: var(--danger-text); }
.stat.warn .n { color: var(--warning-text); }
.stat .n.green { color: var(--success-text); }
.stat .l { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 1px; }
.banner { margin-top: 14px; display: flex; align-items: center; gap: 9px; padding: 11px 14px; border-radius: 10px; }
.banner span { font: 600 12.5px var(--font-body); }
.archive { margin-top: 12px; display: flex; align-items: center; gap: 8px; padding: 9px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); color: var(--text-muted); }
.archive .al { font: 600 11px var(--font-mono); color: var(--text-faint); }
.archive .ap { flex: 1; min-width: 0; font: 600 12px var(--font-mono); color: var(--accent-text); word-break: break-all; }
.eyebrow { margin-top: 16px; font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-faint); text-transform: uppercase; }
.list-wrap { flex: 1; min-height: 80px; margin-top: 10px; padding: 0 24px; }
.list { display: flex; flex-direction: column; border: 1px solid var(--border-subtle); border-radius: 11px; overflow: hidden; }
.srow { display: flex; align-items: flex-start; gap: 11px; padding: 11px 14px; border-bottom: 1px solid var(--border-subtle); }
.idx { font: 600 11px var(--font-mono); color: var(--text-faint); margin-top: 1px; }
.sgrow { flex: 1; min-width: 0; }
.sql { font: 500 12.5px var(--font-mono); color: var(--text-body); word-break: break-all; }
.nw { margin-top: 4px; display: inline-flex; align-items: center; gap: 4px; font: 600 10px var(--font-mono); color: var(--danger-text); }
.bdg { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); }
.submitted { margin: 14px 24px 0; display: flex; align-items: center; gap: 9px; padding: 11px 14px; border-radius: 10px; background: var(--warning-subtle); }
.submitted span { font: 600 12.5px var(--font-body); color: var(--warning-text); }
.footer { margin-top: 16px; display: flex; align-items: center; gap: 12px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.fl { display: flex; align-items: center; gap: 6px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.ok { color: var(--success-text); }
.acts { margin-left: auto; display: flex; gap: 10px; }
.ranmark { margin-right: auto; display: inline-flex; align-items: center; gap: 6px; font: 600 11.5px var(--font-body); color: var(--success-text); }
/* 下发中的遮罩。铺满弹窗、吃掉所有指针事件 —— 它不是装饰,拦住重复下发靠的就是它。
   cursor: progress 让"现在点什么都没用"这件事在指针上就能看出来。 */
.busy {
  position: absolute; inset: 0; z-index: 2; cursor: progress;
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px;
  padding: 0 32px; text-align: center;
  background: color-mix(in srgb, var(--surface-card) 88%, transparent);
  backdrop-filter: blur(2px);
}
.busy .bt { font: 700 14px var(--font-display); color: var(--text-strong); }
.busy .bs { font: 500 12px/1.6 var(--font-body); color: var(--text-muted); max-width: 380px; }
.pcard { width: min(440px, 100%); padding: 16px 18px; border: 1px solid var(--accent-subtle-border); border-radius: 13px; background: var(--surface-card); box-shadow: 0 14px 40px -18px rgba(0, 0, 0, 0.5); text-align: left; }
.ph { display: flex; align-items: center; gap: 8px; }
.pt { font: 700 13.5px var(--font-display); color: var(--text-strong); }
.pp { margin-left: auto; font: 700 13px var(--font-mono); color: var(--accent-text); }
.pbar { margin-top: 10px; height: 7px; border-radius: 999px; background: var(--border-subtle); overflow: hidden; }
.pfill { height: 100%; border-radius: 999px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); transition: width 0.3s ease; }
.pcur { margin-top: 10px; font: 500 12px var(--font-mono); color: var(--text-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pmeta { margin-top: 8px; display: flex; justify-content: space-between; gap: 12px; font: 500 11px var(--font-body); color: var(--text-faint); }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
/* 遮罩期间关闭入口一并失效,鼠标上也要看得出来 */
.x.off { opacity: 0.4; cursor: default; }
.x.off:hover { background: transparent; }
</style>
