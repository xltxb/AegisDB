<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Shield, GitPullRequestArrow, Lock, Bell, Palette, Sailboat, KeyRound } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import MfaModal from '@/components/modals/MfaModal.vue'
import IpAllowlistModal from '@/components/modals/IpAllowlistModal.vue'
import WebhookPanel from '@/components/settings/WebhookPanel.vue'
import { useI18n } from 'vue-i18n'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import api from '@/api'
import type { Member } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()

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
const strict = ref(true)
const apprTimeout = ref('')
const escalate = ref(true)
const ttl = ref('')
const idle = ref(true)
const idleMinutes = ref(15)
const mfa = ref(true)
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
const saved = ref(false)
const approvers = ref<Member[]>([])
const ipAllow = ref('10.20.0.0/16')
const execTimeout = ref('30s')
const scriptPath = ref('')
const exportPath = ref('')

const policyOpts = ['strict', 'approve-1', 'audit-only']

function parse<T>(raw: string | undefined, def: T): T {
  if (raw === undefined) return def
  try { return JSON.parse(raw) as T } catch { return def }
}

onMounted(async () => {
  apprTimeout.value = t('autoEscalate')
  ttl.value = t('ttl8')
  try {
    const s = await api.settings()
    const g = s.settings || {}
    strict.value = s.strictMode
    policy.value = parse(g['gateway.defaultPolicy'], 'strict')
    escalate.value = parse(g['approval.escalate'], true)
    mfa.value = parse(g['security.requireMFA'], true)
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
    const onTimeout = parse<string>(g['approval.onTimeout'], 'auto-escalate')
    apprTimeout.value = onTimeout === 'auto-reject' ? t('autoReject') : onTimeout === 'keep-waiting' ? t('keepWaiting') : t('autoEscalate')
    const ttlKey = parse<string>(g['security.sessionTTL'], '8h')
    ttl.value = ttlKey === '4h' ? t('ttl4') : ttlKey === '24h' ? t('ttl24') : t('ttl8')
    ipAllow.value = parse<string>(g['security.ipAllowlist'], '10.20.0.0/16')
    ipAllowEnabled.value = parse<boolean>(g['security.ipAllowEnabled'], false)
    scriptPath.value = parse<string>(g['script.savePath'], '')
    exportPath.value = parse<string>(g['export.savePath'], '')
    execTimeout.value = parse<number>(g['gateway.execTimeout'], 30) + 's'
  } catch { /* ignore */ }
  try { approvers.value = (await api.approvalChain()).chain } catch { /* ignore */ }
})

async function toggleStrict() {
  const next = !strict.value
  strict.value = next // optimistic
  try {
    await api.saveSettings({ strictMode: next })
  } catch (e) {
    strict.value = !next // roll back so the UI matches the server (R28)
    ui.notifyError(e, '保存失败')
  }
}

async function save() {
  const onTimeout = apprTimeout.value === t('autoReject') ? 'auto-reject'
    : apprTimeout.value === t('keepWaiting') ? 'keep-waiting' : 'auto-escalate'
  const ttlKey = ttl.value === t('ttl4') ? '4h' : ttl.value === t('ttl24') ? '24h' : '8h'
  try {
    await api.saveSettings({
      strictMode: strict.value,
      'gateway.defaultPolicy': policy.value,
      'approval.onTimeout': onTimeout,
      'approval.escalate': escalate.value,
      'script.savePath': scriptPath.value.trim(),
      'export.savePath': exportPath.value.trim(),
      'security.sessionTTL': ttlKey,
      'security.requireMFA': mfa.value,
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
    })
    saved.value = true
    setTimeout(() => (saved.value = false), 2200)
    ui.notify('设置已保存', 'success')
  } catch (e) {
    ui.notifyError(e, '设置保存失败') // R28: no more silent failure
  }
}
</script>

<template>
  <div class="scy page">
    <div class="col">
      <!-- Gateway -->
      <section class="card">
        <div class="shead"><div class="sic"><Shield :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setGw') }}</div><div class="ss">{{ $t('setGwSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefPolicy') }}</div><div class="rd">{{ $t('setDefPolicyD') }}</div></div><div class="w180"><VSelect v-model="policy" :options="policyOpts" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setStrict') }}</div><div class="rd">{{ $t('setStrictD') }}</div></div><VSwitch :model-value="strict" @update:model-value="toggleStrict" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTimeout') }}</div><div class="rd">{{ $t('setTimeoutD') }}</div></div><div class="chipval">{{ execTimeout }}</div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setScriptPath') }}</div><div class="rd">{{ $t('setScriptPathD') }}</div></div><input v-model="scriptPath" class="pathinput" :placeholder="$t('setScriptPathPlaceholder')" /></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setExportPath') }}</div><div class="rd">{{ $t('setExportPathD') }}</div></div><input v-model="exportPath" class="pathinput" :placeholder="$t('setExportPathPlaceholder')" /></div>
      </section>

      <!-- Approval -->
      <section class="card">
        <div class="shead"><div class="sic"><GitPullRequestArrow :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppr') }}</div><div class="ss">{{ $t('setApprSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setApprTimeout') }}</div><div class="rd">{{ $t('setApprTimeoutD') }}</div></div><div class="w180"><VSelect v-model="apprTimeout" :options="[$t('autoReject'), $t('autoEscalate'), $t('keepWaiting')]" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefApprovers') }}</div><div class="rd">{{ $t('setDefApproversD') }}</div></div><div class="approvers"><span v-for="a in approvers" :key="a.id" class="apv"><span class="ava">{{ a.initials }}</span>{{ a.name }}</span></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setEscalate') }}</div><div class="rd">{{ $t('setEscalateD') }}</div></div><VSwitch v-model="escalate" /></div>
      </section>

      <!-- Security -->
      <section class="card">
        <div class="shead"><div class="sic"><Lock :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setSec') }}</div><div class="ss">{{ $t('setSecSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTtl') }}</div><div class="rd">{{ $t('setTtlD') }}</div></div><div class="w160"><VSelect v-model="ttl" :options="[$t('ttl8'), $t('ttl4'), $t('ttl24')]" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setIdle') }}</div><div class="rd">{{ $t('setIdleD') }}</div></div><VSwitch v-model="idle" /></div>
        <div v-if="idle" class="srow"><div class="grow"><div class="rt">空闲锁定时长</div><div class="rd">无操作多少分钟后自动锁定(不超过会话有效期)</div></div><div class="w160"><input v-model.number="idleMinutes" type="number" min="1" max="1440" class="lkin" /></div></div>
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
      <section class="card">
        <div class="shead"><div class="sic"><Bell :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setNotify') }}</div><div class="ss">{{ $t('setNotifySub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLark') }}</div><div class="rd">{{ $t('setLarkD') }}</div></div><VSwitch v-model="lark" /></div>
        <div v-if="lark" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('larkWebhook') }}</label><input v-model="larkWebhook" class="lkin" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('larkSecret') }}</label><input v-model="larkSecret" type="password" class="lkin" :placeholder="hasLarkSecret ? '已配置 · 留空保持不变' : $t('larkSecretPh')" /></div>
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
      <WebhookPanel />

      <!-- Appearance -->
      <section class="card">
        <div class="shead"><div class="sic"><Palette :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppearance') }}</div><div class="ss">{{ $t('setAppearanceSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLangRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.lang === 'zh' }" @click="ui.setLang('zh')">中文</div><div class="si" :class="{ active: ui.lang === 'en' }" @click="ui.setLang('en')">English</div></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setThemeRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.theme === 'dark' }" @click="ui.setTheme('dark')">{{ $t('themeDark') }}</div><div class="si" :class="{ active: ui.theme === 'light' }" @click="ui.setTheme('light')">{{ $t('themeLight') }}</div><div class="si" :class="{ active: ui.theme === 'system' }" @click="ui.setTheme('system')">{{ $t('themeSystem') }}</div></div></div>
      </section>

      <!-- About -->
      <div class="about">
        <div class="alogo"><Sailboat :size="18" color="#fff" /></div>
        <div class="grow"><div class="an">Vela Gateway</div><div class="av">{{ $t('verLabel') }} 2.4.1 · {{ $t('buildLabel') }} 20260624</div></div>
        <span v-if="saved" class="savedtag">{{ $t('saved') }}</span>
        <VButton variant="primary" @click="save">{{ $t('saveSettings') }}</VButton>
      </div>
    </div>

    <MfaModal :open="mfaModalOpen" :enabled="mfaBound" @close="mfaModalOpen = false" @changed="onMfaChanged" />
    <IpAllowlistModal :open="ipModalOpen" :enabled="ipAllowEnabled" :list="ipAllow" @close="ipModalOpen = false" @save="onIpSave" />
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.col { max-width: 860px; display: flex; flex-direction: column; gap: 18px; }
.card { border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); overflow: hidden; }
.shead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.sic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.st { font: 600 14px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.srow { display: flex; align-items: center; gap: 16px; padding: 14px 20px; border-bottom: 1px solid var(--border-subtle); }
.larkcfg { padding: 6px 20px 16px; border-bottom: 1px solid var(--border-subtle); display: flex; flex-direction: column; gap: 10px; }
.lkrow { display: flex; align-items: center; gap: 12px; }
.lkl { width: 128px; flex-shrink: 0; font: 500 12px var(--font-body); color: var(--text-muted); }
.lkin { flex: 1; height: 36px; box-sizing: border-box; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); padding: 0 12px; font: 500 12.5px var(--font-mono); color: var(--text-strong); outline: none; }
.lkin:focus { border-color: var(--accent-text); }
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
.w160 { width: 160px; }
.chipval { font: 600 13px var(--font-mono); color: var(--text-body); background: var(--surface-sunken); border: 1px solid var(--border-default); border-radius: 8px; padding: 7px 14px; }
.pathinput { width: 300px; height: 34px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12px var(--font-mono); outline: none; }
.pathinput:focus { border-color: var(--accent-text); }
.approvers { display: flex; gap: 6px; }
.apv { display: inline-flex; align-items: center; gap: 6px; padding: 5px 11px 5px 5px; border: 1px solid var(--border-default); border-radius: 999px; font: 600 11px var(--font-body); color: var(--text-body); }
.ava { width: 20px; height: 20px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.ipwrap { display: flex; align-items: center; gap: 10px; }
.ipwrap .mono { font: 600 12px var(--font-mono); color: var(--text-muted); }
.badge { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); }
.badge.on { background: var(--success-subtle); color: var(--success-text); }
.badge.off { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 9px; overflow: hidden; }
.si { height: 34px; line-height: 34px; padding: 0 16px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); border-left: 1px solid var(--border-subtle); }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.about { display: flex; align-items: center; gap: 14px; padding: 16px 20px; border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); }
.alogo { width: 34px; height: 34px; border-radius: 9px; background: linear-gradient(135deg, #3b6ef6, #2dcde6); display: flex; align-items: center; justify-content: center; }
.an { font: 700 14px var(--font-display); color: var(--text-strong); }
.av { font: 500 11px var(--font-mono); color: var(--text-faint); margin-top: 2px; }
.savedtag { font: 600 12px var(--font-mono); color: var(--success-text); }
</style>
