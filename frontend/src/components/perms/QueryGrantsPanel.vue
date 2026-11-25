<script setup lang="ts">
// 查询授权总表:谁、哪个实例的哪个库、上限、有效期、来源(外部系统 / 审批单 / 审批人)。
//
// 授权由外部审批系统下发,这里不新建、不编辑 —— 只看,以及在离职、误授、安全事件时由管理员
// 立即撤销(撤销原因写进审计)。
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, Ban } from 'lucide-vue-next'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import type { QueryGrant } from '@/types'

const props = defineProps<{ isAdmin: boolean }>()
const { t } = useI18n()
const ui = useUIStore()

const items = ref<QueryGrant[]>([])
const loading = ref(false)
const email = ref('')
const dept = ref('')
const status = ref('active')
const STATUSES = ['', 'active', 'pending', 'expired', 'revoked']

async function load() {
  loading.value = true
  try {
    const r = await api.queryGrants({ email: email.value.trim(), dept: dept.value.trim(), status: status.value })
    items.value = r.items
  } catch (e) {
    ui.notifyError(e, t('loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(status, load)
let timer: ReturnType<typeof setTimeout> | null = null
watch([email, dept], () => {
  if (timer) clearTimeout(timer)
  timer = setTimeout(load, 300)
})

async function revoke(g: QueryGrant) {
  if (!props.isAdmin || g.status === 'revoked' || g.status === 'expired') return
  const reason = window.prompt(t('qgRevokePrompt', { email: g.email, target: `${g.source} / ${g.database}` }))
  if (reason === null) return
  try {
    await api.revokeQueryGrant(g.id, reason.trim())
    ui.notify(t('qgRevoked'), 'success')
    await load()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

const day = (s: string) => {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleDateString()
}
</script>

<template>
  <div class="qgp" data-testid="grants-panel">
    <div class="qgf">
      <div class="qgsearch"><Search :size="13" /><input v-model="email" :placeholder="$t('qgFilterEmail')" data-testid="qg-email" /></div>
      <div class="qgsearch"><input v-model="dept" :placeholder="$t('qgFilterDept')" data-testid="qg-dept" /></div>
      <select v-model="status" class="qgsel" data-testid="qg-status">
        <option v-for="s in STATUSES" :key="s" :value="s">{{ s ? $t(('qgSt_' + s) as any) : $t('qgAll') }}</option>
      </select>
    </div>
    <div class="qgt">
      <div class="qgh"><span>{{ $t('qgColUser') }}</span><span>{{ $t('qgColTarget') }}</span><span>{{ $t('qgColLimit') }}</span><span>{{ $t('qgColPeriod') }}</span><span>{{ $t('qgColSource') }}</span><span>{{ $t('qgColStatus') }}</span></div>
      <div v-if="!loading && !items.length" class="qgempty">{{ $t('qgEmpty') }}</div>
      <div v-for="g in items" :key="g.id" class="qgr" data-testid="qg-row">
        <div class="cell"><div class="b nm">{{ g.userName || g.email }}</div><div class="s nm">{{ g.email }} · {{ g.dept || '—' }}</div></div>
        <div class="cell"><div class="b nm mono">{{ g.database }}</div><div class="s nm mono">{{ g.source }}</div></div>
        <div class="cell s">{{ g.effectiveMaxRows }} · {{ g.effectiveTimeoutSec }}s<span v-if="g.allowExport" class="exp">{{ $t('qgExport') }}</span></div>
        <div class="cell s">{{ day(g.startsAt) }} ~ {{ day(g.endsAt) }}</div>
        <div class="cell"><div class="s nm">{{ g.client || '—' }} · {{ g.externalId }}</div><div class="s nm">{{ g.approver || '—' }}</div></div>
        <div class="cell st">
          <span class="pill" :class="g.status">{{ $t(('qgSt_' + g.status) as any) }}</span>
          <button v-if="isAdmin && (g.status === 'active' || g.status === 'pending')" class="rv" :title="$t('qgRevoke')" data-testid="qg-revoke" @click="revoke(g)"><Ban :size="13" /></button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.qgp { display: flex; flex-direction: column; gap: 12px; }
.qgf { display: flex; flex-wrap: wrap; gap: 8px; }
.qgsearch { display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-faint); }
.qgsearch input { border: 0; outline: none; background: transparent; color: var(--text-body); font: 500 12.5px var(--font-body); width: 180px; }
.qgsel { height: 32px; padding: 0 8px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-body); font: 500 12.5px var(--font-body); }
.qgt { border: 1px solid var(--border-subtle); border-radius: 12px; overflow: hidden; background: var(--surface-card); }
.qgh, .qgr { display: grid; grid-template-columns: minmax(160px, 1.6fr) minmax(140px, 1.4fr) 120px 170px minmax(140px, 1.3fr) 110px; gap: 10px; align-items: center; padding: 9px 14px; }
.qgh { background: var(--surface-sunken); font: 600 11px var(--font-body); color: var(--text-faint); border-bottom: 1px solid var(--border-subtle); }
.qgr { border-bottom: 1px solid var(--border-subtle); }
.qgr:last-child { border-bottom: 0; }
.cell { min-width: 0; }
.b { font: 600 12.5px var(--font-body); color: var(--text-strong); }
.s { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mono { font-family: var(--font-mono); }
.nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.exp { margin-left: 6px; padding: 0 5px; border-radius: 4px; background: var(--accent-subtle); color: var(--accent-text); font-size: 10.5px; }
.st { display: flex; align-items: center; gap: 6px; }
.pill { padding: 2px 8px; border-radius: 999px; font: 600 11px var(--font-body); white-space: nowrap; background: var(--surface-sunken); color: var(--text-muted); }
.pill.active { background: var(--success-subtle); color: var(--success-text); }
.pill.pending { background: var(--accent-subtle); color: var(--accent-text); }
.pill.revoked { background: var(--danger-subtle); color: var(--danger-text); }
.rv { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; border: 1px solid var(--border-default); border-radius: 6px; background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.rv:hover { color: var(--danger-text); border-color: var(--danger-text); }
.qgempty { padding: 22px; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-faint); }
@media (max-width: 900px) { .qgh { display: none; } .qgh, .qgr { grid-template-columns: 1fr 1fr; } }
</style>
