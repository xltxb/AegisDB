<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Webhook, KeyRound, Check, CircleCheck, Loader, CircleX } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import type { WebhookConfig, WebhookDelivery, Environment } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const webhook = ref<WebhookConfig | null>(null)
const whState = ref<'idle' | 'sending' | 'ok'>('idle')
const whEnabled = ref(true)
const whEndpoint = ref('')
const whSecret = ref('')
const whHasSecret = ref(false)
const whEvents = ref<string[]>(['exec'])
// 推送的环境:空 = 全部环境。按审计行上那一刻的环境匹配;登录这类不属于任何实例的事件不受限。
const whEnvs = ref<string[]>([])
const environments = ref<Environment[]>([])
// 已选、但环境列表里已经没有的代码(环境改了名 / 被删)也要画出来,否则它在界面上看不见、却一直在过滤
const envChips = computed(() => {
  const known = environments.value.map((e) => ({ code: e.code as string, label: e.displayName || e.code, tier: e.tierCode as string }))
  const extra = whEnvs.value.filter((c) => !known.some((k) => k.code === c)).map((c) => ({ code: c, label: c, tier: '' }))
  return [...known, ...extra]
})
const whMessage = ref('')
const showLog = ref(false)
const deliveries = ref<WebhookDelivery[]>([])
const lastDelivery = computed(() => deliveries.value[0] || null)

// 能订阅的 = 审计行上**「增删改查」那一列**的取值,由服务端下发(GetSettings.webhookActions)。
//
// 按这一维而不是按事件类型:接 SIEM 的人想说的通常是"所有删除都推给我",而被拦下来的 DROP
// 和执行掉的 DROP 是两个事件类型、同一件事 —— 按类型订阅答不了那句话。
//
// 文案与审计页「增删改查」列共用同一组词条(auditAct_*),好让这里勾的那一枚和那一列里显示
// 的那一枚一眼对得上。颜色也一致:删是危险色,查是主色。
const eventKeys = ref<string[]>(['create', 'delete', 'update', 'read', 'other'])
const EVENT_CLS: Record<string, string> = {
  create: 'accent', delete: 'danger', update: 'warn', read: 'accent', other: 'off',
}
const EVENT_DEFS = computed(() =>
  eventKeys.value.map((k) => ({ key: k, label: 'auditAct_' + k, cls: EVENT_CLS[k] || 'off' })),
)

const retryText = computed(() => {
  const n = webhook.value?.retryMax ?? 5
  return t('whRetryUpTo', { n })
})
function fmtTime(s: string) { return new Date(s).toLocaleTimeString('en-GB') }

// persistWebhook saves and reports success so optimistic toggles can roll back
// on failure instead of leaving the UI out of sync with the server (R28).
async function persistWebhook(): Promise<boolean> {
  try {
    const saved = await api.saveWebhook({
      endpoint: whEndpoint.value,
      secret: whSecret.value.trim(),
      events: whEvents.value.join(','),
      envs: whEnvs.value.join(','),
      enabled: whEnabled.value,
    })
    webhook.value = saved
    // 保存响应与 GET 一致:不回 secret 明文,只回 hasSecret(工单 02 第 3 条)
    whHasSecret.value = !!(saved as WebhookConfig & { hasSecret?: boolean }).hasSecret
    whSecret.value = ''
    return true
  } catch (e) {
    ui.notifyError(e, t('whSaveFailed'))
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
async function toggleEnv(code: string) {
  const before = [...whEnvs.value]
  const i = whEnvs.value.indexOf(code)
  if (i >= 0) whEnvs.value.splice(i, 1)
  else whEnvs.value.push(code)
  if (!(await persistWebhook())) whEnvs.value = before
}
async function allEnvs() {
  if (!whEnvs.value.length) return
  const before = [...whEnvs.value]
  whEnvs.value = []
  if (!(await persistWebhook())) whEnvs.value = before
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
    // 可订阅的事件清单由服务端下发。拿不到就退回一个保守的默认,而不是留空 —— 留空的话
    // 这一栏一个格子都没有,看起来像"这个功能没了"。
    if (Array.isArray(s.webhookActions) && s.webhookActions.length) {
      eventKeys.value = s.webhookActions
    }
    if (s.webhook) {
      whEnabled.value = s.webhook.enabled
      whEndpoint.value = s.webhook.endpoint
      whSecret.value = '' // secret is never returned; blank means "keep unchanged" on save
      whHasSecret.value = !!s.webhookHasSecret
      // 存着什么就显示什么,**不做过滤**。过滤掉一个界面暂时不认得的键,等于下一次保存
      // 就把它从订阅里抹掉 —— 而那是一份别人配过的东西(原先 intercept / approve 正是
      // 这么丢的)。服务端保存时会按白名单归一(handler.normalizeEvents),真正的无效值
      // 在那里就落不下来。
      whEvents.value = (s.webhook.events || '').split(',').filter(Boolean)
      whEnvs.value = (s.webhook.envs || '').split(',').filter(Boolean)
    }
    try {
      environments.value = [...(await api.environments())].sort((a, b) => a.sortOrder - b.sortOrder || a.code.localeCompare(b.code))
    } catch { /* 拿不到环境列表:已选的仍按代码画出来 */ }
    await loadDeliveries()
  } catch { /* ignore */ }
})

const whState_ = computed(() => {
  if (whState.value === 'sending')
    return { status: t('whSending'), icon: Loader, color: 'var(--accent-text)', spin: true }
  if (whState.value === 'ok')
    return { status: whMessage.value || t('whDelivered'), icon: CircleCheck, color: 'var(--success-text)', spin: false }
  const d = lastDelivery.value
  if (!d)
    return { status: t('whNoDelivery'), icon: Webhook, color: 'var(--text-muted)', spin: false }
  const prefix = t('whLastDelivery')
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
        <div class="whbox"><KeyRound :size="14" color="var(--text-faint)" /><input class="ep" type="password" v-model="whSecret" :placeholder="whHasSecret ? $t('whSecretConfigured') : $t('whSecretPh')" @change="persistWebhook" /></div>
      </div>
      <div>
        <div class="fl">{{ $t('whEvents') }}<span class="hint">{{ $t('whEventsHint') }}</span></div>
        <div class="evs" data-testid="wh-events">
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
      <div class="span2">
        <div class="fl">{{ $t('whEnvs') }}<span class="hint">{{ $t('whEnvsHint') }}</span></div>
        <div class="evs" data-testid="wh-envs">
          <span class="ev" :class="whEnvs.length ? 'off' : 'accent'" style="cursor:pointer" data-testid="wh-env-all" @click="allEnvs">
            <Check v-if="!whEnvs.length" :size="12" />{{ $t('whEnvsAll') }}
          </span>
          <span
            v-for="e in envChips" :key="e.code"
            class="ev" :class="whEnvs.includes(e.code) ? (e.tier === 'prod' ? 'danger' : 'accent') : 'off'"
            style="cursor:pointer" :title="e.code" :data-env="e.code" @click="toggleEnv(e.code)"
          >
            <Check v-if="whEnvs.includes(e.code)" :size="12" />{{ e.label }}
          </span>
        </div>
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
.wh { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.whhead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.whic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; }
.wht { font: 600 14px var(--font-display); color: var(--text-strong); }
.whs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.whbadge { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 10px; border-radius: 999px; font: 600 11px var(--font-mono); }
.dotc { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.whgrid { padding: 18px 20px; display: grid; grid-template-columns: 1.6fr 1fr; gap: 18px 22px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
/* 提示跟在小标题后面,比它还弱一档 —— 它是一句说明,不该跟那四个格子抢注意力。 */
.fl .hint { margin-left: 8px; opacity: 0.75; }
.span2 { grid-column: 1 / -1; }
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
