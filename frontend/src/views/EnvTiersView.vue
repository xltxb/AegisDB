<script setup lang="ts">
/**
 * Control tiers and environments.
 *
 * A tier owns the rules; an environment groups instances and binds to one tier.
 * Adding a second production cluster is a new ENVIRONMENT on the existing prod
 * tier — nothing is copied and it is governed from its first second.
 *
 * Almost every control on this page is a guard rather than a convenience. Both
 * rule lookups treat a missing row as permission granted, so a tier without its
 * rules, or an instance whose environment no longer resolves, is an unregulated
 * production database that looks exactly like a regulated one. The server
 * enforces each rule; the UI states the reason up front so an operator does not
 * discover it from a rejected request.
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Layers, Boxes, Plus, X, ShieldCheck } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { EnvTier } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
// Every mutation is admin-only server-side; non-admins get the page read-only.
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))

const usage = ref<Record<string, number>>({})
const busy = ref(false)

async function reload() {
  await envtier.load(true)
  try { usage.value = await api.environmentUsage() } catch { /* counts are advisory */ }
  ui.pageSub = t('etSub', { tiers: envtier.tiers.length, envs: envtier.environments.length })
}
onMounted(async () => {
  try { await reload() } catch (e) { ui.notifyError(e, t('actionFailed')) }
})

const envCount = (code: string) => usage.value[code] ?? 0
/** Environments bound to a tier — what blocks its deletion. */
const boundEnvs = (code: string) => envtier.environments.filter((e) => e.tierCode === code)

// ---------------------------------------------------------------- tier form

const tierForm = ref(false)
const tf = ref({
  code: '', displayName: '', templateCode: '', sortOrder: 0,
  requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true,
  connLayer: '', defaultRole: 'dba_l2',
})
function openTierForm() {
  if (!isAdmin.value) return
  tf.value = {
    code: '', displayName: '', templateCode: envtier.tierCodes[0] ?? '', sortOrder: envtier.tiers.length,
    requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true,
    connLayer: '', defaultRole: 'dba_l2',
  }
  tierForm.value = true
}
async function createTier() {
  if (!tf.value.code.trim() || !tf.value.displayName.trim() || !tf.value.templateCode) return
  busy.value = true
  try {
    // The template is required by the API, not just prefilled here: a tier
    // created empty would have no capability and no dictionary rows, and every
    // lookup against it falls through to allowed.
    await api.createEnvTier({ ...tf.value, scanBaseline: false })
    tierForm.value = false
    await reload()
    ui.notify(t('etTierCreated', { code: tf.value.code, from: tf.value.templateCode }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

async function saveTier(tier: EnvTier, patch: Partial<EnvTier>) {
  if (!isAdmin.value) return
  busy.value = true
  try {
    await api.updateEnvTier(tier.code, { ...tier, ...patch })
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
    await reload() // the switch showed the attempted state; put it back
  } finally { busy.value = false }
}

/**
 * Moving the scan baseline. It is single-valued: setting it here clears it
 * elsewhere in the same transaction, and it can never be switched off outright —
 * with no baseline, script scanning matches nothing and reports every uploaded
 * script as clean, without raising an error anywhere.
 */
async function makeBaseline(tier: EnvTier) {
  if (!isAdmin.value || tier.scanBaseline) return
  if (!confirmAction(t('etBaselineConfirm', { code: tier.code }))) return
  await saveTier(tier, { scanBaseline: true })
}

async function removeTier(tier: EnvTier) {
  if (!isAdmin.value || !tierDeletable(tier)) return
  if (!confirmAction(t('etTierDeleteConfirm', { code: tier.code }))) return
  busy.value = true
  try {
    await api.deleteEnvTier(tier.code)
    await reload()
    ui.notify(t('etTierDeleted', { code: tier.code }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

const tierDeletable = (tier: EnvTier) =>
  !tier.scanBaseline && boundEnvs(tier.code).length === 0 && envtier.tiers.length > 1
/** Why the delete is unavailable — shown instead of an unexplained dead button. */
function tierBlockReason(tier: EnvTier): string {
  if (tier.scanBaseline) return t('etBlockedBaseline')
  const n = boundEnvs(tier.code).length
  if (n > 0) return t('etBlockedBound', { n })
  if (envtier.tiers.length <= 1) return t('etBlockedLast')
  return ''
}

// ------------------------------------------------------------ environment form

const envForm = ref(false)
const ef = ref({ code: '', displayName: '', tierCode: '', sortOrder: 0 })
function openEnvForm() {
  if (!isAdmin.value) return
  ef.value = {
    code: '', displayName: '', tierCode: envtier.tierCodes[0] ?? '',
    sortOrder: envtier.environments.length,
  }
  envForm.value = true
}
async function createEnvironment() {
  if (!ef.value.code.trim() || !ef.value.displayName.trim() || !ef.value.tierCode) return
  busy.value = true
  try {
    await api.createEnvironment(ef.value)
    envForm.value = false
    await reload()
    ui.notify(t('etEnvCreated', { code: ef.value.code, tier: ef.value.tierCode }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

/**
 * Rebinding an environment to another tier changes how every instance in it is
 * governed from that moment on. It does NOT rewrite history: approvals and audit
 * rows keep the tier they were judged under.
 */
async function rebind(code: string, tierLabel: string) {
  const tierCode = envtier.tierCodes.find((c) => tierOptionLabel(c) === tierLabel)
  const env = envtier.envByCode[code]
  if (!isAdmin.value || !env || !tierCode || tierCode === env.tierCode) return
  if (!confirmAction(t('etRebindConfirm', { code, from: env.tierCode, to: tierCode, n: envCount(code) }))) {
    await reload() // revert the select
    return
  }
  busy.value = true
  try {
    await api.updateEnvironment(code, { ...env, tierCode })
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
    await reload()
  } finally { busy.value = false }
}

/**
 * Deleting an environment moves its instances rather than orphaning them: an
 * instance pointing at an environment that no longer exists resolves to no tier,
 * and therefore to no rules at all.
 */
const delEnv = ref<{ open: boolean; code: string; moveTo: string }>({ open: false, code: '', moveTo: '' })
function openDeleteEnv(code: string) {
  if (!isAdmin.value || envtier.environments.length <= 1) return
  const other = envtier.environments.find((e) => e.code !== code)
  delEnv.value = { open: true, code, moveTo: other ? envOptionLabel(other.code) : '' }
}
async function confirmDeleteEnv() {
  const moveTo = envtier.environments.find((e) => envOptionLabel(e.code) === delEnv.value.moveTo)?.code
  if (!moveTo || moveTo === delEnv.value.code) return
  busy.value = true
  try {
    await api.deleteEnvironment(delEnv.value.code, moveTo)
    ui.notify(t('etEnvDeleted', { code: delEnv.value.code, n: envCount(delEnv.value.code), to: moveTo }), 'success')
    delEnv.value = { open: false, code: '', moveTo: '' }
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

// Selects bind on label strings, so labels must be unique — codes are.
const tierOptionLabel = (code: string) => `${code} · ${envtier.tierLabel(code, t)}`
const envOptionLabel = (code: string) => `${code} · ${envtier.envLabel(code)}`
const tierOptions = computed(() => envtier.tierCodes.map(tierOptionLabel))
const moveTargets = computed(() =>
  envtier.environments.filter((e) => e.code !== delEnv.value.code).map((e) => envOptionLabel(e.code)))
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">TIERS &amp; ENVIRONMENTS</div>
        <div class="sub">{{ $t('etIntro') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
    </div>

    <!-- ------------------------------------------------------------ tiers -->
    <div class="card">
      <div class="chead">
        <div class="cic"><Layers :size="17" color="var(--danger)" /></div>
        <div class="grow">
          <div class="ct">{{ $t('etTierTitle') }}</div>
          <div class="cs">{{ $t('etTierSub') }}</div>
        </div>
        <VButton v-if="isAdmin" variant="secondary" height="34px" @click="openTierForm">
          <Plus :size="14" />{{ $t('etNewTier') }}
        </VButton>
      </div>

      <div class="scx">
        <div class="tgrid">
          <div class="th">
            <span>{{ $t('etColTier') }}</span><span class="ctr">{{ $t('etColMfa') }}</span>
            <span class="ctr">{{ $t('etColBanner') }}</span><span class="ctr">{{ $t('etColPending') }}</span>
            <span class="ctr" :title="$t('etColStrictHint')">{{ $t('etColStrict') }}</span>
            <span class="ctr">{{ $t('etColBaseline') }}</span><span>{{ $t('etColBound') }}</span><span />
          </div>
          <div v-for="tier in envtier.tiers" :key="tier.code" class="tr">
            <div>
              <div class="cn"><span class="d" :class="envtier.dotFor(tier.code)" />{{ tier.code }}</div>
              <div class="cl">{{ envtier.tierLabel(tier.code, t) }}</div>
              <div class="cl2">{{ tier.connLayer }} · {{ tier.defaultRole }}</div>
            </div>
            <div class="ctr"><VSwitch :model-value="tier.requireMfa" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { requireMfa: v })" /></div>
            <div class="ctr"><VSwitch :model-value="tier.dangerBanner" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { dangerBanner: v })" /></div>
            <div class="ctr"><VSwitch :model-value="tier.countsInPending" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { countsInPending: v })" /></div>
            <!-- 判定的第三层。前两层(能力矩阵、高危命令字典)本来就按分层存,
                 这一层过去是个全局开关,于是它是唯一一道瞄不准的闸。 -->
            <div class="ctr" :title="$t('etColStrictHint')"><VSwitch :model-value="tier.strictNoWhere" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { strictNoWhere: v })" /></div>
            <!-- Radio semantics, not a switch: exactly one tier holds it, and
                 turning it off is never an option — only moving it elsewhere. -->
            <div class="ctr">
              <span v-if="tier.scanBaseline" class="basebadge" :title="$t('etBaselineHeld')"><ShieldCheck :size="13" />{{ $t('etBaselineOn') }}</span>
              <button v-else-if="isAdmin" class="baselink" :disabled="busy" @click="makeBaseline(tier)">{{ $t('etBaselineSet') }}</button>
              <span v-else class="dash">—</span>
            </div>
            <div class="mono mute">
              <template v-if="boundEnvs(tier.code).length">{{ boundEnvs(tier.code).map((e) => e.code).join(', ') }}</template>
              <span v-else class="dash">{{ $t('etNoEnv') }}</span>
            </div>
            <div class="acts">
              <button v-if="isAdmin" class="del" :disabled="!tierDeletable(tier) || busy" :title="tierBlockReason(tier)" @click="removeTier(tier)">
                {{ $t('etDelete') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="tierForm" class="form">
        <div class="fhead">{{ $t('etNewTier') }}<span class="x" @click="tierForm = false"><X :size="16" /></span></div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFCode') }}</div><input v-model="tf.code" placeholder="prod-hk" /></div>
          <div><div class="fl">{{ $t('etFName') }}</div><input v-model="tf.displayName" :placeholder="$t('etFNamePh')" /></div>
        </div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFTemplate') }}</div><VSelect v-model="tf.templateCode" :options="envtier.tierCodes" /></div>
          <div><div class="fl">{{ $t('etFLayer') }}</div><input v-model="tf.connLayer" :placeholder="$t('etLayerPh')" /></div>
        </div>
        <!-- Stating what the clone does is the whole point of the mandatory
             template: the operator is copying a rule set, not naming a label. -->
        <div class="warn">{{ $t('etCloneNote', { from: tf.templateCode || '—' }) }}</div>
        <div class="frow3">
          <label class="chk"><VSwitch v-model="tf.requireMfa" />{{ $t('etColMfa') }}</label>
          <label class="chk"><VSwitch v-model="tf.dangerBanner" />{{ $t('etColBanner') }}</label>
          <label class="chk"><VSwitch v-model="tf.countsInPending" />{{ $t('etColPending') }}</label>
          <label class="chk" :title="$t('etColStrictHint')"><VSwitch v-model="tf.strictNoWhere" />{{ $t('etColStrict') }}</label>
        </div>
        <div class="ffoot">
          <VButton variant="secondary" height="34px" @click="tierForm = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" height="34px" :disabled="busy || !tf.code || !tf.displayName || !tf.templateCode" @click="createTier">{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>

    <!-- ----------------------------------------------------- environments -->
    <div class="card">
      <div class="chead">
        <div class="cic"><Boxes :size="17" color="var(--accent-text)" /></div>
        <div class="grow">
          <div class="ct">{{ $t('etEnvTitle') }}</div>
          <div class="cs">{{ $t('etEnvSub') }}</div>
        </div>
        <VButton v-if="isAdmin" variant="secondary" height="34px" @click="openEnvForm">
          <Plus :size="14" />{{ $t('etNewEnv') }}
        </VButton>
      </div>

      <div class="scx">
        <div class="egrid">
          <div class="th"><span>{{ $t('etColEnv') }}</span><span>{{ $t('etColTierBind') }}</span><span class="ctr">{{ $t('etColInsts') }}</span><span /></div>
          <div v-for="e in envtier.environments" :key="e.code" class="tr">
            <div>
              <div class="cn"><span class="d" :class="envtier.dotForEnv(e.code)" />{{ e.code }}</div>
              <div class="cl">{{ e.displayName }}</div>
            </div>
            <div>
              <VSelect v-if="isAdmin" :model-value="tierOptionLabel(e.tierCode)" :options="tierOptions" @update:model-value="(v: string) => rebind(e.code, v)" />
              <span v-else class="mono mute">{{ e.tierCode }}</span>
            </div>
            <div class="ctr mono">{{ envCount(e.code) }}</div>
            <div class="acts">
              <button v-if="isAdmin" class="del" :disabled="envtier.environments.length <= 1 || busy" :title="envtier.environments.length <= 1 ? $t('etBlockedLastEnv') : ''" @click="openDeleteEnv(e.code)">
                {{ $t('etDelete') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="envForm" class="form">
        <div class="fhead">{{ $t('etNewEnv') }}<span class="x" @click="envForm = false"><X :size="16" /></span></div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFCode') }}</div><input v-model="ef.code" placeholder="prod-hk" /></div>
          <div><div class="fl">{{ $t('etFName') }}</div><input v-model="ef.displayName" :placeholder="$t('etFEnvNamePh')" /></div>
        </div>
        <div class="frow">
          <div><div class="fl">{{ $t('etColTierBind') }}</div><VSelect v-model="ef.tierCode" :options="envtier.tierCodes" /></div>
          <div />
        </div>
        <div class="note">{{ $t('etEnvCreateNote', { tier: ef.tierCode || '—' }) }}</div>
        <div class="ffoot">
          <VButton variant="secondary" height="34px" @click="envForm = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" height="34px" :disabled="busy || !ef.code || !ef.displayName || !ef.tierCode" @click="createEnvironment">{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>

    <!-- Deleting an environment always relocates its instances. -->
    <div v-if="delEnv.open" class="ovl">
      <div class="mask" @click="delEnv.open = false" />
      <div class="modal">
        <div class="mhead">{{ $t('etDeleteEnvTitle', { code: delEnv.code }) }}</div>
        <div class="mbody">
          <div class="mwarn">{{ $t('etDeleteEnvWarn', { n: envCount(delEnv.code) }) }}</div>
          <div class="fl">{{ $t('etMoveTo') }}</div>
          <VSelect v-model="delEnv.moveTo" :options="moveTargets" />
        </div>
        <div class="mfoot">
          <VButton variant="secondary" height="34px" @click="delEnv.open = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="danger" height="34px" :disabled="busy || !delEnv.moveTo" @click="confirmDeleteEnv">{{ $t('etDelete') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { padding: var(--page-pad); padding-bottom: 40px; max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 20px; }
.eyebrow { font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); }
.sub { margin-top: 6px; font: 400 12.5px/1.6 var(--font-body); color: var(--text-muted); max-width: 720px; }
.roflag { margin-left: auto; font: 600 11px var(--font-mono); color: var(--warning-text); }

.card { margin-bottom: 22px; border: 1px solid var(--border-subtle); border-radius: 16px; background: var(--surface-card); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 15px 18px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 34px; height: 34px; border-radius: 10px; background: var(--surface-sunken); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; }
.ct { font: 700 14px var(--font-display); color: var(--text-strong); }
.cs { margin-top: 3px; font: 400 11.5px/1.5 var(--font-body); color: var(--text-muted); }

/* Both tables scroll inside their own card; the page body never moves sideways. */
.tgrid, .egrid { min-width: max-content; }
.tgrid .th, .tgrid .tr { display: grid; grid-template-columns: minmax(190px, 2fr) 88px 88px 88px 96px 120px minmax(160px, 1.4fr) 90px; gap: 10px; align-items: center; }
.egrid .th, .egrid .tr { display: grid; grid-template-columns: minmax(190px, 2fr) minmax(220px, 1.6fr) 96px 90px; gap: 10px; align-items: center; }
.th { padding: 11px 18px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); letter-spacing: 0.05em; color: var(--text-faint); text-transform: uppercase; }
.tr { padding: 11px 18px; border-bottom: 1px solid var(--border-subtle); }
.tr:last-child { border-bottom: none; }
.ctr { display: flex; justify-content: center; }
.cn { display: flex; align-items: center; gap: 7px; font: 600 13px var(--font-mono); color: var(--text-strong); }
.cl { margin-top: 3px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.cl2 { margin-top: 2px; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.mono { font: 500 12px var(--font-mono); }
.mute { color: var(--text-muted); }
.dash { color: var(--text-faint); }
.d { width: 7px; height: 7px; border-radius: 50%; }
.d.danger { background: var(--danger); }
.d.info { background: #3b82f6; }
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
.d.muted { background: var(--text-faint); }

.basebadge { display: inline-flex; align-items: center; gap: 5px; padding: 3px 9px; border-radius: 999px; background: var(--success-subtle, rgba(24, 179, 104, 0.12)); color: var(--success-text); font: 600 10.5px var(--font-mono); }
.baselink { border: 1px solid var(--border-subtle); border-radius: 8px; background: transparent; color: var(--text-muted); font: 500 11px var(--font-body); padding: 4px 10px; cursor: pointer; }
.baselink:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.baselink:disabled { opacity: 0.5; cursor: not-allowed; }

.acts { display: flex; justify-content: flex-end; }
.del { border: 1px solid var(--border-subtle); border-radius: 8px; background: transparent; color: var(--danger-text); font: 500 11px var(--font-body); padding: 4px 10px; cursor: pointer; }
.del:hover:not(:disabled) { background: var(--danger-subtle); }
/* Disabled deletes keep a tooltip explaining which guard is holding — a dead
   button with no reason reads as a bug. */
.del:disabled { opacity: 0.4; cursor: not-allowed; }

.form { padding: 16px 18px 18px; border-top: 1px solid var(--border-subtle); background: var(--surface-sunken); }
.fhead { display: flex; align-items: center; font: 700 13px var(--font-display); color: var(--text-strong); margin-bottom: 12px; }
.fhead .x { margin-left: auto; cursor: pointer; color: var(--text-faint); display: flex; }
.frow { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 12px; }
.frow3 { display: flex; flex-wrap: wrap; gap: 18px; margin: 12px 0; }
.chk { display: flex; align-items: center; gap: 8px; font: 500 12px var(--font-body); color: var(--text-body); }
.fl { font: 600 11px var(--font-mono); color: var(--text-muted); margin-bottom: 6px; }
.form input { width: 100%; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-card); color: var(--text-strong); font: 500 12.5px var(--font-mono); outline: none; }
.form input:focus { border-color: var(--accent-text); }
.warn { padding: 9px 12px; border-radius: 9px; background: var(--warning-subtle, rgba(245, 165, 36, 0.1)); color: var(--warning-text); font: 500 11.5px/1.6 var(--font-body); }
.note { padding: 9px 12px; border-radius: 9px; background: var(--surface-card); color: var(--text-muted); font: 500 11.5px/1.6 var(--font-body); }
.ffoot { margin-top: 14px; display: flex; justify-content: flex-end; gap: 10px; }

.ovl { position: fixed; inset: 0; z-index: 60; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 440px; max-width: 94vw; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 16px; overflow: hidden; }
.mhead { padding: 16px 20px; font: 700 15px var(--font-display); color: var(--text-strong); border-bottom: 1px solid var(--border-subtle); }
.mbody { padding: 16px 20px; }
.mwarn { margin-bottom: 14px; padding: 10px 12px; border-radius: 9px; background: var(--danger-subtle); color: var(--danger-text); font: 500 12px/1.6 var(--font-body); }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 14px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
