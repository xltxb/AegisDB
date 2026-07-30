<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert, ArrowRight, Radio } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import { useAuthStore } from '@/stores/auth'
import type { Member } from '@/types'

const props = defineProps<{
  open: boolean
  command: string
  instance: string
  env: string
  risk: 'high' | 'mid'
  auditId: string
  chain: Member[]
  rule: string
}>()
const emit = defineEmits<{ cancel: []; submit: [string] }>()

const { t } = useI18n()
const auth = useAuthStore()

const reason = ref('')
watch(() => props.open, (v) => { if (v) reason.value = '' })

const chainNodes = computed(() => {
  const me = auth.me
  const head = me ? [{ i: me.initials, name: me.name, isYou: true }] : []
  const rest = props.chain.map((m) => ({ i: m.initials, name: m.name, isYou: false }))
  return [...head, ...rest]
})

const badge = () => (props.risk === 'high'
  ? { t: t('apBadgeHigh'), bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
  : { t: t('apBadgePerm'), bg: 'var(--warning-subtle)', c: 'var(--warning-text)' })
</script>

<template>
  <div v-if="open" class="overlay">
    <div class="mask" @click="emit('cancel')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><ShieldAlert :size="22" color="var(--danger)" /></div>
          <div class="grow">
            <div class="titlerow">
              <div class="title">{{ $t('mApprTitle') }}</div>
              <span class="badge" :style="{ background: badge().bg, color: badge().c }">{{ badge().t }}</span>
            </div>
            <div class="desc">{{ $t('mPolicyHit') }} <span class="b">{{ rule || $t('mPolicyName') }}</span>{{ $t('sentenceSep') }}{{ $t('mApprDesc') }}</div>
          </div>
        </div>

        <div class="cmdbox">
          <div class="cl">{{ $t('mPendCmd') }}</div>
          <div class="cmd">{{ command }}</div>
        </div>

        <div class="grid3">
          <div><div class="gl">{{ $t('mEnv') }}</div><div class="gv">{{ env.toUpperCase() }}</div></div>
          <div><div class="gl">{{ $t('mInst') }}</div><div class="gv">{{ instance }}</div></div>
          <div><div class="gl">{{ $t('mRisk') }}</div><div class="gv" :class="risk === 'high' ? 'danger' : 'warn'">{{ risk === 'high' ? $t('highTag') : $t('med') }}</div></div>
        </div>

        <div class="eyebrow">{{ $t('mChain') }}</div>
        <div class="chain">
          <template v-for="(p, i) in chainNodes" :key="i">
            <ArrowRight v-if="i > 0" :size="14" color="var(--text-faint)" />
            <div class="node" :class="{ me: p.isYou }">
              <div class="ava" :class="{ me: p.isYou }">{{ p.i }}</div>
              <span>{{ p.isYou ? $t('mYou') : p.name }}</span>
            </div>
          </template>
        </div>

        <div class="reason">
          <div class="gl">{{ $t('mReason') }} <span class="req">*</span></div>
          <textarea v-model="reason" :placeholder="$t('mReasonPh')" />
        </div>
        <div class="lark"><Radio :size="14" color="#2dcde6" />{{ $t('mLarkNote1') }} <span class="azure">Lark</span> {{ $t('mLarkNote2') }}</div>
      </div>
      <div class="footer">
        <div class="aud">{{ $t('auditId') }} · {{ auditId }}</div>
        <div class="acts">
          <VButton variant="secondary" @click="emit('cancel')">{{ $t('mCancel') }}</VButton>
          <VButton variant="primary" :disabled="!reason.trim()" @click="emit('submit', reason)">{{ $t('mSubmit') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 580px; max-width: 92vw; background: var(--surface-card);
  border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7), 0 0 0 1px rgba(45, 205, 230, 0.12);
  overflow: hidden; animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #f0473e, #ffb648); }
.pad { padding: 22px 26px 0; }
.hdr { display: flex; align-items: flex-start; gap: 14px; }
.ic { width: 42px; height: 42px; border-radius: 12px; background: var(--danger-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.titlerow { display: flex; align-items: center; gap: 10px; }
.title { font: 700 18px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.badge { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.desc { margin-top: 5px; font: 400 12.5px/1.55 var(--font-body); color: var(--text-muted); }
.desc .b { color: var(--text-body); }
.cmdbox { margin-top: 18px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); border-radius: 10px; padding: 12px 14px; }
.cl { font: 500 11px var(--font-mono); color: var(--text-faint); margin-bottom: 6px; }
.cmd { font: 500 13.5px var(--font-mono); color: var(--text-strong); word-break: break-all; }
.grid3 { margin-top: 14px; display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px; }
.gl { font: 500 11px var(--font-body); color: var(--text-faint); }
.gv { font: 600 12px var(--font-mono); color: var(--text-body); margin-top: 3px; }
.gv.danger { color: var(--danger-text); }
.gv.warn { color: var(--warning-text); }
.eyebrow { margin-top: 16px; font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-faint); text-transform: uppercase; }
.chain { margin-top: 10px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.node { display: flex; align-items: center; gap: 7px; padding: 5px 11px 5px 5px; border: 1px solid var(--border-default); border-radius: 999px; }
.node.me { border-color: var(--accent-subtle-border); background: var(--accent-subtle); }
.node span { font: 600 11px var(--font-body); color: var(--text-body); }
.node.me span { color: var(--accent-text); }
.ava { width: 22px; height: 22px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.ava.me { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; }
.reason { margin-top: 16px; }
.req { color: var(--danger-text); }
.reason textarea {
  width: 100%; box-sizing: border-box; min-height: 62px; resize: none; margin-top: 6px;
  border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken);
  padding: 11px 13px; font: 400 13px var(--font-body); color: var(--text-body); outline: none;
}
.lark { margin-top: 14px; display: flex; align-items: center; gap: 8px; font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.azure { color: #8facff; }
.footer { margin-top: 18px; display: flex; align-items: center; gap: 12px; padding: 14px 26px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.aud { font: 500 11px var(--font-mono); color: var(--text-faint); }
.acts { margin-left: auto; display: flex; gap: 10px; }
</style>
