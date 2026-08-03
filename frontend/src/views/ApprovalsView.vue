<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Inbox, GitPullRequestArrow, CircleCheckBig, CircleX, X, FileText } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Approval } from '@/types'

const ui = useUIStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

// The row shows a one-line summary; the full command, reason, chain and result
// live in a detail card. A command can be a whole migration script, so putting
// it in the row would either truncate it or wreck the list.
const detail = ref<Approval | null>(null)
function openDetail(a: Approval) { detail.value = a }
function closeDetail() {
  detail.value = null
  // Drop the deep-link parameter so a later reload doesn't reopen the card.
  if (route.query.ap) router.replace({ name: 'approvals', query: {} })
}
const scope = ref<'mine' | 'all'>('all')
const list = ref<Approval[]>([])

const pending = computed(() => list.value.filter((a) => a.status === 'pending').length)

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    list.value = await api.approvals(scope.value)
    auth.pendingCount = list.value.filter((a) => a.status === 'pending').length
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
onMounted(async () => {
  await load()
  await openFromQuery()
})

// The audit log links a ticket by number (?ap=AP-1234). Such a ticket may not be
// in the current tab — "mine" only lists what awaits this user — so widen to all
// before giving up, and say so plainly if it still cannot be found rather than
// leaving the click looking broken.
async function openFromQuery() {
  const ap = String(route.query.ap || '')
  if (!ap) return
  let hit = list.value.find((a) => a.apNo === ap)
  if (!hit && scope.value !== 'all') {
    scope.value = 'all'
    await load()
    hit = list.value.find((a) => a.apNo === ap)
  }
  if (hit) openDetail(hit)
  else ui.notify(t('apNotFound', { ap }), 'info')
}

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
  const verb = approve ? t('apApprove') : t('apReject')
  if (!confirmAction(t('apConfirm', { verb, cmd: a.command }))) return
  // M14: 审批/驳回失败以 toast 呈现
  try {
    if (approve) await api.approve(a.id)
    else await api.reject(a.id)
    await load()
    // Reflect the new state in the open card instead of leaving a stale one.
    if (detail.value) detail.value = list.value.find((x) => x.id === a.id) || null
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
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

    <div class="table">
      <div class="thead">
        <span>{{ $t('apNo') }}</span><span>{{ $t('apRisk') }}</span><span>{{ $t('apCommand') }}</span>
        <span>{{ $t('apInitiator') }}</span><span>{{ $t('apTarget') }}</span><span>{{ $t('apTime') }}</span>
        <span>{{ $t('apStatus') }}</span><span></span>
      </div>
      <div v-for="a in list" :key="a.id" class="tr" @click="openDetail(a)">
        <span class="mono apno">#{{ a.apNo }}</span>
        <span><span class="badge" :style="{ background: riskMeta(a).bg, color: riskMeta(a).c }">{{ $t(riskMeta(a).t as any) }}</span></span>
        <span class="mono cmd1">{{ a.command }}</span>
        <span class="who"><span class="ava">{{ a.initiator.slice(0, 2).toUpperCase() }}</span>{{ a.initiator }}</span>
        <span class="mono mute">{{ a.env.toUpperCase() }} · {{ a.instance }}</span>
        <span class="mono mute">{{ new Date(a.createdAt).toLocaleString('zh', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) }}</span>
        <span class="st" :class="a.status">
          <CircleCheckBig v-if="a.status === 'approved'" :size="13" />
          <CircleX v-else-if="a.status === 'rejected'" :size="13" />
          {{ a.status === 'pending' ? $t('apPending') : a.status === 'approved' ? $t('apDone') : $t('apRejected') }}
        </span>
        <span class="detbtn" @click.stop="openDetail(a)"><FileText :size="13" />{{ $t('apDetail') }}</span>
      </div>
      <div v-if="!list.length" class="empty">{{ $t('apEmpty') }}</div>
    </div>

    <!-- detail card: the full command and everything needed to decide on it -->
    <div v-if="detail" class="overlay">
      <div class="mask" @click="closeDetail" />
      <div class="dcard">
        <div class="dhead">
          <span class="badge" :style="{ background: riskMeta(detail).bg, color: riskMeta(detail).c }">{{ $t(riskMeta(detail).t as any) }}</span>
          <span class="dtitle">{{ detail.keyword }} {{ $t('apprCardTail') }}</span>
          <span class="mono apno">#{{ detail.apNo }}</span>
          <X :size="18" class="dx" @click="closeDetail" />
        </div>
        <div class="dbody">
          <div class="dlbl">{{ $t('apCommand') }}</div>
          <pre class="dcmd">{{ detail.command }}</pre>
          <div class="dgrid">
            <div><span class="ml">{{ $t('apInitiator') }}</span><div class="who"><span class="ava">{{ detail.initiator.slice(0, 2).toUpperCase() }}</span>{{ detail.initiator }}</div></div>
            <div><span class="ml">{{ $t('apTarget') }}</span><div class="tgt">{{ detail.env.toUpperCase() }} · {{ detail.instance }}<template v-if="detail.database"> · {{ detail.database }}</template></div></div>
          </div>
          <div class="ml">{{ $t('apReason') }}</div><div class="rt">{{ detail.reason || '—' }}</div>
          <div class="ml">{{ $t('apChain') }}</div>
          <div class="chain"><GitPullRequestArrow :size="14" color="#8facff" />{{ chainText(detail) }}</div>
          <template v-if="detail.status === 'approved' && detail.result">
            <div class="ml">{{ $t('apResult') }}</div><pre class="dcmd out">{{ detail.result }}</pre>
          </template>
        </div>
        <div class="dfoot">
          <template v-if="detail.status === 'pending' && canDecide(detail)">
            <VButton variant="danger" height="38px" @click="decide(detail, false)">{{ $t('apReject') }}</VButton>
            <VButton variant="primary" height="38px" @click="decide(detail, true)">{{ $t('apApprove') }}</VButton>
          </template>
          <span v-else-if="detail.status === 'pending'" class="waitc">{{ $t('apWaitChain') }}</span>
          <span v-else class="waitc">{{ detail.status === 'approved' ? $t('apDone') : $t('apRejected') }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.table { border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-card); overflow: hidden; }
.thead, .tr { display: grid; grid-template-columns: 110px 88px minmax(180px, 1fr) 130px 190px 120px 96px 76px; gap: 10px; align-items: center; padding: 10px 14px; }
.thead { background: var(--surface-page); font: 600 11px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; }
.tr { border-top: 1px solid var(--border-subtle); font: 500 12px var(--font-body); color: var(--text-body); cursor: pointer; }
.tr:hover { background: var(--surface-page); }
.mono { font-family: var(--font-mono); }
.mute { color: var(--text-muted); }
.apno { color: #8facff; }
.cmd1 { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--text-strong); }
.who { display: flex; align-items: center; gap: 6px; }
.ava { width: 20px; height: 20px; border-radius: 6px; background: var(--accent-subtle); color: var(--accent-text);
  font: 600 10px var(--font-mono); display: grid; place-items: center; }
.st { display: flex; align-items: center; gap: 4px; font-weight: 600; }
.st.pending { color: var(--warning-text); }
.st.approved { color: var(--success-text); }
.st.rejected { color: var(--danger-text); }
.detbtn { display: inline-flex; align-items: center; gap: 4px; justify-content: center; padding: 4px 8px;
  border: 1px solid var(--border-subtle); border-radius: 7px; color: var(--text-muted); font-size: 11px; }
.detbtn:hover { color: var(--accent-text); border-color: var(--accent-text); }
.empty { padding: 28px; text-align: center; color: var(--text-faint); font-size: 13px; }

.overlay { position: fixed; inset: 0; z-index: 60; display: grid; place-items: center; }
.mask { position: absolute; inset: 0; background: rgba(0,0,0,.45); }
.dcard { position: relative; width: min(760px, 92vw); max-height: 86vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; }
.dhead { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--border-subtle); }
.dtitle { font: 600 14px var(--font-display); color: var(--text-strong); }
.dx { margin-left: auto; cursor: pointer; color: var(--text-muted); }
.dbody { padding: 14px 16px; overflow: auto; }
.dlbl, .ml { font: 600 11px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; margin-top: 10px; }
.dcmd { margin: 6px 0 0; padding: 10px 12px; border-radius: 9px; background: var(--surface-page);
  border: 1px solid var(--border-subtle); font: 400 12px/1.65 var(--font-mono); color: var(--text-strong);
  white-space: pre-wrap; word-break: break-word; }
.dcmd.out { color: var(--text-muted); }
.dgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 10px; }
.tgt, .rt { font: 500 12px var(--font-body); color: var(--text-body); margin-top: 4px; }
.chain { display: flex; align-items: center; gap: 6px; font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.dfoot { display: flex; justify-content: flex-end; align-items: center; gap: 10px; padding: 12px 16px;
  border-top: 1px solid var(--border-subtle); }
.waitc { font: 500 12px var(--font-body); color: var(--text-muted); }

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
.badge { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.apno { margin-left: auto; font: 600 12px var(--font-mono); color: #8facff; }
.ml { font: 500 11px var(--font-body); color: var(--text-faint); }
.who { display: flex; align-items: center; gap: 7px; margin-top: 3px; font: 600 12px var(--font-body); color: var(--text-body); }
.ava { width: 22px; height: 22px; border-radius: 50%; background: #232838; border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.tgt { font: 600 12px var(--font-mono); color: var(--text-body); margin-top: 5px; }
.rt { font: 400 12.5px/1.5 var(--font-body); color: var(--text-body); margin-top: 3px; }
.chain { display: flex; align-items: center; gap: 7px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.waitc { margin-left: auto; font: 600 11.5px var(--font-mono); color: var(--text-faint); }
</style>
