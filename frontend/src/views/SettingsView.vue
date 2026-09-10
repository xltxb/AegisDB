<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { EyeOff, Shield, GitPullRequestArrow, Lock, Bell, Palette, Sailboat, KeyRound, Webhook, DatabaseZap } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import MfaModal from '@/components/modals/MfaModal.vue'
import IpAllowlistModal from '@/components/modals/IpAllowlistModal.vue'
import WebhookPanel from '@/components/settings/WebhookPanel.vue'
import ApiClientsPanel from '@/components/settings/ApiClientsPanel.vue'
import SensitiveColumnsPanel from '@/components/settings/SensitiveColumnsPanel.vue'
import { useI18n } from 'vue-i18n'
import { APPROVAL_TIMEOUT_KEYS, SESSION_TTL_KEYS, labelOf, keyOf, keyForLabel } from '@/lib/settingOptions'
import { useRouter } from 'vue-router'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import api from '@/api'
import type { Member } from '@/types'

const router = useRouter()
// The approval chain is the "DBA 负责人" role membership — managed on the
// Permissions page, not here (settings only displays it).
function goPerms() { router.push('/permissions') }

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()

// Tabbed sections — only the active panel renders, so the page stays compact.
const tabs = [
  { id: 'gateway', icon: Shield, label: 'setGw' },
  { id: 'meta', icon: DatabaseZap, label: 'setMeta' },
  { id: 'approval', icon: GitPullRequestArrow, label: 'setAppr' },
  { id: 'security', icon: Lock, label: 'setSec' },
  { id: 'sensitive', icon: EyeOff, label: 'sensTitle' },
  { id: 'notify', icon: Bell, label: 'setNotify' },
  { id: 'webhook', icon: Webhook, label: 'setWebhookTab' },
  { id: 'openapi', icon: KeyRound, label: 'setOpenApi' },
  { id: 'appearance', icon: Palette, label: 'setAppearance' },
]
const activeTab = ref('gateway')

// per-user MFA enrollment (distinct from the requireMFA policy toggle)
const mfaModalOpen = ref(false)
const mfaBound = computed(() => auth.me?.mfaEnabled ?? false)
async function onMfaChanged() {
  try { await auth.fetchMe() } catch { /* ignore */ }
}

// IP allowlist
const ipAllowEnabled = ref(false)
const ipModalOpen = ref(false)
async function onIpSave(payload: { enabled: boolean; list: string }) {
  ipAllowEnabled.value = payload.enabled
  ipAllow.value = payload.list
  ipModalOpen.value = false
  await api.saveSettings({ 'security.ipAllowEnabled': payload.enabled, 'security.ipAllowlist': payload.list })
}

const policy = ref('strict')
const apprTimeout = ref('')
const escalate = ref(true)
const allowSelf = ref(false)
const ttl = ref('')
const idle = ref(true)
const idleMinutes = ref(15)
const mfa = ref(true)
// Force every account to enrol before it may touch PROD. Off by default: turning
// it on blocks production work for anyone not yet enrolled, so it is a rollout
// decision rather than a default.
const mfaMandatory = ref(false)
// How long one PROD step-up vouches for a session on that instance.
const mfaGrace = ref(30)
const lark = ref(true)
const email = ref(false)
const push = ref(true)
// Lark (飞书) channel config — the approval card is pushed to this bot webhook.
const larkWebhook = ref('')
const larkSecret = ref('')
const hasLarkSecret = ref(false)
const consoleURL = ref('')
const larkTest = ref<{ state: 'idle' | 'sending' | 'ok' | 'err'; msg: string }>({ state: 'idle', msg: '' })
async function testLark() {
  if (larkTest.value.state === 'sending') return
  larkTest.value = { state: 'sending', msg: '' }
  try {
    await api.saveSettings({ 'notify.larkWebhook': larkWebhook.value.trim(), 'notify.larkSecret': larkSecret.value.trim(), 'notify.consoleURL': consoleURL.value.trim() })
    const r = await api.testLark()
    larkTest.value = { state: r.ok ? 'ok' : 'err', msg: r.message }
  } catch { larkTest.value = { state: 'err', msg: t('larkTestFail') } }
}
// External 飞书审批(审批魔方)对接配置(ADR-0003)
const extEnabled = ref(false)
const extBaseURL = ref('')
const extToken = ref('')
const hasExtToken = ref(false)
const extAiGroup = ref('')
const extCallbackBaseURL = ref('')
const extCallbackSecret = ref('')
const hasExtCallbackSecret = ref(false)
const extAllowIPs = ref('')
// The callback URL to register with the vendor. The vendor authenticates by
// sending the callback secret as `Authorization: Bearer <secret>` (configured on
// their side); the URL itself carries no secret.
const extCallbackURL = computed(() => {
  const b = extCallbackBaseURL.value.trim().replace(/\/+$/, '')
  return b ? `${b}/api/v1/approvals/lark/callback` : ''
})
const saved = ref(false)
const approvers = ref<Member[]>([])
const ipAllow = ref('')
const execTimeout = ref(30) // command execution timeout, seconds
const scriptPath = ref('')
const exportPath = ref('')
const exportMaxRows = ref(5000000) // per-job export caps; 0 = unlimited
const exportMaxBytes = ref(2000000000)
const exportTimeout = ref(1800) // per-job export execution budget, seconds
const exportRetention = ref(3)  // 归档在服务器上保留几天,0 = 永久保留
// 元数据同步。默认**关着** —— 打开它意味着这台网关会周期性地登录每一台实例,
// 那必须是一次明确的决定,不能因为升级了一个版本就自己开始跑。
const metaEnabled = ref(false)
const metaIntervalHrs = ref(24)
const metaConcurrency = ref(2)

const policyOpts = ['strict', 'approve-1', 'audit-only']

function parse<T>(raw: string | undefined, def: T): T {
  if (raw === undefined) return def
  try { return JSON.parse(raw) as T } catch { return def }
}

onMounted(async () => {
  apprTimeout.value = 'auto-escalate'
  ttl.value = '8h'
  try {
    const s = await api.settings()
    const g = s.settings || {}
    policy.value = parse(g['gateway.defaultPolicy'], 'strict')
    escalate.value = parse(g['approval.escalate'], true)
    allowSelf.value = parse(g['approval.allowSelfApprove'], false)
    mfa.value = parse(g['security.requireMFA'], true)
    mfaMandatory.value = parse(g['security.mfaMandatory'], false)
    mfaGrace.value = Number(parse(g['security.mfaGraceMinutes'], 30)) || 30
    idle.value = parse(g['security.idleLock'], true)
    idleMinutes.value = Number(parse(g['security.idleMinutes'], 15)) || 15
    lark.value = parse(g['notify.lark'], true)
    email.value = parse(g['notify.email'], false)
    push.value = parse(g['notify.push'], true)
    larkWebhook.value = parse<string>(g['notify.larkWebhook'], '')
    // The secret is never returned; keep the field blank (blank on save == keep
    // unchanged) and just remember whether one is on file. (R3)
    larkSecret.value = ''
    hasLarkSecret.value = !!s.secretsSet?.['notify.larkSecret']
    consoleURL.value = parse<string>(g['notify.consoleURL'], '')
    apprTimeout.value = keyOf(parse<string>(g['approval.onTimeout'], 'auto-escalate'), APPROVAL_TIMEOUT_KEYS, 'auto-escalate')
    ttl.value = keyOf(parse<string>(g['security.sessionTTL'], '8h'), SESSION_TTL_KEYS, '8h')
    ipAllow.value = parse<string>(g['security.ipAllowlist'], '')
    ipAllowEnabled.value = parse<boolean>(g['security.ipAllowEnabled'], false)
    scriptPath.value = parse<string>(g['script.savePath'], '')
    exportPath.value = parse<string>(g['export.savePath'], '')
    exportMaxRows.value = Number(parse(g['export.maxRows'], 5000000))
    exportMaxBytes.value = Number(parse(g['export.maxBytes'], 2000000000))
    exportTimeout.value = Number(parse(g['export.execTimeout'], 1800)) || 1800
    // `|| 3` 会把用户设的 0(永久保留)悄悄改回 3,所以这里不能用它兜底。
    exportRetention.value = Math.max(0, Number(parse(g['export.retentionDays'], 3)))
    execTimeout.value = Number(parse(g['gateway.execTimeout'], 30)) || 30
    metaEnabled.value = parse(g['meta.sync.enabled'], false)
    metaIntervalHrs.value = Number(parse(g['meta.sync.intervalHours'], 24)) || 24
    metaConcurrency.value = Number(parse(g['meta.sync.concurrency'], 2)) || 2
    // External approval — secrets (token/callbackSecret) are never returned; keep
    // the fields blank (blank on save == keep unchanged) and just note presence.
    extEnabled.value = parse(g['approval.external.enabled'], false)
    extBaseURL.value = parse<string>(g['approval.external.baseURL'], '')
    extAiGroup.value = parse<string>(g['approval.external.aiGroup'], '')
    extCallbackBaseURL.value = parse<string>(g['approval.external.callbackBaseURL'], '')
    extAllowIPs.value = parse<string>(g['approval.external.callbackAllowIPs'], '')
    extToken.value = ''
    hasExtToken.value = !!s.secretsSet?.['approval.external.token']
    extCallbackSecret.value = ''
    hasExtCallbackSecret.value = !!s.secretsSet?.['approval.external.callbackSecret']
  } catch { /* ignore */ }
  try { approvers.value = (await api.approvalChain()).chain } catch { /* ignore */ }
})

async function save() {
  // The model holds the stored key, so saving needs no reverse lookup against
  // the current language (EF11).
  const onTimeout = keyOf(apprTimeout.value, APPROVAL_TIMEOUT_KEYS, 'auto-escalate')
  const ttlKey = keyOf(ttl.value, SESSION_TTL_KEYS, '8h')
  try {
    await api.saveSettings({
      'gateway.defaultPolicy': policy.value,
      'approval.onTimeout': onTimeout,
      'approval.escalate': escalate.value,
      'approval.allowSelfApprove': allowSelf.value,
      'gateway.execTimeout': Math.max(1, Math.min(3600, Math.round(Number(execTimeout.value) || 30))),
      'script.savePath': scriptPath.value.trim(),
      'export.savePath': exportPath.value.trim(),
      // 0 = unlimited; negative input is meaningless, clamp to 0
      'export.maxRows': Math.max(0, Math.round(Number(exportMaxRows.value) || 0)),
      'export.maxBytes': Math.max(0, Math.round(Number(exportMaxBytes.value) || 0)),
      'export.execTimeout': Math.max(1, Math.round(Number(exportTimeout.value) || 1800)),
      // 0 = 永久保留,和 maxRows 的 0=不限一致;负数是笔误,归零
      'export.retentionDays': Math.max(0, Math.round(Number(exportRetention.value) || 0)),
      'meta.sync.enabled': metaEnabled.value,
      'meta.sync.intervalHours': Math.max(1, Math.min(720, Math.round(Number(metaIntervalHrs.value) || 24))),
      'meta.sync.concurrency': Math.max(1, Math.min(8, Math.round(Number(metaConcurrency.value) || 2))),
      'security.sessionTTL': ttlKey,
      'security.requireMFA': mfa.value,
      'security.mfaMandatory': mfaMandatory.value,
      'security.mfaGraceMinutes': mfaGrace.value,
      'security.idleLock': idle.value,
      'security.idleMinutes': Math.max(1, Math.round(Number(idleMinutes.value) || 15)),
      'security.ipAllowlist': ipAllow.value,
      'security.ipAllowEnabled': ipAllowEnabled.value,
      'notify.lark': lark.value,
      'notify.email': email.value,
      'notify.push': push.value,
      'notify.larkWebhook': larkWebhook.value.trim(),
      'notify.larkSecret': larkSecret.value.trim(),
      'notify.consoleURL': consoleURL.value.trim(),
      // External 飞书审批(审批魔方) — empty token/secret keeps the stored one.
      'approval.external.enabled': extEnabled.value,
      'approval.external.baseURL': extBaseURL.value.trim(),
      'approval.external.token': extToken.value.trim(),
      'approval.external.aiGroup': extAiGroup.value.trim(),
      'approval.external.callbackBaseURL': extCallbackBaseURL.value.trim(),
      'approval.external.callbackSecret': extCallbackSecret.value.trim(),
      'approval.external.callbackAllowIPs': extAllowIPs.value.trim(),
    })
    saved.value = true
    setTimeout(() => (saved.value = false), 2200)
    ui.notify(t('settingsSaved'), 'success')
  } catch (e) {
    ui.notifyError(e, t('saveFailed')) // R28: no more silent failure
  }
}
</script>

<template>
  <div class="scy page">
    <!-- Tab bar: switch between setting groups instead of one long scroll.
         刻意放在 .col 外面 —— 见下方 .tabs 的注释。 -->
    <div class="tabs">
      <button v-for="tb in tabs" :key="tb.id" class="tab" :class="{ active: activeTab === tb.id }" @click="activeTab = tb.id">
        <component :is="tb.icon" :size="15" />{{ $t(tb.label) }}
      </button>
    </div>
    <div class="col">

      <!-- Gateway -->
      <section v-show="activeTab === 'gateway'" class="card">
        <div class="shead"><div class="sic"><Shield :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setGw') }}</div><div class="ss">{{ $t('setGwSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefPolicy') }}</div><div class="rd">{{ $t('setDefPolicyD') }}</div></div><div class="w180"><VSelect v-model="policy" :options="policyOpts" /></div></div>
        <!-- 无 WHERE 的 DELETE / UPDATE 按分层开关(迁移 0030),开关在【环境分层】页 —— 
             判定的另外两层也在那套按分层的规则里,不该有第二个能改同一件事的地方。 -->
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setStrict') }}</div><div class="rd">{{ $t('setStrictD') }}</div></div><RouterLink class="srlink" to="/env-tiers">{{ $t('pStrictGo') }}</RouterLink></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTimeout') }}</div><div class="rd">{{ $t('setTimeoutD') }}</div></div><div class="w160"><input v-model.number="execTimeout" type="number" min="1" max="3600" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setScriptPath') }}</div><div class="rd">{{ $t('setScriptPathD') }}</div></div><input v-model="scriptPath" class="pathinput" :placeholder="$t('setScriptPathPlaceholder')" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportPath') }}</div><div class="rd">{{ $t('setExportPathD') }}</div></div><input v-model="exportPath" class="pathinput" :placeholder="$t('setExportPathPlaceholder')" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportMaxRows') }}</div><div class="rd">{{ $t('setExportMaxRowsD') }}</div></div><div class="w160"><input v-model.number="exportMaxRows" type="number" min="0" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportMaxBytes') }}</div><div class="rd">{{ $t('setExportMaxBytesD') }}</div></div><div class="w160"><input v-model.number="exportMaxBytes" type="number" min="0" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportTimeout') }}</div><div class="rd">{{ $t('setExportTimeoutD') }}</div></div><div class="w160"><input v-model.number="exportTimeout" type="number" min="1" class="lkin" /></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setExportRetention') }}</div><div class="rd">{{ $t('setExportRetentionD') }}</div></div><div class="w160"><input v-model.number="exportRetention" type="number" min="0" class="lkin" /></div></div>
      </section>

      <!-- 元数据同步 -->
      <section v-show="activeTab === 'meta'" class="card">
        <div class="shead"><div class="sic"><DatabaseZap :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setMeta') }}</div><div class="ss">{{ $t('setMetaSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMetaEnabled') }}</div><div class="rd">{{ $t('setMetaEnabledD') }}</div></div><VSwitch v-model="metaEnabled" /></div>
        <div v-if="metaEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setMetaInterval') }}</div><div class="rd">{{ $t('setMetaIntervalD') }}</div></div><div class="w160"><input v-model.number="metaIntervalHrs" type="number" min="1" max="720" class="lkin" /></div></div>
        <div v-if="metaEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setMetaConcurrency') }}</div><div class="rd">{{ $t('setMetaConcurrencyD') }}</div></div><div class="w160"><input v-model.number="metaConcurrency" type="number" min="1" max="8" class="lkin" /></div></div>
        <!-- 这三句不是客套话,是这个功能真实的三处行为。少说任何一句,人都会等在
             一个不会发生的事情上,或者以为某台实例是空的。 -->
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setMetaNote') }}</div>
          <div class="rd">{{ $t('setMetaNoteFirst') }}</div>
          <div class="rd">{{ $t('setMetaNoteSkip') }}</div>
          <div class="rd">{{ $t('setMetaNoteStale') }}</div>
        </div><RouterLink class="srlink" to="/connections">{{ $t('setMetaGo') }}</RouterLink></div>
      </section>

      <!-- Approval -->
      <section v-show="activeTab === 'approval'" class="card">
        <div class="shead"><div class="sic"><GitPullRequestArrow :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppr') }}</div><div class="ss">{{ $t('setApprSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setApprTimeout') }}</div><div class="rd">{{ $t('setApprTimeoutD') }}</div></div><div class="w180"><VSelect :model-value="labelOf(apprTimeout, $t)" :options="APPROVAL_TIMEOUT_KEYS.map((k) => labelOf(k, $t))" @update:model-value="(l: string) => (apprTimeout = keyForLabel(l, APPROVAL_TIMEOUT_KEYS, $t, 'auto-escalate'))" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefApprovers') }}</div><div class="rd">{{ $t('setDefApproversD') }}</div></div><div class="approvers"><span v-for="a in approvers" :key="a.id" class="apv"><span class="ava">{{ a.initials }}</span>{{ a.name }}</span><span v-if="!approvers.length" class="apv-empty">{{ $t('setApproversEmpty') }}</span><a class="apv-manage" @click="goPerms">{{ $t('setApproversManage') }}</a></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setEscalate') }}</div><div class="rd">{{ $t('setEscalateD') }}</div></div><VSwitch v-model="escalate" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setSelfApprove') }}</div><div class="rd">{{ $t('setSelfApproveD') }}</div></div><VSwitch v-model="allowSelf" /></div>
        <!-- External 飞书审批(审批魔方) -->
        <div class="srow" :class="{ last: !extEnabled }"><div class="grow"><div class="rt">{{ $t('setExtAppr') }}</div><div class="rd">{{ $t('setExtApprD') }}</div></div><VSwitch v-model="extEnabled" /></div>
        <div v-if="extEnabled" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('extBaseURL') }}</label><input v-model="extBaseURL" class="lkin" placeholder="https://approval.example.com" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extToken') }}</label><input v-model="extToken" type="password" class="lkin" :placeholder="hasExtToken ? $t('secretConfigured') : $t('extTokenPh')" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extAiGroup') }}</label><input v-model="extAiGroup" class="lkin" placeholder="K8S_AI" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extCallbackBase') }}</label><input v-model="extCallbackBaseURL" class="lkin" placeholder="https://gw.corp.io" /></div>
          <div v-if="extCallbackURL" class="lkhint">{{ $t('extCallbackFull') }}<span class="mono">{{ extCallbackURL }}</span></div>
          <div class="lkrow"><label class="lkl">{{ $t('extCallbackSecret') }}</label><input v-model="extCallbackSecret" type="password" class="lkin" :placeholder="hasExtCallbackSecret ? $t('secretConfigured') : $t('extCallbackSecretPh')" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extAllowIPs') }}</label><input v-model="extAllowIPs" class="lkin" :placeholder="$t('extAllowIPsPh')" /></div>
        </div>
      </section>

      <!-- Security -->
      <section v-show="activeTab === 'security'" class="card">
        <div class="shead"><div class="sic"><Lock :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setSec') }}</div><div class="ss">{{ $t('setSecSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTtl') }}</div><div class="rd">{{ $t('setTtlD') }}</div></div><div class="w160"><VSelect :model-value="labelOf(ttl, $t)" :options="SESSION_TTL_KEYS.map((k) => labelOf(k, $t))" @update:model-value="(l: string) => (ttl = keyForLabel(l, SESSION_TTL_KEYS, $t, '8h'))" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setIdle') }}</div><div class="rd">{{ $t('setIdleD') }}</div></div><VSwitch v-model="idle" /></div>
        <div v-if="idle" class="srow"><div class="grow"><div class="rt">{{ $t('setIdleMinutes') }}</div><div class="rd">{{ $t('setIdleMinutesDesc') }}</div></div><div class="w160"><input v-model.number="idleMinutes" type="number" min="1" max="1440" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMfa') }}</div><div class="rd">{{ $t('setMfaD') }}</div></div><VSwitch v-model="mfa" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMfaEnroll') }}</div><div class="rd">{{ $t('setMfaEnrollD') }}</div></div>
          <div class="ipwrap">
            <span class="badge" :class="mfaBound ? 'on' : 'off'"><KeyRound :size="12" />{{ mfaBound ? $t('mfaBound') : $t('mfaUnbound') }}</span>
            <VButton variant="secondary" height="34px" @click="mfaModalOpen = true">{{ mfaBound ? $t('manage') : $t('mfaBind') }}</VButton>
          </div>
        </div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setIpAllow') }}</div><div class="rd">{{ $t('setIpAllowD') }}</div></div>
          <div class="ipwrap">
            <span class="badge" :class="ipAllowEnabled ? 'on' : 'off'">{{ ipAllowEnabled ? $t('enabledTag') : $t('disabledTag') }}</span>
            <span class="mono">{{ ipAllow }}</span>
            <VButton variant="secondary" height="34px" @click="ipModalOpen = true">{{ $t('manage') }}</VButton>
          </div>
        </div>
      </section>

      <!-- Notifications -->
      <section v-show="activeTab === 'notify'" class="card">
        <div class="shead"><div class="sic"><Bell :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setNotify') }}</div><div class="ss">{{ $t('setNotifySub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLark') }}</div><div class="rd">{{ $t('setLarkD') }}</div></div><VSwitch v-model="lark" /></div>
        <div v-if="lark" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('larkWebhook') }}</label><input v-model="larkWebhook" class="lkin" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('larkSecret') }}</label><input v-model="larkSecret" type="password" class="lkin" :placeholder="hasLarkSecret ? $t('secretConfiguredPh') : $t('larkSecretPh')" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('larkConsole') }}</label><input v-model="consoleURL" class="lkin" placeholder="https://gw.corp.io" /></div>
          <div class="lkfoot">
            <span class="lkmsg" :class="larkTest.state">{{ larkTest.state === 'sending' ? $t('larkSending') : larkTest.msg }}</span>
            <VButton variant="secondary" height="34px" :disabled="!larkWebhook.trim()" @click="testLark">{{ $t('larkSendTest') }}</VButton>
          </div>
        </div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setEmail') }}</div><div class="rd">{{ $t('setEmailD') }}</div></div><VSwitch v-model="email" /></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setPush') }}</div><div class="rd">{{ $t('setPushD') }}</div></div><VSwitch v-model="push" /></div>
      </section>

      <!-- Webhook (audit event forwarding) -->
      <WebhookPanel v-show="activeTab === 'webhook'" />

      <!-- 敏感字段:规则在这里维护,脱敏在服务端做 -->
      <SensitiveColumnsPanel v-show="activeTab === 'sensitive'" />

      <!-- 开放接口凭据 -->
      <ApiClientsPanel v-show="activeTab === 'openapi'" />

      <!-- Appearance -->
      <section v-show="activeTab === 'appearance'" class="card">
        <div class="shead"><div class="sic"><Palette :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppearance') }}</div><div class="ss">{{ $t('setAppearanceSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLangRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.lang === 'zh' }" @click="ui.setLang('zh')">中文</div><div class="si" :class="{ active: ui.lang === 'en' }" @click="ui.setLang('en')">English</div></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setThemeRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.theme === 'dark' }" @click="ui.setTheme('dark')">{{ $t('themeDark') }}</div><div class="si" :class="{ active: ui.theme === 'light' }" @click="ui.setTheme('light')">{{ $t('themeLight') }}</div><div class="si" :class="{ active: ui.theme === 'system' }" @click="ui.setTheme('system')">{{ $t('themeSystem') }}</div></div></div>
      </section>

      <!-- About -->
      <div class="about">
        <div class="alogo"><Sailboat :size="18" color="#fff" /></div>
        <div class="grow"><div class="an">AegisDB</div><div class="av">{{ $t('verLabel') }} 2.4.1 · {{ $t('buildLabel') }} 20260624</div></div>
        <span v-if="saved" class="savedtag">{{ $t('saved') }}</span>
        <VButton variant="primary" @click="save">{{ $t('saveSettings') }}</VButton>
      </div>
    </div>

    <MfaModal :open="mfaModalOpen" :enabled="mfaBound" @close="mfaModalOpen = false" @changed="onMfaChanged" />
    <IpAllowlistModal :open="ipModalOpen" :enabled="ipAllowEnabled" :list="ipAllow" @close="ipModalOpen = false" @save="onIpSave" />
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max-narrow); margin-inline: auto; width: 100%; }
.col { max-width: 100%; display: flex; flex-direction: column; gap: 18px; }
/* Tab bar — switch between setting groups so only one panel shows at a time.
   它在 .col **外面**:820px 是正文的阅读宽度(一行设置项拉得太宽就难读),而页签是
   导航,不该跟着受限。八个页签一共约 830px,只差 10px 就挤不下,最后一个会孤零零
   掉到第二行 —— 那不是设计,是撞到了一条本不该管它的约束。
   窗口真的窄到放不下时仍然换行,那才是它该换行的时候。 */
.tabs { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 18px; }
.tab {
  display: flex; align-items: center; gap: 7px; height: 36px; padding: 0 14px;
  border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-card);
  font: 500 12.5px var(--font-body); color: var(--text-muted); cursor: pointer;
  transition: color .12s, border-color .12s, background .12s;
}
.tab:hover { color: var(--text-body); border-color: var(--border-strong); }
.tab.active { border-color: var(--accent-subtle-border); background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.card { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.shead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.sic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.st { font: 600 14px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.srlink { flex-shrink: 0; font: 600 12px var(--font-body); color: var(--accent-text); text-decoration: none; white-space: nowrap; }
.srlink:hover { text-decoration: underline; }
.srow { display: flex; align-items: center; gap: 16px; padding: 14px 20px; border-bottom: 1px solid var(--border-subtle); }
.larkcfg { padding: 6px 20px 16px; border-bottom: 1px solid var(--border-subtle); display: flex; flex-direction: column; gap: 10px; }
.lkrow { display: flex; align-items: center; gap: 12px; }
.lkl { width: 128px; flex-shrink: 0; font: 500 12px var(--font-body); color: var(--text-muted); }
/* min-width:0 —— 没有它,flex 项目不会缩到 input 的固有宽度(number 约 197px)以下,
   160px 的槽位就装不下,数值框会压出卡片边缘。 */
.lkin { flex: 1; min-width: 0; height: 36px; box-sizing: border-box; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); padding: 0 12px; font: 500 12.5px var(--font-mono); color: var(--text-strong); outline: none; }
.lkin:focus { border-color: var(--accent-text); }
.lkhint { margin: -4px 0 2px 140px; font: 500 11.5px var(--font-body); color: var(--text-faint); display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.lkhint .mono { font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.lkfoot { display: flex; align-items: center; gap: 12px; margin-top: 2px; }
.lkmsg { flex: 1; font: 600 12px var(--font-mono); color: var(--text-muted); }
.lkmsg.ok { color: var(--success-text); }
.lkmsg.err { color: var(--danger-text); }
.lkmsg.sending { color: var(--accent-text); }
.srow.last { border-bottom: none; }
.grow { flex: 1; }
.rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.rd { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
.w180 { width: 180px; }
/* 里面的 input 是 flex:1,父级不是 flex 容器时它按 type=number 的默认宽度
   撑到 ~177px,溢出卡片右边 —— 四个导出数值框一直是歪的。 */
.w160 { width: 160px; display: flex; flex-shrink: 0; }
.chipval { font: 600 13px var(--font-mono); color: var(--text-body); background: var(--surface-sunken); border: 1px solid var(--border-default); border-radius: 8px; padding: 7px 14px; }
.pathinput { width: 300px; height: 34px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12px var(--font-mono); outline: none; }
.pathinput:focus { border-color: var(--accent-text); }
.approvers { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
.apv-empty { font: 500 12px var(--font-body); color: var(--warning-text); }
.apv-manage { font: 600 12px var(--font-body); color: var(--accent-text); cursor: pointer; white-space: nowrap; }
.apv-manage:hover { text-decoration: underline; }
.apv { display: inline-flex; align-items: center; gap: 6px; padding: 5px 11px 5px 5px; border: 1px solid var(--border-default); border-radius: 999px; font: 600 11px var(--font-body); color: var(--text-body); }
.ava { width: 20px; height: 20px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.ipwrap { display: flex; align-items: center; gap: 10px; }
.ipwrap .mono { font: 600 12px var(--font-mono); color: var(--text-muted); }
.badge { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); }
.badge.on { background: var(--success-subtle); color: var(--success-text); }
.badge.off { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 9px; overflow: hidden; }
/* 用 flex 居中,不用 line-height —— `font:` 简写会把 line-height 一并重置成
   normal,而这里原先正是 `line-height: 34px; ... font: 600 ...`,后者把前者吹掉了,
   于是文字在盒子里贴着上边。改成 flex 之后,字号怎么调都不会再把居中弄丢。 */
.si { display: flex; align-items: center; height: 34px; padding: 0 16px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); border-left: 1px solid var(--border-subtle); }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.about { display: flex; align-items: center; gap: 14px; padding: 16px 20px; border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); }
.alogo { width: 34px; height: 34px; border-radius: 9px; background: linear-gradient(135deg, #3b6ef6, #2dcde6); display: flex; align-items: center; justify-content: center; }
.an { font: 700 14px var(--font-display); color: var(--text-strong); }
.av { font: 500 11px var(--font-mono); color: var(--text-faint); margin-top: 2px; }
.savedtag { font: 600 12px var(--font-mono); color: var(--success-text); }
</style>
