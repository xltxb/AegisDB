<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Webhook, KeyRound, Check, CircleCheck, Loader, CircleX } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import type { WebhookConfig, WebhookDelivery } from '@/types'

const { locale } = useI18n()
const ui = useUIStore()
const webhook = ref<WebhookConfig | null>(null)
const whState = ref<'idle' | 'sending' | 'ok'>('idle')
const whEnabled = ref(true)
const whEndpoint = ref('')
const whSecret = ref('')
const whHasSecret = ref(false)
const whEvents = ref<string[]>(['exec'])
const whMessage = ref('')
const showLog = ref(false)
const deliveries = ref<WebhookDelivery[]>([])
const lastDelivery = computed(() => deliveries.value[0] || null)

// Webhook只转发审计日志类事件(命令执行 / 登录);命令拦截与审批流转不推送。
const EVENT_DEFS = [
  { key: 'exec', label: 'evExec', cls: 'accent' },
  { key: 'login', label: 'evLogin', cls: 'off' },
]
const KNOWN_EVENTS = EVENT_DEFS.map(e => e.key)

const retryText = computed(() => {
  const n = webhook.value?.retryMax ?? 5
  return locale.value === 'zh' ? `最多重试 ${n} 次` : `Up to ${n} retries`
})
function fmtTime(s: string) { return new Date(s).toLocaleTimeString('en-GB') }

// persistWebhook saves and reports success so optimistic toggles can roll back
// on failure instead of leaving the UI out of sync with the server (R28).
async function persistWebhook(): Promise<boolean> {
  try {
    webhook.value = await api.saveWebhook({
      endpoint: whEndpoint.value,
      secret: whSecret.value.trim(),
      events: whEvents.value.join(','),
      enabled: whEnabled.value,
    })
    return true
  } catch (e) {
    ui.notifyError(e, 'Webhook 保存失败')
    return false
  }
}
async function toggleWh() {
  const next = !whEnabled.value
  whEnabled.value = next
  if (!(await persistWebhook())) whEnabled.value = !next
}
async function toggleEvent(key: string) {
  const before = [...whEvents.value]
  const i = whEvents.value.indexOf(key)
  if (i >= 0) whEvents.value.splice(i, 1)
  else whEvents.value.push(key)
  if (!(await persistWebhook())) whEvents.value = before
}
async function testWebhook() {
  if (whState.value === 'sending') return
  whState.value = 'sending'
  try {
    const res = await api.testWebhook()
    whMessage.value = res?.message || ''
    whState.value = 'ok'
    await loadDeliveries()
  } catch { whState.value = 'idle' }
}
async function toggleLog() {
  showLog.value = !showLog.value
  if (showLog.value) await loadDeliveries()
}
async function loadDeliveries() {
  deliveries.value = await api.webhookDeliveries()
}

onMounted(async () => {
  try {
    const s = await api.settings()
    webhook.value = s.webhook
    if (s.webhook) {
      whEnabled.value = s.webhook.enabled
      whEndpoint.value = s.webhook.endpoint
      whSecret.value = '' // secret is never returned; blank means "keep unchanged" on save
      whHasSecret.value = !!s.webhookHasSecret
      // Drop retired events (intercept/approve) so re-saving persists only the
      // audit-log events this webhook still forwards.
      whEvents.value = (s.webhook.events || '').split(',').filter(Boolean).filter(k => KNOWN_EVENTS.includes(k))
    }
    await loadDeliveries()
  } catch { /* ignore */ }
})

const whState_ = computed(() => {
  const zh = locale.value === 'zh'
  if (whState.value === 'sending')
    return { status: zh ? '正在发送测试事件…' : 'Sending test event…', icon: Loader, color: 'var(--accent-text)', spin: true }
  if (whState.value === 'ok')
    return { status: whMessage.value || (zh ? '测试事件已投递' : 'Test event delivered'), icon: CircleCheck, color: 'var(--success-text)', spin: false }
  const d = lastDelivery.value
  if (!d)
    return { status: zh ? '尚未投递' : 'No deliveries yet', icon: Webhook, color: 'var(--text-muted)', spin: false }
  const prefix = zh ? '上次投递' : 'Last delivery'
  return {
    status: `${prefix} ${fmtTime(d.createdAt)} · ${d.status} · ${d.attempts}×`,
    icon: d.success ? CircleCheck : CircleX,
    color: d.success ? 'var(--success-text)' : 'var(--danger-text)',
    spin: false,
  }
})
</script>

<template>
  <div class="wh">
    <div class="whhead">
      <div class="whic"><Webhook :size="18" color="var(--accent-text)" /></div>
      <div class="grow"><div class="wht">{{ $t('whTitle') }}</div><div class="whs">{{ $t('whSub') }}</div></div>
      <span class="whbadge" :style="{ background: whEnabled ? 'var(--success-subtle)' : 'var(--surface-sunken)', color: whEnabled ? 'var(--success-text)' : 'var(--text-muted)' }"><span class="dotc" />{{ whEnabled ? $t('whEnabled') : $t('disabled') }}</span>
      <VSwitch :model-value="whEnabled" @update:model-value="toggleWh" />
    </div>
    <div class="whgrid">
      <div>
        <div class="fl">{{ $t('whEndpoint') }}</div>
        <div class="whbox"><span class="method">POST</span><input class="ep" v-model="whEndpoint" @change="persistWebhook" /></div>
      </div>
      <div>
        <div class="fl">{{ $t('whSecret') }}</div>
        <div class="whbox"><KeyRound :size="14" color="var(--text-faint)" /><input class="ep" type="password" v-model="whSecret" :placeholder="whHasSecret ? '已配置 · 留空保持不变' : $t('whSecretPh')" @change="persistWebhook" /></div>
      </div>
      <div>
        <div class="fl">{{ $t('whEvents') }}</div>
        <div class="evs">
          <span
            v-for="e in EVENT_DEFS" :key="e.key"
            class="ev" :class="whEvents.includes(e.key) ? e.cls : 'off'"
            style="cursor:pointer" @click="toggleEvent(e.key)"
          >
            <Check v-if="whEvents.includes(e.key)" :size="12" />{{ $t(e.label as any) }}
          </span>
        </div>
      </div>
      <div>
        <div class="fl">{{ $t('whRetry') }}</div>
        <div class="retry"><span>{{ retryText }}</span><span class="sep">·</span><span>{{ $t('whRetryDesc2') }}</span><span class="sep">·</span><span class="green">{{ $t('whRetryDesc3') }}</span></div>
      </div>
    </div>
    <div class="whfoot">
      <div class="whstatus" :style="{ color: whState_.color }"><component :is="whState_.icon" :size="14" :class="{ spin: whState_.spin }" />{{ whState_.status }}</div>
      <div class="whacts"><VButton variant="secondary" height="36px" @click="toggleLog">{{ $t('whViewLog') }}</VButton><VButton variant="primary" height="36px" @click="testWebhook">{{ $t('whSendTest') }}</VButton></div>
    </div>
    <div v-if="showLog" class="whlog">
      <div v-if="!deliveries.length" class="whlog-empty">{{ $t('whLogEmpty') }}</div>
      <div v-for="d in deliveries" :key="d.id" class="whlog-row">
        <component :is="d.success ? CircleCheck : CircleX" :size="14" :color="d.success ? 'var(--success-text)' : 'var(--danger-text)'" />
        <span class="whlog-ev">{{ d.event }}</span>
        <span class="mono mute">{{ d.status }}</span>
        <span class="whlog-att">{{ $t('whLogAttempts', { n: d.attempts }) }}</span>
        <span class="mono mute whlog-time">{{ d.createdAt }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wh { border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); overflow: hidden; }
.whhead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.whic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; }
.wht { font: 600 14px var(--font-display); color: var(--text-strong); }
.whs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.whbadge { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 10px; border-radius: 999px; font: 600 11px var(--font-mono); }
.dotc { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.whgrid { padding: 18px 20px; display: grid; grid-template-columns: 1.6fr 1fr; gap: 18px 22px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.whbox { display: flex; align-items: center; gap: 8px; height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; }
.method { display: inline-flex; align-items: center; height: 20px; padding: 0 7px; border-radius: 6px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.ep { font: 500 13px var(--font-mono); color: var(--text-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
input.ep { flex: 1; min-width: 0; background: transparent; border: none; outline: none; }
.mono { font: 500 13px var(--font-mono); }
.mute { color: var(--text-muted); }
.evs { display: flex; flex-wrap: wrap; gap: 7px; }
.ev { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 11px; border-radius: 8px; font: 600 11px var(--font-mono); }
.ev.danger { background: var(--danger-subtle); color: var(--danger-text); }
.ev.warn { background: var(--warning-subtle); color: var(--warning-text); }
.ev.accent { background: var(--accent-subtle); color: var(--accent-text); }
.ev.off { border: 1px dashed var(--border-default); color: var(--text-faint); }
.retry { display: flex; align-items: center; gap: 14px; font: 500 12px var(--font-mono); color: var(--text-muted); }
.retry .sep { color: var(--border-strong); }
.retry .green { color: var(--success-text); }
.whfoot { display: flex; align-items: center; gap: 12px; padding: 14px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.whstatus { display: flex; align-items: center; gap: 7px; font: 500 12px var(--font-mono); }
.whacts { margin-left: auto; display: flex; gap: 10px; }
.whlog { border-top: 1px solid var(--border-subtle); background: var(--surface-sunken); padding: 8px 20px 14px; display: flex; flex-direction: column; }
.whlog-empty { padding: 14px 0; font: 500 12px var(--font-body); color: var(--text-faint); text-align: center; }
.whlog-row { display: flex; align-items: center; gap: 10px; padding: 9px 0; border-bottom: 1px solid var(--border-subtle); font: 500 12px var(--font-mono); }
.whlog-row:last-child { border-bottom: none; }
.whlog-ev { color: var(--text-strong); min-width: 78px; }
.whlog-att { color: var(--text-muted); }
.whlog-time { margin-left: auto; color: var(--text-faint); }
.spin { animation: spin 0.9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
