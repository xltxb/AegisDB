<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScanSearch, ShieldCheck, AlertOctagon, PanelRightClose } from 'lucide-vue-next'
import type { Connection, Member, RiskCommandView } from '@/types'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{
  risk: 'idle' | 'safe' | 'high'
  riskCommands: RiskCommandView[]
  conn: Connection | null
  chain: Member[]
}>()
const emit = defineEmits<{ collapse: [] }>()
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
// 发起人排在最前,后面是审批链上的人。角色标签分开存,不再和名字拼成一行字符串 ——
// "谁" 和 "他在这条链上是什么身份" 是两件事,挤成一句话时后者最先被读丢。
const chainView = computed(() => {
  const me = auth.me
  const head = me ? [{ i: me.initials, n: me.name, role: t('initiated'), me: true }] : []
  const rest = props.chain.map((m) => ({ i: m.initials, n: m.name, role: m.dept || t('chainReviewer'), me: false }))
  return [...head, ...rest]
})
</script>

<template>
  <div class="scy insp">
    <div class="ihead">
      <div class="eyebrow">{{ $t('ctxTitle') }}</div>
      <button class="collapse" :title="$t('ctxCollapse')" @click="emit('collapse')"><PanelRightClose :size="15" /></button>
    </div>

    <div class="riskcard" :style="{ borderColor: meta.border, background: meta.bg }">
      <component :is="meta.icon" :size="22" :color="meta.color" />
      <div class="rtxt">
        <div class="rt" :style="{ color: meta.color }">{{ meta.title }}</div>
        <div class="rs">{{ meta.sub }}</div>
      </div>
    </div>

    <!-- 目标与角色:紧凑的键值卡片,而不是四行贴着面板边的裸文字 -->
    <div class="kvcard">
      <div class="row"><span>{{ $t('targetInst') }}</span><b class="mono">{{ conn ? conn.env + '-' + conn.name : '—' }}</b></div>
      <div class="row"><span>{{ $t('dbLabel') }}</span><b class="mono">{{ conn?.engine || '—' }}</b></div>
      <div class="row"><span>{{ $t('connRole') }}</span><b class="mono azure">{{ conn?.defaultRole || '—' }}</b></div>
      <div class="row"><span>{{ $t('gwPolicy') }}</span><b class="mono">{{ conn?.policy || '—' }}</b></div>
    </div>

    <div class="sechead">
      <span class="eyebrow2">{{ $t('limitList') }}</span>
      <span class="envtag">{{ env.toUpperCase() }}</span>
    </div>
    <!-- 虚线边框:这些不是可点的按钮,是"碰不得"的清单。实线胶囊在这一栏里会和
         上面那张键值卡片抢,读起来像另一组可操作的东西。 -->
    <div class="chips">
      <span
        v-for="c in prodChips" :key="c.name" class="chip" :class="c.high ? 'danger' : 'warn'"
        :title="c.high ? $t('limitTipHigh', { cmd: c.name, env: env.toUpperCase() }) : $t('limitTipMid', { cmd: c.name, env: env.toUpperCase() })"
      >{{ c.name }}</span>
      <span v-if="!prodChips.length" class="nolimit">{{ $t('limitNone') }}</span>
    </div>
    <div class="hint">{{ $t('limitHint') }}</div>

    <div class="sechead mt"><span class="eyebrow2">{{ $t('chainTitle') }}</span></div>
    <div class="chain">
      <div v-for="(p, i) in chainView" :key="i" class="cr">
        <div class="avawrap">
          <div class="ava" :class="{ me: p.me }">{{ p.i }}</div>
          <span class="online" />
        </div>
        <div class="cbody">
          <div class="cn">{{ p.n }}</div>
          <div class="crole">{{ p.role }}</div>
        </div>
        <span class="rtag" :class="{ me: p.me }">{{ p.me ? $t('initiated') : $t('chainApprover') }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.insp { border-left: 1px solid var(--border-subtle); background: var(--surface-card); padding: 14px 16px 24px; }
.ihead { display: flex; align-items: center; gap: 8px; }
.eyebrow { flex: 1; font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); text-transform: uppercase; }
.collapse { display: grid; place-items: center; width: 24px; height: 24px; border: none; border-radius: 7px; background: transparent; color: var(--text-faint); cursor: pointer; }
.collapse:hover { background: var(--surface-sunken); color: var(--text-body); }
.eyebrow2 { font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-faint); text-transform: uppercase; }
.sechead { margin-top: 18px; display: flex; align-items: center; gap: 8px; }
.sechead.mt { margin-top: 20px; }
.envtag { padding: 1px 7px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 9.5px var(--font-mono); color: var(--text-muted); }

.riskcard {
  margin-top: 12px; display: flex; align-items: center; gap: 10px; padding: 12px 14px;
  border: 1px solid; border-radius: 12px;
}
.rtxt { min-width: 0; }
.rt { font: 700 14px var(--font-display); letter-spacing: -0.01em; }
.rs { font: 500 11px var(--font-mono); color: var(--text-muted); }

/* 键值卡片:一块沉底的小卡,把四项框在一起 —— 它们回答的是同一个问题("这条
   连接是什么"),散着排时和下面的受限命令看着是同一层内容。 */
.kvcard { margin-top: 14px; padding: 12px 13px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); display: flex; flex-direction: column; gap: 9px; }
.row { display: flex; justify-content: space-between; align-items: center; gap: 10px; }
.row span { font: 500 11.5px var(--font-body); color: var(--text-muted); white-space: nowrap; }
.row b { min-width: 0; font: 500 11.5px var(--font-mono); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row b.azure { color: var(--accent-text); }
.mono { font-family: var(--font-mono); }

.chips { margin-top: 10px; display: flex; flex-wrap: wrap; gap: 6px; }
.chip { height: 22px; padding: 0 9px; display: inline-flex; align-items: center; border-radius: 7px; border: 1px dashed; font: 600 10.5px var(--font-mono); cursor: help; }
.chip.danger { background: var(--danger-subtle); color: var(--danger-text); border-color: var(--danger); }
.chip.warn { background: var(--warning-subtle); color: var(--warning-text); border-color: var(--warning); }
.nolimit { font: 500 11px var(--font-mono); color: var(--text-faint); }
.hint { margin-top: 8px; font: 500 10px var(--font-mono); color: var(--text-faint); line-height: 1.5; }

.chain { margin-top: 11px; display: flex; flex-direction: column; gap: 8px; }
.cr { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.avawrap { position: relative; flex-shrink: 0; }
.ava {
  width: 28px; height: 28px; border-radius: 50%; background: var(--surface-raised); border: 1px solid var(--border-default);
  display: grid; place-items: center; font: 600 10px var(--font-body); color: var(--text-muted);
}
.ava.me { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; border: none; }
/* 在线点画在头像上,而不是另起一列 —— 它是这个人的属性,不是一列数据。 */
.online { position: absolute; right: -1px; bottom: -1px; width: 9px; height: 9px; border-radius: 50%; background: var(--success); border: 2px solid var(--surface-sunken); }
.cbody { flex: 1; min-width: 0; }
.cn { font: 600 12px var(--font-body); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.crole { font: 500 10.5px var(--font-body); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rtag { flex-shrink: 0; padding: 2px 7px; border-radius: 999px; background: var(--surface-raised); border: 1px solid var(--border-subtle); font: 600 9.5px var(--font-mono); color: var(--text-muted); }
.rtag.me { background: var(--accent-subtle); border-color: transparent; color: var(--accent-text); }
</style>
