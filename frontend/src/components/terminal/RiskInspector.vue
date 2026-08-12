<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScanSearch, ShieldCheck, AlertOctagon } from 'lucide-vue-next'
import type { Connection, Member, RiskCommandView } from '@/types'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{
  risk: 'idle' | 'safe' | 'high'
  riskCommands: RiskCommandView[]
  conn: Connection | null
  chain: Member[]
}>()
const { t } = useI18n()
const auth = useAuthStore()

const meta = computed(() => {
  switch (props.risk) {
    case 'safe':
      return { title: t('lowRiskTitle'), sub: t('lowRiskSub'), icon: ShieldCheck, color: 'var(--success-text)', bg: 'var(--success-subtle)', border: 'rgba(24,179,104,.4)' }
    case 'high':
      return { title: t('highRiskTitle'), sub: t('highRiskSub'), icon: AlertOctagon, color: 'var(--danger-text)', bg: 'var(--danger-subtle)', border: 'rgba(240,71,62,.4)' }
    default:
      return { title: t('idleTitle'), sub: t('idleSub'), icon: ScanSearch, color: 'var(--text-muted)', bg: 'var(--surface-sunken)', border: 'var(--border-subtle)' }
  }
})

// restricted commands for the selected connection's environment (level != off)
const env = computed(() => props.conn?.env || 'prod')
const prodChips = computed(() =>
  props.riskCommands
    .filter((c) => c.tiers[env.value] && c.tiers[env.value] !== 'off')
    .map((c) => ({ name: c.command, high: c.tiers[env.value] === 'high' })),
)
const chainView = computed(() => {
  const me = auth.me
  const head = me ? [{ i: me.initials, n: `${me.name} · ${t('initiated')}`, me: true }] : []
  const rest = props.chain.map((m) => ({ i: m.initials, n: `${m.name} · ${m.dept || ''}`, me: false }))
  return [...head, ...rest]
})
</script>

<template>
  <div class="scy insp">
    <div class="eyebrow">{{ $t('ctxTitle') }}</div>
    <div class="riskcard" :style="{ borderColor: meta.border, background: meta.bg }">
      <component :is="meta.icon" :size="22" :color="meta.color" />
      <div>
        <div class="rt" :style="{ color: meta.color }">{{ meta.title }}</div>
        <div class="rs">{{ meta.sub }}</div>
      </div>
    </div>
    <div class="kv">
      <div class="row"><span>{{ $t('targetInst') }}</span><b>{{ conn ? conn.env + '-' + conn.name : '—' }}</b></div>
      <div class="row"><span>{{ $t('dbLabel') }}</span><b>{{ conn?.engine || '—' }}</b></div>
      <div class="row"><span>{{ $t('connRole') }}</span><b class="azure">{{ conn?.defaultRole || '—' }}</b></div>
      <div class="row"><span>{{ $t('gwPolicy') }}</span><b>{{ conn?.policy || '—' }}</b></div>
    </div>
    <div class="div" />
    <div class="eyebrow2">{{ $t('limitList') }} · {{ env.toUpperCase() }}</div>
    <div class="chips">
      <span v-for="c in prodChips" :key="c.name" class="chip" :class="c.high ? 'danger' : 'warn'">{{ c.name }}</span>
    </div>
    <div class="hint">{{ $t('limitHint') }}</div>
    <div class="eyebrow2 mt">{{ $t('chainTitle') }}</div>
    <div class="chain">
      <div v-for="(p, i) in chainView" :key="i" class="cr">
        <div class="ava" :class="{ me: p.me }">{{ p.i }}</div>
        <div class="cn">{{ p.n }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.insp { border-left: 1px solid var(--border-subtle); background: var(--surface-card); padding: 16px 16px 24px; }
.eyebrow { font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); text-transform: uppercase; }
.eyebrow2 { font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-faint); text-transform: uppercase; }
.eyebrow2.mt { margin-top: 18px; }
.riskcard {
  margin-top: 14px; display: flex; align-items: center; gap: 10px; padding: 12px 14px;
  border: 1px solid; border-radius: 12px;
}
.rt { font: 700 14px var(--font-display); letter-spacing: -0.01em; }
.rs { font: 500 11px var(--font-mono); color: var(--text-muted); }
.kv { margin-top: 16px; display: flex; flex-direction: column; gap: 11px; }
.row { display: flex; justify-content: space-between; align-items: center; }
.row span { font: 500 12px var(--font-body); color: var(--text-muted); }
.row b { font: 500 12px var(--font-mono); color: var(--text-body); }
.row b.azure { color: #8facff; }
.div { margin-top: 16px; height: 1px; background: var(--border-subtle); }
.chips { margin-top: 11px; display: flex; flex-wrap: wrap; gap: 7px; }
.chip { height: 24px; padding: 0 10px; display: inline-flex; align-items: center; border-radius: 8px; font: 600 11px var(--font-mono); }
.chip.danger { background: var(--danger-subtle); color: var(--danger-text); }
.chip.warn { background: var(--warning-subtle); color: var(--warning-text); }
.hint { margin-top: 8px; font: 500 10px var(--font-mono); color: var(--text-faint); }
.chain { margin-top: 11px; display: flex; flex-direction: column; gap: 9px; }
.cr { display: flex; align-items: center; gap: 10px; }
.ava {
  width: 26px; height: 26px; border-radius: 50%; background: #232838; border: 1px solid var(--border-default);
  display: flex; align-items: center; justify-content: center; font: 600 10px var(--font-body); color: var(--text-muted);
}
.ava.me { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; border: none; }
.cn { flex: 1; font: 600 12px var(--font-body); color: var(--text-body); }
</style>
