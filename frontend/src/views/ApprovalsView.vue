<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Inbox, GitPullRequestArrow, CircleCheckBig, CircleX } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Approval } from '@/types'

const ui = useUIStore()
const auth = useAuthStore()
const scope = ref<'mine' | 'all'>('all')
const list = ref<Approval[]>([])

const pending = computed(() => list.value.filter((a) => a.status === 'pending').length)

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    list.value = await api.approvals(scope.value)
    auth.pendingCount = list.value.filter((a) => a.status === 'pending').length
  } catch (e) {
    ui.notifyError(e, '加载失败')
  }
}
onMounted(load)

async function setScope(s: 'mine' | 'all') {
  scope.value = s
  await load()
}

// Only a member on this approval's chain may act on it (backend enforces too).
function canDecide(a: Approval) {
  return (a.steps || []).some((s) => s.approverId === auth.me?.id)
}
async function decide(a: Approval, approve: boolean) {
  if (!canDecide(a)) return
  // B3: 批准会立即真实执行该命令，驳回同样不可逆——二次确认防误点
  const verb = approve ? '批准并执行' : '驳回'
  if (!confirmAction(`确定要${verb}「${a.command}」吗？`)) return
  // M14: 审批/驳回失败以 toast 呈现
  try {
    if (approve) await api.approve(a.id)
    else await api.reject(a.id)
    await load()
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

function riskMeta(a: Approval) {
  return a.riskLevel === 'high'
    ? { t: 'highP1', bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    : { t: 'privP2', bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}
// approval chain from real step data: initiator → approvers
function chainText(a: Approval) {
  const names = (a.steps || []).map((s) => s.approver).filter(Boolean)
  return [a.initiator, ...names].join(' → ')
}
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">APPROVAL INBOX</div>
        <div class="sub">{{ pending }} {{ $t('awaitingYou') }} {{ $t('apprSubTail') }}</div>
      </div>
      <div class="tabs">
        <div class="tab" :class="{ active: scope === 'mine' }" @click="setScope('mine')"><Inbox :size="14" />{{ $t('mineAppr') }}</div>
        <div class="tab" :class="{ active: scope === 'all' }" @click="setScope('all')">{{ $t('allAppr') }}</div>
      </div>
    </div>

    <div class="cards">
      <div v-for="a in list" :key="a.id" class="card">
        <div class="stripe" />
        <div class="cbody">
          <div class="crow">
            <span class="title">{{ a.keyword }} {{ $t('apprCardTail') }}</span>
            <span class="badge" :style="{ background: riskMeta(a).bg, color: riskMeta(a).c }">{{ $t(riskMeta(a).t as any) }}</span>
            <span class="apno">#{{ a.apNo }}</span>
            <span class="time">{{ new Date(a.createdAt).toLocaleTimeString('zh', { hour: '2-digit', minute: '2-digit' }) }}</span>
          </div>
          <div class="cmd">{{ a.command }}</div>
          <div class="meta">
            <div><span class="ml">{{ $t('apInitiator') }}</span><div class="who"><div class="ava">{{ a.initiator.slice(0, 2).toUpperCase() }}</div>{{ a.initiator }}</div></div>
            <div><span class="ml">{{ $t('apTarget') }}</span><div class="tgt">{{ a.env.toUpperCase() }} · {{ a.instance }}</div></div>
          </div>
          <div class="reason"><span class="ml">{{ $t('apReason') }}</span><div class="rt">{{ a.reason || '—' }}</div></div>
          <div v-if="a.status === 'approved' && a.result" class="result">
            <span class="ml">{{ $t('apResult') }}</span>
            <div class="rout">{{ a.result }}</div>
          </div>
        </div>
        <div v-if="a.status === 'pending'" class="cfoot">
          <div class="chain"><GitPullRequestArrow :size="14" color="#8facff" />{{ chainText(a) }}</div>
          <div v-if="canDecide(a)" class="acts">
            <VButton variant="danger" height="38px" @click="decide(a, false)">{{ $t('apReject') }}</VButton>
            <VButton variant="primary" height="38px" @click="decide(a, true)">{{ $t('apApprove') }}</VButton>
          </div>
          <div v-else class="waitc">{{ $t('apWaitChain') }}</div>
        </div>
        <div v-else-if="a.status === 'approved'" class="cfoot done"><CircleCheckBig :size="15" />{{ $t('apDone') }}</div>
        <div v-else-if="a.status === 'rejected'" class="cfoot rej"><CircleX :size="15" />{{ $t('apRejected') }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.tabs { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.tab {
  display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border: 1px solid var(--border-default);
  border-radius: 10px; font: 500 12px var(--font-mono); color: var(--text-muted); cursor: pointer;
}
.tab.active { border-color: var(--accent-subtle-border); background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.cards { display: flex; flex-direction: column; gap: 14px; max-width: 920px; }
.card { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.stripe { height: 4px; background: linear-gradient(90deg, #f0473e, #ffb648); }
.cbody { padding: 16px 20px; }
.crow { display: flex; align-items: center; gap: 10px; }
.title { font: 700 15px var(--font-display); color: var(--text-strong); letter-spacing: -0.01em; }
.badge { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.apno { margin-left: auto; font: 600 12px var(--font-mono); color: #8facff; }
.time { font: 500 11px var(--font-mono); color: var(--text-faint); }
.cmd { margin-top: 12px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); border-radius: 10px; padding: 10px 13px; font: 500 13px var(--font-mono); color: var(--text-body); word-break: break-all; }
.meta { margin-top: 11px; display: grid; grid-template-columns: 1fr 1fr; gap: 9px 20px; }
.ml { font: 500 11px var(--font-body); color: var(--text-faint); }
.who { display: flex; align-items: center; gap: 7px; margin-top: 3px; font: 600 12px var(--font-body); color: var(--text-body); }
.ava { width: 22px; height: 22px; border-radius: 50%; background: #232838; border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.tgt { font: 600 12px var(--font-mono); color: var(--text-body); margin-top: 5px; }
.reason { margin-top: 10px; }
.rt { font: 400 12.5px/1.5 var(--font-body); color: var(--text-body); margin-top: 3px; }
.result { margin-top: 10px; }
.rout { margin-top: 4px; background: var(--success-subtle); border: 1px solid var(--success-subtle); border-radius: 10px; padding: 9px 13px; font: 600 12.5px var(--font-mono); color: var(--success-text); white-space: pre-wrap; word-break: break-all; }
.cfoot { display: flex; align-items: center; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.chain { display: flex; align-items: center; gap: 7px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.acts { margin-left: auto; display: flex; gap: 10px; }
.waitc { margin-left: auto; font: 600 11.5px var(--font-mono); color: var(--text-faint); }
.cfoot.done { background: var(--success-subtle); font: 600 12px var(--font-mono); color: var(--success-text); }
.cfoot.rej { background: var(--danger-subtle); font: 600 12px var(--font-mono); color: var(--danger-text); }
</style>
